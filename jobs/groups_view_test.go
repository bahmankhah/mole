package jobs

import (
	"testing"
	"time"

	"github.com/resolver/crawler/models"
)

func num(v float64) *float64 { return &v }

func obs(jobID, url string, at time.Time, value *float64, text, currency string) models.FieldObservation {
	return models.FieldObservation{
		CrawlJobID: jobID, URL: url, ObservedAt: at,
		NumValue: value, TextValue: text, Currency: currency,
	}
}

// Several pages of one site in the same run are one value, not a history:
// the table used to read two of them as "this run" and "the run before",
// and reported the gap between two listings as a price change.
func TestGroupObservationsByRunFoldsPagesOfOneRun(t *testing.T) {
	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	runs := groupObservationsByRun([]models.FieldObservation{
		obs("job-1", "https://shop.ir/a", at, num(433500000), "", "IRR"),
		obs("job-1", "https://shop.ir/b", at, num(287010000), "", "IRR"),
		obs("job-1", "https://shop.ir/c", at, num(300000000), "", "IRR"),
	})
	if len(runs) != 1 {
		t.Fatalf("three pages of one run = %d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.num == nil || *r.num != 287010000 {
		t.Errorf("value = %v, want the lowest offer 287010000", r.num)
	}
	if r.url != "https://shop.ir/b" {
		t.Errorf("url = %q, want the page the value came from", r.url)
	}
	if r.low == nil || r.high == nil || *r.low != 287010000 || *r.high != 433500000 {
		t.Errorf("spread = %v..%v, want 287010000..433500000", r.low, r.high)
	}
	if r.pages != 3 || len(r.alsoURLs) != 2 {
		t.Errorf("pages = %d with %d other URLs, want 3 and 2", r.pages, len(r.alsoURLs))
	}
}

func TestGroupObservationsByRunOrdersRuns(t *testing.T) {
	first := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	second := first.Add(24 * time.Hour)
	runs := groupObservationsByRun([]models.FieldObservation{
		obs("job-1", "https://shop.ir/a", first, num(100), "", "IRR"),
		obs("job-2", "https://shop.ir/a", second, num(90), "", "IRR"),
	})
	if len(runs) != 2 {
		t.Fatalf("two runs = %d", len(runs))
	}
	if *runs[0].num != 100 || *runs[1].num != 90 {
		t.Errorf("runs = %v, %v; want oldest first", *runs[0].num, *runs[1].num)
	}
}

// A run whose currencies disagree compares only the ones in the currency
// most of its pages used; a stray other-currency page is not "cheapest".
func TestGroupObservationsByRunIgnoresOtherCurrencies(t *testing.T) {
	at := time.Now()
	runs := groupObservationsByRun([]models.FieldObservation{
		obs("job-1", "https://shop.ir/a", at, num(400), "", "IRR"),
		obs("job-1", "https://shop.ir/b", at, num(500), "", "IRR"),
		obs("job-1", "https://shop.ir/c", at, num(9), "", "USD"),
	})
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if runs[0].currency != "IRR" {
		t.Errorf("currency = %q, want IRR (what most pages used)", runs[0].currency)
	}
	if runs[0].num == nil || *runs[0].num != 400 {
		t.Errorf("value = %v, want 400; a USD price is not comparable with rials", runs[0].num)
	}
}

func TestBuildCellPreviousIsThePreviousRun(t *testing.T) {
	first := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	second := first.Add(24 * time.Hour)
	members := []models.ProductGroupMember{
		{ID: 7, SiteKey: "shop.ir", URL: "https://shop.ir/a"},
		{ID: 8, SiteKey: "shop.ir", URL: "https://shop.ir/b"},
	}
	cell := buildCell([]models.FieldObservation{
		obs("job-1", "https://shop.ir/a", first, num(100), "", "IRR"),
		obs("job-1", "https://shop.ir/b", first, num(130), "", "IRR"),
		obs("job-2", "https://shop.ir/a", second, num(90), "", "IRR"),
		obs("job-2", "https://shop.ir/b", second, num(120), "", "IRR"),
	}, members)
	if cell == nil {
		t.Fatal("no cell")
	}
	if *cell.Num != 90 {
		t.Errorf("value = %v, want 90", *cell.Num)
	}
	if cell.Previous == nil || *cell.Previous != 100 {
		t.Errorf("previous = %v, want the previous run's 100", cell.Previous)
	}
	if !cell.Changed() || cell.Delta() != -10 {
		t.Errorf("delta = %v, want -10", cell.Delta())
	}
	if cell.Runs != 2 || len(cell.History) != 2 {
		t.Errorf("runs = %d with %d history points, want 2 and 2", cell.Runs, len(cell.History))
	}
	if cell.MemberID != 7 {
		t.Errorf("member = %d, want 7 — the page the shown value came from", cell.MemberID)
	}
}

// One run only: there is nothing to compare with, so no change is claimed.
func TestBuildCellFirstRunHasNoChange(t *testing.T) {
	at := time.Now()
	cell := buildCell([]models.FieldObservation{
		obs("job-1", "https://shop.ir/a", at, num(433500000), "", "IRR"),
		obs("job-1", "https://shop.ir/b", at, num(287010000), "", "IRR"),
	}, []models.ProductGroupMember{{ID: 1, URL: "https://shop.ir/b"}})
	if cell.Previous != nil || cell.Changed() {
		t.Errorf("first run reported a change: previous = %v", cell.Previous)
	}
	if !cell.Disagrees() {
		t.Error("two pages at different prices should be flagged as disagreeing")
	}
}

func TestBuildCellTextFieldChange(t *testing.T) {
	first := time.Now().Add(-24 * time.Hour)
	cell := buildCell([]models.FieldObservation{
		obs("job-1", "https://shop.ir/a", first, nil, "out_of_stock", ""),
		obs("job-2", "https://shop.ir/a", time.Now(), nil, "in_stock", ""),
	}, []models.ProductGroupMember{{ID: 3, URL: "https://shop.ir/a"}})
	if cell.Text != "in_stock" || cell.PreviousText != "out_of_stock" {
		t.Fatalf("text = %q, previous = %q", cell.Text, cell.PreviousText)
	}
	if !cell.TextChanged() {
		t.Error("a text field that changed between runs should report it")
	}
}

func TestModalCurrency(t *testing.T) {
	at := time.Now()
	got := modalCurrency([]models.FieldObservation{
		obs("j", "u1", at, num(1), "", "USD"),
		obs("j", "u2", at, num(2), "", "IRR"),
		obs("j", "u3", at, num(3), "", "IRR"),
		obs("j", "u4", at, num(4), "", ""),
	})
	if got != "IRR" {
		t.Errorf("modal currency = %q, want IRR", got)
	}
	if modalCurrency(nil) != "" {
		t.Error("no observations should give no currency")
	}
}
