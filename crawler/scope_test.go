package crawler

import (
	"testing"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
)

func TestScopeDomainsSetting(t *testing.T) {
	cfg := &config.Config{}
	build := func(s *models.JobSettings) (*RunSettings, error) { return BuildRunSettings(cfg, s, nil, nil) }

	rs, err := build(&models.JobSettings{})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if rs.ScopeDomains != nil {
		t.Errorf("no scope_domains should leave the crawl on its own domain, got %v", rs.ScopeDomains)
	}

	// Hosts are reduced to their base domain, so a subdomain in the list
	// still matches links anywhere on that site.
	rs, err = build(&models.JobSettings{ScopeDomains: []string{"shop-b.ir", "https://www.shop-c.com/path"}})
	if err != nil {
		t.Fatalf("scope_domains: %v", err)
	}
	for _, want := range []string{"shop-b.ir", "shop-c.com"} {
		if !rs.ScopeDomains[want] {
			t.Errorf("scope domains %v missing %q", rs.ScopeDomains, want)
		}
	}

	if _, err := build(&models.JobSettings{ScopeDomains: []string{"   "}}); err == nil {
		t.Errorf("a blank scope domain should be rejected")
	}
}

// Every start URL's own domain is in scope: giving a crawl several start
// URLs is how one run covers several sites and can compare them.
func TestSeedDomainsAreInScope(t *testing.T) {
	cleaner := modules.NewURLCleaner()
	cleaner.Initialize()
	job := &models.CrawlJob{
		Domain: "shop-a.ir",
		SeedURLs: models.StringSlice{
			"https://shop-a.ir/category/2", // the job's own domain, not a second site
			"https://www.shop-b.com/phones",
			"https://shop-c.ir/p/1",
		},
	}
	r := &Run{job: job, urlCleaner: cleaner, rs: &RunSettings{}}
	r.seedDomains = seedDomainsOf(cleaner, job)

	for _, link := range []string{"https://shop-a.ir/p/9", "https://shop-b.com/p/3", "https://m.shop-c.ir/p/2"} {
		if !r.inScope(link) {
			t.Errorf("%s should be in scope", link)
		}
	}
	if r.inScope("https://unrelated.com/p/1") {
		t.Error("a site that is not a start URL should stay out of scope")
	}
	if r.seedDomains["shop-a.ir"] {
		t.Error("the job's own domain does not need to be repeated in seedDomains")
	}
}

func TestDomainBudget(t *testing.T) {
	cleaner := modules.NewURLCleaner()
	cleaner.Initialize()
	r := &Run{
		job:         &models.CrawlJob{Domain: "shop-a.ir"},
		urlCleaner:  cleaner,
		rs:          &RunSettings{MaxPagesPerDomain: 2},
		domainPages: map[string]int{},
		domainFull:  map[string]bool{},
	}
	for i, want := range []bool{false, false, true} {
		if got := r.domainBudgetSpent("https://shop-a.ir/p/" + string(rune('1'+i))); got != want {
			t.Errorf("page %d of shop-a.ir: spent = %v, want %v", i+1, got, want)
		}
	}
	// Another site has its own share.
	if r.domainBudgetSpent("https://shop-b.com/p/1") {
		t.Error("one site reaching its share must not stop the others")
	}
	// No cap configured = no limit.
	r.rs.MaxPagesPerDomain = 0
	if r.domainBudgetSpent("https://shop-a.ir/p/99") {
		t.Error("without max_pages_per_domain nothing is capped")
	}
}
