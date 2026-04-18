package scraper

import (
	"strings"
	"testing"

	_ "embed"
)

//go:embed testdata/exchange.html
var fixture string

func TestParseExchangePage(t *testing.T) {
	stats, err := parseExchangePage(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("parseExchangePage: %v", err)
	}

	// 8h window
	t.Run("Window8h", func(t *testing.T) {
		w := stats.Window8h
		assertInt64(t, "High", 2974, w.High)
		assertInt64(t, "Low", 2940, w.Low)
		assertInt64(t, "Avg", 2955, w.Avg)
		assertInt64(t, "Volume", 18341, w.Volume)
	})

	// 24h window
	t.Run("Window24h", func(t *testing.T) {
		w := stats.Window24h
		assertInt64(t, "High", 2979, w.High)
		assertInt64(t, "Low", 2937, w.Low)
		assertInt64(t, "Avg", 2957, w.Avg)
		assertInt64(t, "Volume", 31828, w.Volume)
	})

	// Order book
	t.Run("Bids", func(t *testing.T) {
		if len(stats.Bids) == 0 {
			t.Fatal("expected bids, got none")
		}
		// Best bid should be 2940 @ 94 Hath
		assertInt64(t, "Bids[0].Price", 2940, stats.Bids[0].Price)
		assertInt64(t, "Bids[0].Quantity", 94, stats.Bids[0].Quantity)
	})

	t.Run("Asks", func(t *testing.T) {
		if len(stats.Asks) == 0 {
			t.Fatal("expected asks, got none")
		}
		// Best ask should be 2972 @ 1326 Hath
		assertInt64(t, "Asks[0].Price", 2972, stats.Asks[0].Price)
		assertInt64(t, "Asks[0].Quantity", 1326, stats.Asks[0].Quantity)
	})

	// Transactions
	t.Run("Transactions", func(t *testing.T) {
		if len(stats.Transactions) == 0 {
			t.Fatal("expected transactions, got none")
		}
		// Most recent transaction: 3 Hath @ 2940 Credits
		assertInt64(t, "Transactions[0].Price", 2940, stats.Transactions[0].Price)
		assertInt64(t, "Transactions[0].Quantity", 3, stats.Transactions[0].Quantity)
	})

	// Wallet (from page: 98,169 Credits / 50 Hath)
	t.Run("Wallet", func(t *testing.T) {
		assertInt64(t, "AvailableCredits", 98169, stats.AvailableCredits)
		assertInt64(t, "AvailableHath", 50, stats.AvailableHath)
	})
}

func assertInt64(t *testing.T, name string, want, got int64) {
	t.Helper()
	if got != want {
		t.Errorf("%s: want %d, got %d", name, want, got)
	}
}
