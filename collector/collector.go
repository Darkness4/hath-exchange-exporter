package collector

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Darkness4/hath-exchange-exporter/scraper"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Hath scrapes the exchange on a fixed interval and keeps the latest
// HathStats in memory. OTEL observable (async) instruments read from it on
// every collection cycle.
type Hath struct {
	scraper  scraper.Scraper
	interval time.Duration

	mu      sync.RWMutex
	latest  *scraper.HathStats
	lastErr error
}

// NewHath creates a new Hath collector.
func NewHath(hc *http.Client, scrapeURL string, interval time.Duration) *Hath {
	return &Hath{
		scraper:  scraper.New(hc, scrapeURL),
		interval: interval,
	}
}

// Start registers all OTEL instruments against meter and launches the
// background scrape loop. Call once at startup.
func (c *Hath) Start(ctx context.Context, meter metric.Meter) error {
	if err := c.registerInstruments(meter); err != nil {
		return err
	}
	go c.loop(ctx)
	return nil
}

// loop runs an immediate scrape then repeats on the configured interval.
func (c *Hath) loop(ctx context.Context) {
	c.scrape()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.scrape()
		}
	}
}

func (c *Hath) scrape() {
	stats, err := c.scraper.Fetch()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		log.Error().Err(err).Msg("scrape error")
		c.lastErr = err
		return
	}
	c.latest = stats
	c.lastErr = nil
	log.Info().
		Int64("best_bid", bestBid(stats)).
		Int64("best_ask", bestAsk(stats)).
		Int64("8h_avg", stats.Window8h.Avg).
		Msg("scraped OK")
}

