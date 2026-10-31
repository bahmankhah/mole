package jobs

import (
	"testing"

	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
)

func TestNameTokensDropsStopWordsAndBrand(t *testing.T) {
	got := nameTokens("Samsung Galaxy S23 Ultra اصل گارانتی", "Samsung", nil)
	want := map[string]bool{"galaxy": true, "s23": true, "ultra": true}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	for _, tok := range got {
		if !want[tok] {
			t.Errorf("unexpected token %q in %v", tok, got)
		}
	}
}

func TestNameTokensExtraStopWords(t *testing.T) {
	got := nameTokens("Apple iPhone 15 Pro Max Combo", "Apple", []string{"combo"})
	for _, tok := range got {
		if tok == "combo" {
			t.Errorf("extra stop word %q was not dropped: %v", tok, got)
		}
	}
}

func TestJaccard(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"b", "c", "d"}
	if got := jaccard(a, b); got < 0.49 || got > 0.51 {
		t.Errorf("jaccard = %v, want 0.5", got)
	}
	if jaccard(nil, b) != 0 {
		t.Errorf("jaccard with empty set should be 0")
	}
	if jaccard(a, a) != 1 {
		t.Errorf("jaccard of identical sets should be 1")
	}
}

func TestCandidateExactKey(t *testing.T) {
	c := candidate{gtin: "1234567890123"}
	key, method := c.exactKey()
	if key != "gtin:1234567890123" || method != "gtin" {
		t.Errorf("gtin key = %q %q", key, method)
	}
	c = candidate{brand: "Samsung", mpn: "SM-S918"}
	key, method = c.exactKey()
	if key != "mpn:samsung|sm-s918" || method != "mpn" {
		t.Errorf("mpn key = %q %q", key, method)
	}
	c = candidate{name: "no identity here"}
	if key, _ := c.exactKey(); key != "" {
		t.Errorf("expected no exact key, got %q", key)
	}
}

func TestMatchSetUnionPrefersExistingRoot(t *testing.T) {
	ms := newMatchSet(3)
	ms.union(rowNode(0), rowNode(1))
	ms.union(rowNode(1), existingNode("pg-1"))
	ms.union(rowNode(2), rowNode(0))
	root := ms.find(rowNode(2))
	if root != existingNode("pg-1") {
		t.Errorf("component root = %q, want existing:pg-1", root)
	}
}

func TestOverrideKeyRoundTrip(t *testing.T) {
	k1 := overrideKey("site-a", "https://x.com/p/1", "")
	k2 := overrideKey("site-a", "https://x.com/p/1", "")
	k3 := overrideKey("site-b", "https://x.com/p/1", "")
	if k1 != k2 {
		t.Errorf("same inputs produced different keys")
	}
	if k1 == k3 {
		t.Errorf("different sites produced the same key")
	}
}

func TestScopeForJob(t *testing.T) {
	on, off := true, false
	scheduleID := "s1"

	// A crawl is grouped only when it asks to be.
	job := &models.CrawlJob{ID: "j1"}
	if _, _, ok := ScopeForJob(job); ok {
		t.Errorf("a plain crawl with no settings should not be grouped")
	}
	job = &models.CrawlJob{ID: "j1", Settings: &models.JobSettings{GroupProducts: &off}}
	if _, _, ok := ScopeForJob(job); ok {
		t.Errorf("group_products=false should not be grouped")
	}
	// A one-off crawl is scoped to itself.
	job = &models.CrawlJob{ID: "j1", Settings: &models.JobSettings{GroupProducts: &on}}
	if st, id, ok := ScopeForJob(job); !ok || st != models.ScopeCrawl || id != "j1" {
		t.Errorf("crawl scope = %q %q %v", st, id, ok)
	}
	// A scheduled crawl is scoped to the schedule, so history survives reruns.
	job = &models.CrawlJob{ID: "j1", ScheduleID: &scheduleID, Settings: &models.JobSettings{GroupProducts: &on}}
	if st, id, ok := ScopeForJob(job); !ok || st != models.ScopeSchedule || id != scheduleID {
		t.Errorf("schedule scope = %q %q %v", st, id, ok)
	}
}

