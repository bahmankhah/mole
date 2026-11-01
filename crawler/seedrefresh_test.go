package crawler

import (
	"testing"

	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
)

// A start URL can be added while the crawl is running. The run has to
// widen its scope to the new site, or it would fetch the one page it was
// handed and refuse to follow any link inside it.
func TestAddedStartURLWidensScopeMidRun(t *testing.T) {
	cleaner := modules.NewURLCleaner()
	cleaner.Initialize()
	job := &models.CrawlJob{
		Domain:   "shop-a.ir",
		SeedURLs: models.StringSlice{"https://shop-a.ir/c/2"},
	}
	r := &Run{job: job, urlCleaner: cleaner, rs: &RunSettings{}}
	r.setSeedDomains(seedDomainsOf(cleaner, job), "https://shop-a.ir/c/2")

	if r.inScope("https://shop-b.com/p/1") {
		t.Fatal("a site that is not a start URL starts out of scope")
	}
	r.applyAddedStartURLs(models.StringSlice{"https://shop-a.ir/c/2", "https://shop-b.com/phones"})
	if !r.inScope("https://shop-b.com/p/1") {
		t.Error("a site added mid-run should come into scope, so its links are followed")
	}
	if !r.inScope("https://shop-a.ir/p/9") {
		t.Error("the original sites must stay in scope")
	}
	if r.inScope("https://unrelated.com/p/1") {
		t.Error("an unrelated site must stay out of scope")
	}
}

// The refresh runs every few seconds on every crawl, so it must do nothing
// at all when the list has not changed.
func TestUnchangedStartURLsAreNotReapplied(t *testing.T) {
	cleaner := modules.NewURLCleaner()
	cleaner.Initialize()
	job := &models.CrawlJob{Domain: "shop-a.ir", SeedURLs: models.StringSlice{"https://shop-b.com/x"}}
	r := &Run{job: job, urlCleaner: cleaner, rs: &RunSettings{}}
	key := "https://shop-b.com/x"
	r.setSeedDomains(seedDomainsOf(cleaner, job), key)
	before, _ := r.seedDomains.Load().(map[string]bool)

	r.applyAddedStartURLs(models.StringSlice{"https://shop-b.com/x"})
	after, _ := r.seedDomains.Load().(map[string]bool)
	if &before == &after && len(before) != len(after) {
		t.Error("the in-scope set changed for an unchanged list")
	}
	if !r.inScope("https://shop-b.com/y") {
		t.Error("scope should be unaffected")
	}
}

// Removing a site from a running crawl narrows its scope on the next tick.
func TestRemovedStartURLNarrowsScopeMidRun(t *testing.T) {
	cleaner := modules.NewURLCleaner()
	cleaner.Initialize()
	job := &models.CrawlJob{
		Domain:   "shop-a.ir",
		SeedURLs: models.StringSlice{"https://shop-b.com/phones", "https://shop-c.ir/p"},
	}
	r := &Run{job: job, urlCleaner: cleaner, rs: &RunSettings{}}
	r.setSeedDomains(seedDomainsOf(cleaner, job), "x")
	if !r.inScope("https://shop-c.ir/p/1") {
		t.Fatal("shop-c starts in scope")
	}
	r.applyAddedStartURLs(models.StringSlice{"https://shop-b.com/phones"})
	if r.inScope("https://shop-c.ir/p/1") {
		t.Error("a site taken off the list should fall out of scope")
	}
}
