package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Darkness4/hath-exchange-exporter/collector"
	"github.com/Darkness4/hath-exchange-exporter/cookie"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/urfave/cli/v3"
	"go.opentelemetry.io/otel"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

var version string

func main() {
	// Pretty console logging by default; set LOG_FORMAT=json for production.
	if os.Getenv("LOG_FORMAT") != "json" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	}

	app := &cli.Command{
		Name:    "hath-exporter",
		Usage:   "Prometheus / OTEL exporter for the Hath Exchange",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "listen-address",
				Aliases: []string{"l"},
				Value:   ":9101",
				Usage:   "HTTP listen address for /metrics and /healthz",
				Sources: cli.EnvVars("HATH_LISTEN_ADDRESS"),
			},
			&cli.StringFlag{
				Name:    "scrape-url",
				Value:   "https://e-hentai.org/exchange.php?t=hath",
				Usage:   "Hath Exchange page URL",
				Sources: cli.EnvVars("HATH_SCRAPE_URL"),
			},
			&cli.DurationFlag{
				Name:    "interval",
				Aliases: []string{"i"},
				Value:   5 * time.Minute,
				Usage:   "How often to scrape the exchange page",
				Sources: cli.EnvVars("HATH_INTERVAL"),
			},
			&cli.StringFlag{
				Name:    "cookie-file",
				Aliases: []string{"c"},
				Value:   "",
				Usage:   "File containing the Cookie header value (required for wallet metrics)",
				Sources: cli.EnvVars("HATH_COOKIE_FILE"),
			},
			&cli.BoolFlag{
				Name:    "debug",
				Value:   false,
				Usage:   "Enable debug-level logging",
				Sources: cli.EnvVars("HATH_DEBUG"),
			},
		},
		Action: run,
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		log.Fatal().Err(err).Msg("exiting")
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	if cmd.Bool("debug") {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	// --- Signal handling / root context ------------------------------------
	ctx, stop := context.WithCancelCause(ctx)

	cleanChan := make(chan os.Signal, 1)
	signal.Notify(cleanChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-cleanChan
		log.Warn().Stringer("signal", sig).Msg("received signal, shutting down")
		stop(fmt.Errorf("signal received: %s", sig))
	}()
	defer signal.Stop(cleanChan)

	jar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("creating cookie jar: %w", err)
	}
	if err := cookie.ParseFromFile(jar, cmd.String("cookie-file")); err != nil {
		return fmt.Errorf("parsing cookie file: %w", err)
	}
	hc := &http.Client{
		Jar:     jar,
		Timeout: 5 * time.Second,
	}

	// --- OTEL: Prometheus exporter -----------------------------------------
	reg := prometheus.NewRegistry()
	promExporter, err := otelprometheus.New(
		otelprometheus.WithRegisterer(reg),
	)
	if err != nil {
		return fmt.Errorf("creating Prometheus exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("hath-exporter"),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return fmt.Errorf("creating OTEL resource: %w", err)
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithReader(promExporter),
		metric.WithResource(res),
	)

	otel.SetMeterProvider(meterProvider)
	meter := meterProvider.Meter("hath_exchange")

	// --- Scraper -----------------------------------------------------------
	collector := collector.NewHath(
		hc,
		cmd.String("scrape-url"),
		cmd.Duration("interval"),
	)
	if err := collector.Start(ctx, meter); err != nil {
		return fmt.Errorf("starting collector: %w", err)
	}

	// --- HTTP server -------------------------------------------------------
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              cmd.String("listen-address"),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Serve in a goroutine; send errors to a channel.
	srvErr := make(chan error, 1)
	go func() {
		log.Info().
			Str("addr", srv.Addr).
			Dur("scrape_interval", cmd.Duration("interval")).
			Msg("hath-exporter started")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
		close(srvErr)
	}()

	// --- Wait for shutdown or server error ---------------------------------
	select {
	case err := <-srvErr:
		// Server died on its own — context may still be live.
		stop(fmt.Errorf("http server error: %w", err))
		return fmt.Errorf("http server: %w", err)

	case <-ctx.Done():
		cause := context.Cause(ctx)
		log.Info().Err(cause).Msg("shutting down http server")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("http server shutdown error")
		}

		// Flush OTEL metrics before exit.
		if err := meterProvider.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("meter provider shutdown error")
		}

		log.Info().Err(cause).Msg("shutdown complete")
		return nil
	}
}
