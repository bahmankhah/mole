package jobs

import (
	"testing"
	"time"

	"github.com/resolver/crawler/models"
)

func num(v float64) *float64 { return &v }

// obsFor is one recorded value of one listing in one run.
func obsFor(listingID uint, jobID string, at time.Time, value *float64, text, currency string) models.FieldObservation {
	return models.FieldObservation{
		ListingID: listingID, CrawlJobID: jobID, ObservedAt: at,
		NumValue: value, TextValue: text, Currency: currency,
	}
}

func listingAt(id uint, site, url string) models.Listing {
	return models.Listing{ID: id, SiteKey: site, URL: url, KeyMethod: models.ListingBySKU, Runs: 2}
}

// A shop with two offers of one product used to be folded into a single
// number, so the gap between two different offers was reported as a price
// change. Each offer now carries its own series, and "previous" is that
// offer's own previous run.
func TestBuildCellPreviousIsTheOffersOwnPreviousRun(t *testing.T) {
	first := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	second := first.Add(24 * time.Hour)
	listings := []models.Listing{
		listingAt(7, "shop.ir", "https://shop.ir/a"),
		listingAt(8, "shop.ir", "https://shop.ir/b"),
	}
	series := map[uint][]models.FieldObservation{
		7: {
			obsFor(7, "job-1", first, num(100), "", "IRR"),
			obsFor(7, "job-2", second, num(90), "", "IRR"),
		},
		8: {
			obsFor(8, "job-1", first, num(130), "", "IRR"),
			obsFor(8, "job-2", second, num(120), "", "IRR"),
		},
	}
	cell := buildCell(listings, series)
	if cell == nil {
		t.Fatal("no cell")
	}
	if cell.Num == nil || *cell.Num != 90 {
		t.Errorf("headline = %v, want the cheapest live offer 90", cell.Num)
	}
	if cell.ListingID != 7 {
		t.Errorf("headline listing = %d, want 7 — the offer the value came from", cell.ListingID)
	}
	if cell.Previous == nil || *cell.Previous != 100 {
		t.Errorf("previous = %v, want 100 — this offer's own previous run, not the other offer's 130", cell.Previous)
	}
	if !cell.Changed() || cell.Delta() != -10 {
		t.Errorf("delta = %v, want -10", cell.Delta())
	}
	if cell.Offers() != 2 {
		t.Errorf("offers = %d, want 2", cell.Offers())
	}
	if cell.Low == nil || cell.High == nil || *cell.Low != 90 || *cell.High != 120 {
		t.Errorf("spread = %v..%v, want 90..120", cell.Low, cell.High)
	}
	if !cell.Disagrees() {
		t.Error("two offers at different prices should report a spread")
	}
}

// Each offer keeps a whole series of its own, which is what makes a
// per-offer chart possible at all.
func TestBuildCellOffersKeepSeparateHistories(t *testing.T) {
	first := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	series := map[uint][]models.FieldObservation{
		7: {
			obsFor(7, "job-1", first, num(100), "", "IRR"),
			obsFor(7, "job-2", first.Add(24*time.Hour), num(90), "", "IRR"),
			obsFor(7, "job-3", first.Add(48*time.Hour), num(95), "", "IRR"),
		},
		8: {obsFor(8, "job-3", first.Add(48*time.Hour), num(120), "", "IRR")},
	}
	cell := buildCell([]models.Listing{
		listingAt(7, "shop.ir", "https://shop.ir/a"),
		listingAt(8, "shop.ir", "https://shop.ir/b"),
	}, series)
	byID := map[uint]CellListing{}
	for _, cl := range cell.Listings {
		byID[cl.ListingID] = cl
	}
	if got := len(byID[7].History); got != 3 {
		t.Errorf("offer 7 history = %d points, want 3", got)
	}
	if got := len(byID[8].History); got != 1 {
		t.Errorf("offer 8 history = %d points, want 1", got)
	}
	if byID[8].Previous != nil || byID[8].Changed() {
		t.Error("an offer seen once has nothing to compare with and must claim no change")
	}
	if p := byID[7].Previous; p == nil || *p != 90 {
		t.Errorf("offer 7 previous = %v, want 90", p)
	}
}

