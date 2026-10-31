package content

import (
	"math"
	"strings"

	"github.com/resolver/crawler/modules"
	"golang.org/x/net/html"
)

// Iranian sites often label toman amounts "IRR" in their structured data
// (Divar does), or rial amounts "IRT". The price the page shows its visitors
// names the unit in words (تومان / ریال), so the structured prices are checked
// against it and relabelled when the page disagrees.

const (
	// maxPriceText bounds the text read around a تومان/ریال word: enough for
	// "قیمت: ۱,۵۰۰,۰۰۰ تومان", short enough not to pick up a neighbour's number.
	maxPriceText = 80
	// maxPriceMentions bounds the work on pages that list many prices.
	maxPriceMentions = 200
)

// skipTextTags hold text a visitor does not see.
var skipTextTags = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "head": true}

// iranianCurrencyWords name the toman and the rial in page text.
var iranianCurrencyWords = []string{"تومان", "تومن", "ریال", "toman", "rial", "﷼"}

// visiblePriceMentions returns the toman and rial prices written in the
// page's visible text, such as "‏۱,۵۰۰,۰۰۰ تومان" or "<b>1,500,000</b> <span>ریال</span>".
func visiblePriceMentions(root *html.Node) []modules.Price {
	var out []modules.Price
	seen := map[*html.Node]bool{}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if len(out) >= maxPriceMentions {
			return
		}
		if n.Type == html.ElementNode && skipTextTags[n.Data] {
			return
		}
		if n.Type == html.TextNode && hasIranianCurrencyWord(n.Data) {
			// The number may sit in a sibling element: climb to the
			// smallest ancestor whose text holds a digit.
			for el := n.Parent; el != nil && el.Type == html.ElementNode; el = el.Parent {
				text := strings.Join(strings.Fields(visibleText(el)), " ")
				if len([]rune(text)) > maxPriceText {
					break
				}
				if !hasDigit(text) {
					continue
				}
				if !seen[el] {
					seen[el] = true
					if pr, ok := modules.ParsePrice(text); ok && pr.Amount > 0 && (pr.Currency == "IRT" || pr.Currency == "IRR") {
						out = append(out, pr)
					}
				}
				break
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

func hasIranianCurrencyWord(s string) bool {
	s = strings.ToLower(modules.NormalizePersian(s))
	for _, w := range iranianCurrencyWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func hasDigit(s string) bool {
	for _, r := range modules.ToLatinDigits(s) {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// visibleText is the text of n and its descendants, minus invisible elements.
func visibleText(n *html.Node) string {
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skipTextTags[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// inRials converts a toman or rial price to rials (1 toman = 10 rials).
func inRials(p modules.Price) float64 {
	if p.Currency == "IRT" {
		return p.Amount * 10
	}
	return p.Amount
}

func sameAmount(a, b float64) bool {
	return math.Abs(a-b) <= 0.0005*math.Max(math.Abs(a), math.Abs(b))
}

// pageCurrency says which unit the page text gives a structured amount:
// "IRR" when a shown price equals amount rials, "IRT" when it equals amount
// tomans, "" when the page shows neither or both equally often.
func pageCurrency(amount float64, mentions []modules.Price) string {
	rial, toman := 0, 0
	for _, m := range mentions {
		switch v := inRials(m); {
		case sameAmount(v, amount):
			rial++
		case sameAmount(v, amount*10):
			toman++
		}
	}
	switch {
	case toman > rial:
		return "IRT"
	case rial > toman:
		return "IRR"
	}
	return ""
}

// fixIranianCurrency relabels the product's prices when the page shows them
// in the other Iranian unit. The main price decides, then the regular and
// range prices if there is no main price evidence; one mislabel applies to
// every price the product read with that label, since sites mislabel all
// their prices the same way. An unlabelled price gets the unit the page
// shows it in.
func fixIranianCurrency(pg *pageData, p *Product) {
	prices := []*modules.Price{p.Price, p.RegularPrice, p.LowPrice, p.HighPrice}
	relevant := false
	for _, pr := range prices {
		if pr != nil && (pr.Currency == "IRR" || pr.Currency == "IRT" || pr.Currency == "") {
			relevant = true
		}
	}
	if !relevant {
		return
	}
	mentions := visiblePriceMentions(pg.root)
	if len(mentions) == 0 {
		return
	}
	fixes := map[string]string{} // labelled currency → currency the page shows
	for _, pr := range prices {
		if pr == nil {
			continue
		}
		if _, done := fixes[pr.Currency]; done {
			continue
		}
		switch pr.Currency {
		case "IRR", "IRT":
			if shown := pageCurrency(pr.Amount, mentions); shown != "" {
				fixes[pr.Currency] = shown
			}
		case "":
			// Without a label only an exact match is evidence; "150,000
			// تومان" for an unlabelled 1500000 is too weak to call it rials.
			for _, m := range mentions {
				if sameAmount(m.Amount, pr.Amount) {
					fixes[""] = m.Currency
					break
				}
			}
		}
	}
	changed := false
	done := map[*modules.Price]bool{}
	for _, pr := range prices {
		if pr == nil || done[pr] {
			continue
		}
		done[pr] = true
		if to, ok := fixes[pr.Currency]; ok && to != pr.Currency {
			from := pr.Currency
			pr.Currency = to
			pr.Raw = strings.TrimSpace(strings.TrimSuffix(pr.Raw, from)) + " " + to
			changed = true
		}
	}
	if changed {
		p.Sources["currency"] = "page_text"
	}
}