func TestPlanPrimaryField(t *testing.T) {
	cases := []struct {
		fields []string
		want   string
	}{
		{[]string{"price", "availability"}, "price"},
		{[]string{"availability", "price"}, "price"}, // price wins wherever it sits
		{[]string{"rating", "review_count"}, "rating"},
		{nil, "price"},
	}
	for _, c := range cases {
		if got := (groupPlan{compareFields: c.fields}).primaryField(); got != c.want {
			t.Errorf("primaryField(%v) = %q, want %q", c.fields, got, c.want)
		}
	}
}

func TestCompareValuesPrefersVariant(t *testing.T) {
	p := &content.Product{
		Name:         "Phone",
		Price:        &modules.Price{Amount: 100, Currency: "USD"},
		Availability: "out_of_stock",
		Rating:       4.5,
	}
	v := &content.ProductVariant{
		Name:         "256GB",
		Price:        &modules.Price{Amount: 150, Currency: "USD"},
		Availability: "in_stock",
	}
	got := compareValues(p, v, nil, []string{"price", "availability", "rating"})
	if got["price"].Num == nil || *got["price"].Num != 150 {
		t.Errorf("variant price should win: %+v", got["price"])
	}
	if got["availability"].Text != "in_stock" {
		t.Errorf("variant availability should win: %+v", got["availability"])
	}
	if got["rating"].Num == nil || *got["rating"].Num != 4.5 {
		t.Errorf("product rating should carry through: %+v", got["rating"])
	}
}

func TestCompareValuesFallsBackToExtracted(t *testing.T) {
	p := &content.Product{Name: "Phone"}
	extracted := []byte(`{"warranty":"18 months","stock_count":"1,250"}`)
	got := compareValues(p, nil, extracted, []string{"warranty", "stock_count", "price"})
	if got["warranty"].Text != "18 months" {
		t.Errorf("extract field text = %+v", got["warranty"])
	}
	if got["stock_count"].Num == nil || *got["stock_count"].Num != 1250 {
		t.Errorf("numeric extract field = %+v", got["stock_count"])
	}
	if _, ok := got["price"]; ok {
		t.Errorf("a product with no price should contribute no price value")
	}
}

func TestScopedCanonicalKeySeparatesVariants(t *testing.T) {
	base := scopedCanonicalKey("gtin:123", "")
	v128 := scopedCanonicalKey("gtin:123", "storage:128gb")
	v256 := scopedCanonicalKey("gtin:123", "storage:256gb")
	if base == v128 || v128 == v256 {
		t.Errorf("variants must not share a canonical key: %q %q %q", base, v128, v256)
	}
}

func TestStrongestKeyPrefersGTIN(t *testing.T) {
	rows := []candidate{
		{name: "no identity"},
		{brand: "Samsung", mpn: "SM-A075"},
		{gtin: "8806095000000"},
	}
	// Whichever row happens to come first, the group must be keyed by the
	// barcode: a weaker key would make the next run create a second group
	// for the same product.
	if got := strongestKey(rows, []int{0, 1, 2}); got != "gtin:8806095000000" {
		t.Errorf("key = %q, want the GTIN", got)
	}
	if got := strongestKey(rows, []int{0, 1}); got != "mpn:samsung|sm-a075" {
		t.Errorf("key without a GTIN = %q, want the brand+MPN", got)
	}
	if got := strongestKey(rows, []int{0}); got != "" {
		t.Errorf("key with no identity = %q, want empty", got)
	}
}

func TestSplitTargets(t *testing.T) {
	got := SplitTargets("  https://shop-a.ir/c \n https://shop-b.com/x?a=1,2 \n\n example.com  ")
	want := []string{"https://shop-a.ir/c", "https://shop-b.com/x?a=1,2", "example.com"}
	if len(got) != len(want) {
		t.Fatalf("split = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("url %d = %q, want %q", i, got[i], want[i])
		}
	}
	if len(SplitTargets("   \n  ")) != 0 {
		t.Error("blank input should give no URLs")
	}
}