// One run only: there is nothing to compare with, so no change is claimed.
func TestBuildCellFirstRunHasNoChange(t *testing.T) {
	at := time.Now()
	cell := buildCell(
		[]models.Listing{listingAt(1, "shop.ir", "https://shop.ir/b")},
		map[uint][]models.FieldObservation{1: {obsFor(1, "job-1", at, num(287010000), "", "IRR")}},
	)
	if cell.Previous != nil || cell.Changed() {
		t.Errorf("first run reported a change: previous = %v", cell.Previous)
	}
	if cell.Disagrees() {
		t.Error("a single offer cannot disagree with itself")
	}
}

func TestBuildCellTextFieldChange(t *testing.T) {
	first := time.Now().Add(-24 * time.Hour)
	cell := buildCell(
		[]models.Listing{listingAt(3, "shop.ir", "https://shop.ir/a")},
		map[uint][]models.FieldObservation{3: {
			obsFor(3, "job-1", first, nil, "out_of_stock", ""),
			obsFor(3, "job-2", time.Now(), nil, "in_stock", ""),
		}},
	)
	if cell.Text != "in_stock" || cell.PreviousText != "out_of_stock" {
		t.Fatalf("text = %q, previous = %q", cell.Text, cell.PreviousText)
	}
	if !cell.TextChanged() {
		t.Error("a text field that changed between runs should report it")
	}
}

// A delisted offer still shows what it last cost, marked so it is not read
// as a live price.
func TestBuildCellEveryOfferGoneIsStale(t *testing.T) {
	at := time.Now()
	gone := listingAt(5, "shop.ir", "https://shop.ir/a")
	gone.Stale = true
	cell := buildCell(
		[]models.Listing{gone},
		map[uint][]models.FieldObservation{5: {obsFor(5, "job-1", at, num(500), "", "IRR")}},
	)
	if !cell.Stale {
		t.Error("a cell whose every offer is gone should be stale")
	}
	if cell.Num == nil || *cell.Num != 500 {
		t.Errorf("value = %v, want the last known 500", cell.Num)
	}
}

// A live offer outranks a delisted one even when the delisted one is cheaper:
// a price you can no longer pay is not the cheapest offer.
func TestBuildCellPrefersLiveOfferOverCheaperStaleOne(t *testing.T) {
	at := time.Now()
	stale := listingAt(9, "shop.ir", "https://shop.ir/old")
	stale.Stale = true
	cell := buildCell(
		[]models.Listing{stale, listingAt(10, "shop.ir", "https://shop.ir/new")},
		map[uint][]models.FieldObservation{
			9:  {obsFor(9, "job-1", at, num(100), "", "IRR")},
			10: {obsFor(10, "job-1", at, num(140), "", "IRR")},
		},
	)
	if cell.Stale {
		t.Error("the shop still has a live offer, so the cell is not stale")
	}
	if cell.ListingID != 10 || cell.Num == nil || *cell.Num != 140 {
		t.Errorf("headline = offer %d at %v, want offer 10 at 140", cell.ListingID, cell.Num)
	}
}

// A shop quoting a stray other currency is not thereby the cheapest.
func TestPromoteHeadlineIgnoresForeignCurrency(t *testing.T) {
	at := time.Now()
	cell := buildCell(
		[]models.Listing{
			listingAt(1, "shop.ir", "https://shop.ir/a"),
			listingAt(2, "shop.ir", "https://shop.ir/b"),
			listingAt(3, "shop.ir", "https://shop.ir/c"),
		},
		map[uint][]models.FieldObservation{
			1: {obsFor(1, "job-1", at, num(400), "", "IRR")},
			2: {obsFor(2, "job-1", at, num(500), "", "IRR")},
			3: {obsFor(3, "job-1", at, num(9), "", "USD")},
		},
	)
	if cell.Currency != "IRR" {
		t.Errorf("currency = %q, want IRR (what most offers quote)", cell.Currency)
	}
	if cell.Num == nil || *cell.Num != 400 {
		t.Errorf("headline = %v, want 400; 9 USD is not comparable with rials", cell.Num)
	}
}

func TestModalCurrency(t *testing.T) {
	if got := modalCurrency([]string{"USD", "IRR", "IRR", ""}); got != "IRR" {
		t.Errorf("modal currency = %q, want IRR", got)
	}
	if modalCurrency(nil) != "" {
		t.Error("no currencies should give no currency")
	}
	// A tie breaks alphabetically, so the choice does not flap between runs.
	if got := modalCurrency([]string{"USD", "EUR"}); got != "EUR" {
		t.Errorf("tie = %q, want EUR", got)
	}
}
