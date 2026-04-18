package scraper

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Darkness4/hath-exchange-exporter/useragent"
	"golang.org/x/net/html"
)

// HathStats holds all data parsed from the exchange page.
type HathStats struct {
	Window8h  PriceWindow
	Window24h PriceWindow

	Bids []OrderLevel // index 0 = best bid
	Asks []OrderLevel // index 0 = best ask

	Transactions []Transaction // index 0 = most recent

	AvailableCredits int64 // wallet (requires auth)
	AvailableHath    int64 // wallet (requires auth)

	ScrapedAt time.Time
}

// PriceWindow holds statistics for a rolling time window.
type PriceWindow struct {
	High   int64
	Low    int64
	Avg    int64
	Volume int64 // in Hath
}

// OrderLevel is a single price level in the order book.
type OrderLevel struct {
	Quantity int64
	Price    int64 // credits per Hath
}

// Transaction is a single completed trade.
type Transaction struct {
	Price    int64 // credits per Hath
	Quantity int64 // Hath
}

// Scraper fetches and parses the exchange page.
type Scraper struct {
	url        string
	httpClient *http.Client
	userAgent  string
}

// New creates a Scraper.
func New(hc *http.Client, url string) Scraper {
	if hc == nil {
		hc = http.DefaultClient
	}
	return Scraper{
		url:        url,
		httpClient: hc,
		userAgent:  useragent.Get(),
	}
}

// Fetch downloads the exchange page and returns parsed stats.
func (s *Scraper) Fetch() (*HathStats, error) {
	req, err := http.NewRequest("GET", s.url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("User-Agent", s.userAgent)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}

	return parseExchangePage(resp.Body)
}

// ---- Parser ----------------------------------------------------------------

// reNumber matches one or more digit groups separated by commas, e.g. "2,974" or "18341".
var reNumber = regexp.MustCompile(`[\d,]+`)

// extractInt strips formatting and returns the integer value from a raw text
// node that may contain commas, surrounding words, and whitespace.
// e.g. "2,974 Credits" → 2974, "18,341 Hath" → 18341, "  50  " → 50
func extractInt(raw string) int64 {
	m := reNumber.FindString(raw)
	if m == "" {
		return 0
	}
	m = strings.ReplaceAll(m, ",", "")
	n, _ := strconv.ParseInt(m, 10, 64)
	return n
}

// innerText returns the concatenated text content of a node and all its
// descendants, with whitespace collapsed.
func innerText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

// attr returns the value of an attribute on an element node, or "".
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// isElem returns true if n is an element node with the given tag.
func isElem(n *html.Node, tag string) bool {
	return n.Type == html.ElementNode && n.Data == tag
}

// children returns the direct element-node children of n.
func children(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			out = append(out, c)
		}
	}
	return out
}

// findAll does a depth-first search and returns all element nodes matching tag.
func findAll(root *html.Node, tag string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if isElem(n, tag) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// findByID returns the first element node with the given id attribute, or nil.
func findByID(root *html.Node, id string) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && attr(n, "id") == id {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return found
}

// nextSiblingElem returns the next sibling that is an element node, or nil.
func nextSiblingElem(n *html.Node) *html.Node {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode {
			return s
		}
	}
	return nil
}

// parseExchangePage is the top-level parser.
func parseExchangePage(r io.Reader) (*HathStats, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing HTML: %w", err)
	}

	stats := &HathStats{ScrapedAt: time.Now()}

	parsePriceWindows(doc, stats)
	parseOrderBook(doc, stats)
	parseTransactions(doc, stats)
	parseWallet(doc, stats)

	return stats, nil
}

// ---- Price windows ---------------------------------------------------------
//
// Structure:
//   <div class="outer">
//     <div>
//       <h2>Last 8 Hours (Credits per Hath)</h2>
//       <div>
//         <strong>High</strong>: 2,974 Credits &nbsp;
//         <strong>Low</strong>: 2,940 Credits &nbsp;
//         ...
//       </div>
//     </div>
//     <div> ... 24h ... </div>
//   </div>
//
// Strategy: find every <h2>, check its text for "8 hour"/"24 hour", then walk
// the parent <div> collecting <strong> label → next text node value pairs.