// registerInstruments creates all OTEL async gauge instruments.
func (c *Hath) registerInstruments(meter metric.Meter) error {
	// Helper to register an Int64ObservableGauge and bail on error.
	gauge := func(name, desc string, cb metric.Int64Callback) error {
		_, err := meter.Int64ObservableGauge(name,
			metric.WithDescription(desc),
			metric.WithInt64Callback(cb),
		)
		return err
	}

	registrations := []struct {
		name string
		desc string
		cb   metric.Int64Callback
	}{
		// ---- Price windows -------------------------------------------------
		{
			"hath_exchange.price.high",
			"Highest trade price (credits/Hath) over the window",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				o.Observe(s.Window8h.High, metric.WithAttributes(attrWindow("8h")))
				o.Observe(s.Window24h.High, metric.WithAttributes(attrWindow("24h")))
				return nil
			},
		},
		{
			"hath_exchange.price.low",
			"Lowest trade price (credits/Hath) over the window",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				o.Observe(s.Window8h.Low, metric.WithAttributes(attrWindow("8h")))
				o.Observe(s.Window24h.Low, metric.WithAttributes(attrWindow("24h")))
				return nil
			},
		},
		{
			"hath_exchange.price.avg",
			"Average trade price (credits/Hath) over the window",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				o.Observe(s.Window8h.Avg, metric.WithAttributes(attrWindow("8h")))
				o.Observe(s.Window24h.Avg, metric.WithAttributes(attrWindow("24h")))
				return nil
			},
		},
		{
			"hath_exchange.volume",
			"Total Hath traded over the window",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				o.Observe(s.Window8h.Volume, metric.WithAttributes(attrWindow("8h")))
				o.Observe(s.Window24h.Volume, metric.WithAttributes(attrWindow("24h")))
				return nil
			},
		},

		// ---- Best bid / ask / spread ---------------------------------------
		{
			"hath_exchange.best_bid",
			"Best (highest) active bid price in credits",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				if v := bestBid(s); v > 0 {
					o.Observe(v)
				}
				return nil
			},
		},
		{
			"hath_exchange.best_ask",
			"Best (lowest) active ask price in credits",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				if v := bestAsk(s); v > 0 {
					o.Observe(v)
				}
				return nil
			},
		},
		{
			"hath_exchange.spread",
			"Bid-ask spread in credits (best_ask - best_bid)",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				bb, ba := bestBid(s), bestAsk(s)
				if bb > 0 && ba > 0 {
					o.Observe(ba - bb)
				}
				return nil
			},
		},

		// ---- Order book depth (top 20 levels each side) --------------------
		{
			"hath_exchange.orderbook.bid.price",
			"Bid price at each order book level (rank=0 is best)",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				for i, lvl := range s.Bids {
					o.Observe(lvl.Price, metric.WithAttributes(attrRank(i)))
				}
				return nil
			},
		},
		{
			"hath_exchange.orderbook.bid.quantity",
			"Hath quantity at each bid level",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				for i, lvl := range s.Bids {
					o.Observe(lvl.Quantity, metric.WithAttributes(attrRank(i)))
				}
				return nil
			},
		},
		{
			"hath_exchange.orderbook.ask.price",
			"Ask price at each order book level (rank=0 is best)",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				for i, lvl := range s.Asks {
					o.Observe(lvl.Price, metric.WithAttributes(attrRank(i)))
				}
				return nil
			},
		},
		{
			"hath_exchange.orderbook.ask.quantity",
			"Hath quantity at each ask level",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				for i, lvl := range s.Asks {
					o.Observe(lvl.Quantity, metric.WithAttributes(attrRank(i)))
				}
				return nil
			},
		},

		// ---- Last transaction ----------------------------------------------
		{
			"hath_exchange.last_tx.price",
			"Price (credits/Hath) of the most recent transaction",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil || len(s.Transactions) == 0 {
					return nil
				}
				o.Observe(s.Transactions[0].Price)
				return nil
			},
		},
		{
			"hath_exchange.last_tx.quantity",
			"Hath volume of the most recent transaction",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil || len(s.Transactions) == 0 {
					return nil
				}
				o.Observe(s.Transactions[0].Quantity)
				return nil
			},
		},

		// ---- Wallet (auth only) --------------------------------------------
		{
			"hath_exchange.wallet.credits",
			"Credits available in the authenticated account",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil || s.AvailableCredits == 0 {
					return nil
				}
				o.Observe(s.AvailableCredits)
				return nil
			},
		},
		{
			"hath_exchange.wallet.hath",
			"Hath available in the authenticated account",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil || s.AvailableHath == 0 {
					return nil
				}
				o.Observe(s.AvailableHath)
				return nil
			},
		},

		// ---- Scrape health -------------------------------------------------
		{
			"hath_exchange.scrape.success",
			"1 if the last scrape succeeded, 0 otherwise",
			func(_ context.Context, o metric.Int64Observer) error {
				c.mu.RLock()
				err := c.lastErr
				c.mu.RUnlock()
				if err == nil {
					o.Observe(1)
				} else {
					o.Observe(0)
				}
				return nil
			},
		},
		{
			"hath_exchange.scrape.timestamp",
			"Unix timestamp of the last successful scrape",
			func(_ context.Context, o metric.Int64Observer) error {
				s := c.snapshot()
				if s == nil {
					return nil
				}
				o.Observe(s.ScrapedAt.Unix())
				return nil
			},
		},
	}

	for _, r := range registrations {
		if err := gauge(r.name, r.desc, r.cb); err != nil {
			return err
		}
	}
	return nil
}

// snapshot returns a read-locked copy of the latest stats (nil if none yet).
func (c *Hath) snapshot() *scraper.HathStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest
}

func bestBid(s *scraper.HathStats) int64 {
	if len(s.Bids) == 0 {
		return 0
	}
	return s.Bids[0].Price
}

func bestAsk(s *scraper.HathStats) int64 {
	if len(s.Asks) == 0 {
		return 0
	}
	return s.Asks[0].Price
}

func attrWindow(w string) attribute.KeyValue {
	return attribute.String("window", w)
}

func attrRank(i int) attribute.KeyValue {
	return attribute.String("rank", strconv.Itoa(i))
}
