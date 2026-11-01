package crawler

import (
	"testing"

	"github.com/resolver/crawler/models"
)

// A crawl given start URLs beside its target used to queue only the start
// URLs, so the site the job is named after was never fetched — a two-site
// comparison came back with one site in it.
func TestStartURLsKeepTheTargetSite(t *testing.T) {
	job := &models.CrawlJob{
		TargetURL: "https://www.digikala.com/search/category-mobile-phone/",
		Domain:    "digikala.com",
		SeedURLs:  models.StringSlice{"https://torob.com/browse/94/mobile/"},
	}
	got := startURLsOf(job)
	want := []string{
		"https://www.digikala.com/search/category-mobile-phone/",
		"https://torob.com/browse/94/mobile/",
	}
	if len(got) != len(want) {
		t.Fatalf("start URLs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("start URL %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A job whose target is also listed as a start URL must not fetch it twice.
func TestStartURLsDoNotRepeatTheTarget(t *testing.T) {
	job := &models.CrawlJob{
		TargetURL: "https://shop-a.ir/",
		Domain:    "shop-a.ir",
		SeedURLs:  models.StringSlice{"https://shop-a.ir/", "https://shop-b.com/"},
	}
	if got := startURLsOf(job); len(got) != 2 {
		t.Errorf("start URLs = %v, want the target listed once", got)
	}
}

// A job with no start URLs still crawls its target.
func TestStartURLsFallBackToTarget(t *testing.T) {
	job := &models.CrawlJob{TargetURL: "https://shop-a.ir/", Domain: "shop-a.ir"}
	got := startURLsOf(job)
	if len(got) != 1 || got[0] != "https://shop-a.ir/" {
		t.Errorf("start URLs = %v, want just the target", got)
	}
}