func parsePriceWindows(doc *html.Node, stats *HathStats) {
	for _, h2 := range findAll(doc, "h2") {
		text := innerText(h2)
		var pw *PriceWindow
		switch {
		case strings.Contains(strings.ToLower(text), "8 hour"):
			pw = &stats.Window8h
		case strings.Contains(strings.ToLower(text), "24 hour"):
			pw = &stats.Window24h
		default:
			continue
		}

		// The stats are in the <div> sibling that follows the <h2> within the
		// same parent <div>. Walk all <strong> nodes inside that sibling div.
		statsDiv := nextSiblingElem(h2)
		if statsDiv == nil || !isElem(statsDiv, "div") {
			continue
		}

		for _, strong := range findAll(statsDiv, "strong") {
			label := innerText(strong)
			// The value is in the text node immediately after the <strong>.
			valueNode := strong.NextSibling
			if valueNode == nil {
				continue
			}
			// The text node after <strong> looks like ": 2,974 Credits \u00a0"
			raw := valueNode.Data
			switch label {
			case "High":
				pw.High = extractInt(raw)
			case "Low":
				pw.Low = extractInt(raw)
			case "Avg":
				pw.Avg = extractInt(raw)
			case "Vol":
				pw.Volume = extractInt(raw)
			}
		}
	}
}

// ---- Order book ------------------------------------------------------------
//
// Structure:
//   <div> (active orders outer)
//     <h2>Active Orders</h2>
//     <div style="float:left"> (bid side)
//       <h3>Bid (Buyers)</h3>
//       <table>
//         <tr><td>94 Hath</td><td>@</td><td>2,940 Credits</td></tr>
//         ...
//       </table>
//     </div>
//     <div style="float:left"> (ask side)
//       <h3>Ask (Sellers)</h3>
//       <table> ... </table>
//     </div>
//   </div>
//
// Strategy: find each <h3>, check its text, then find the <table> sibling
// and parse its rows. Each row's <td> nodes give qty / separator / price.

func parseOrderBook(doc *html.Node, stats *HathStats) {
	for _, h3 := range findAll(doc, "h3") {
		text := innerText(h3)

		var target *[]OrderLevel
		switch text {
		case "Bid (Buyers)":
			target = &stats.Bids
		case "Ask (Sellers)":
			target = &stats.Asks
		default:
			continue
		}

		// Find the <table> that is a sibling of this <h3> within the same parent.
		table := nextSiblingElem(h3)
		if table == nil || !isElem(table, "table") {
			continue
		}

		for _, tr := range findAll(table, "tr") {
			tds := children(tr)
			// Expect exactly 3 <td>: qty | "@" | price
			if len(tds) != 3 {
				continue
			}
			qty := extractInt(innerText(tds[0]))
			price := extractInt(innerText(tds[2]))
			if qty <= 0 || price <= 0 {
				continue
			}
			*target = append(*target, OrderLevel{Quantity: qty, Price: price})
		}
	}
}

// ---- Transactions ----------------------------------------------------------
//
// Structure:
//   <table id="historytable">
//     <tr><th>Time</th><th>Seller</th><th>Buyer</th><th>Volume</th><th>Unit Cost</th></tr>
//     <tr>
//       <td>15:01</td>
//       <td><div>seller</div></td>
//       <td><div>buyer</div></td>
//       <td>3 Hath</td>
//       <td>2,940 Credits</td>
//     </tr>
//     ...
//   </table>
//
// Strategy: find the table by id, skip the header row, parse data rows by
// column index (3=qty, 4=price).

func parseTransactions(doc *html.Node, stats *HathStats) {
	table := findByID(doc, "historytable")
	if table == nil {
		return
	}

	rows := findAll(table, "tr")
	// rows[0] is the header — skip it.
	for _, tr := range rows[1:] {
		tds := children(tr)
		// Columns: 0=time, 1=seller, 2=buyer, 3=volume, 4=unit cost
		if len(tds) < 5 {
			continue
		}
		qty := extractInt(innerText(tds[3]))
		price := extractInt(innerText(tds[4]))
		if qty <= 0 || price <= 0 {
			continue
		}
		stats.Transactions = append(stats.Transactions, Transaction{
			Quantity: qty,
			Price:    price,
		})
	}
}

// ---- Wallet ----------------------------------------------------------------
//
// Structure (inside buy/sell form divs):
//   <div style="margin-top:5px; font-weight:bold">Available: 98,169 Credits</div>
//   <div style="margin-top:5px; font-weight:bold">Available: 50 Hath</div>
//
// Strategy: find all <div> nodes whose raw text starts with "Available:",
// then use extractInt + suffix detection.

func parseWallet(doc *html.Node, stats *HathStats) {
	for _, div := range findAll(doc, "div") {
		text := innerText(div)
		if !strings.HasPrefix(text, "Available:") {
			continue
		}
		switch {
		case strings.HasSuffix(text, "Credits"):
			stats.AvailableCredits = extractInt(text)
		case strings.HasSuffix(text, "Hath"):
			stats.AvailableHath = extractInt(text)
		}
	}
}
