package jobs

import (
	"strings"
	"testing"

	"github.com/resolver/crawler/models"
)

func TestNormalizeURLListSplitsAndDeduplicates(t *testing.T) {
	// One entry may hold several URLs, so pasting a block behaves the same
	// as passing them one at a time.
	got, err := normalizeURLList([]string{"shop-a.ir\nhttps://shop-b.ir/phones", "  shop-a.ir  "}, models.JobKindCrawl)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d URLs %v, want 2 after de-duplication", len(got), got)
	}
	for _, u := range got {
		if !strings.HasPrefix(u, "http") {
			t.Errorf("%q was not normalized to an absolute URL", u)
		}
	}
}

func TestNormalizeURLListRejectsWhatCannotBeCrawled(t *testing.T) {
	for _, bad := range []string{"ftp://files.example.com", "file:///etc/passwd"} {
		if _, err := normalizeURLList([]string{bad}, models.JobKindCrawl); err == nil {
			t.Errorf("%q should be refused: only http and https can be crawled", bad)
		}
	}
	// Nothing to add is not an error here; the caller reports it, so that
	// "urls: []" and "urls: [\" \"]" behave the same way.
	got, err := normalizeURLList([]string{"   ", ""}, models.JobKindCrawl)
	if err != nil || len(got) != 0 {
		t.Errorf("blank input = %v, %v; want an empty list and no error", got, err)
	}
}

// A bare host is accepted and given a scheme, and a host with no dot is
// allowed on purpose: an intranet name like "wiki" is a real target, so
// requiring a dot would refuse it.
func TestNormalizeURLListAcceptsBareHosts(t *testing.T) {
	got, err := normalizeURLList([]string{"shop-a.ir", "wiki"}, models.JobKindCrawl)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want both accepted", got)
	}
	for _, u := range got {
		if !strings.HasPrefix(u, "https://") {
			t.Errorf("%q should have been given a scheme", u)
		}
	}
}

func TestJoinAndSplitListRoundTrip(t *testing.T) {
	// A job records a target plus seeds; callers see one ordered list. The
	// first URL must stay the target through a round trip, or the crawl's
	// own domain would change under it.
	all := joinList("https://a.ir", []string{"https://b.ir", "https://c.ir"})
	target, seeds := splitList(all)
	if target != "https://a.ir" {
		t.Errorf("target = %q, want the first URL", target)
	}
	if len(seeds) != 2 {
		t.Errorf("seeds = %v, want the remaining two", seeds)
	}
	if got := joinList("", nil); len(got) != 0 {
		t.Errorf("an empty list should stay empty, got %v", got)
	}
	// Blank entries are dropped rather than becoming an empty start URL.
	if got := joinList("https://a.ir", []string{"", "  "}); len(got) != 1 {
		t.Errorf("blank seeds should be dropped, got %v", got)
	}
}

func TestMergeURLsReportsAddedAndSkipped(t *testing.T) {
	have := []string{"https://a.ir", "https://b.ir"}
	all, added, skipped := mergeURLs(have, []string{"https://b.ir", "https://c.ir"})
	if len(all) != 3 || all[0] != "https://a.ir" {
		t.Errorf("all = %v, want the original order kept with c appended", all)
	}
	if len(added) != 1 || added[0] != "https://c.ir" {
		t.Errorf("added = %v, want just c", added)
	}
	if len(skipped) != 1 || skipped[0] != "https://b.ir" {
		t.Errorf("skipped = %v, want just b", skipped)
	}
}

// Adding a URL already on the list must not reorder it: the first start
// URL is the target, and the crawl's own domain follows from it.
func TestMergeURLsKeepsTheTargetFirst(t *testing.T) {
	all, added, _ := mergeURLs([]string{"https://a.ir", "https://b.ir"}, []string{"https://a.ir"})
	if all[0] != "https://a.ir" || len(all) != 2 {
		t.Errorf("all = %v, want the list unchanged", all)
	}
	if len(added) != 0 {
		t.Errorf("added = %v, want nothing", added)
	}
}

// A resuming crawl does not re-add its start URLs — it already has a queue
// — so a URL added while it was between workers has to go into that queue
// directly. Only a crawl that has never run can be left to seed itself,
// and putting a URL in its queue early would be actively harmful: the run
// seeds only when it finds the queue empty, so it would skip seeding and
// never fetch the original target.
func TestQueuedEffectTellsTheTruthForEachState(t *testing.T) {
	cases := []struct {
		name            string
		status          models.JobStatus
		added, queued   int
		wantContains    string
		wantNotContains string
	}{
		{"running", models.JobStatusRunning, 1, 1, "within a few seconds", ""},
		{"paused", models.JobStatusPaused, 1, 1, "when you resume", ""},
		{"queued between workers", models.JobStatusQueued, 1, 1, "picks up again", "when this crawl starts"},
		{"already listed but re-queued", models.JobStatusRunning, 0, 1, "re-queued", ""},
		{"nothing to do", models.JobStatusRunning, 0, 0, "nothing to do", ""},
	}
	for _, c := range cases {
		got := queuedEffect(c.status, c.added, c.queued)
		if !strings.Contains(got, c.wantContains) {
			t.Errorf("%s: %q does not mention %q", c.name, got, c.wantContains)
		}
		if c.wantNotContains != "" && strings.Contains(got, c.wantNotContains) {
			t.Errorf("%s: %q wrongly claims %q", c.name, got, c.wantNotContains)
		}
	}
}
