package jobs

import (
	"fmt"
	"strings"

	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
)

// A crawl or a schedule covers one site per start URL, and which sites
// those are is the thing most likely to change after it exists: a new
// competitor appears, a shop is dropped. Replacing the whole definition to
// add one URL is both awkward and risky, so start URLs are editable on
// their own.
//
// The first start URL is the target and the rest are seeds. That split is
// an implementation detail of how a job records them — these functions
// present one ordered list and keep the split consistent underneath.

// URLChange reports what changing a start-URL list did and, because the
// answer differs by state, when it takes effect.
type URLChange struct {
	URLs    []string `json:"urls"`              // the whole list afterwards, target first
	Added   []string `json:"added,omitempty"`   // newly accepted
	Removed []string `json:"removed,omitempty"` // taken off the list
	Skipped []string `json:"skipped,omitempty"` // already on the list, left alone
	Queued  int      `json:"queued,omitempty"`  // pushed straight into a running crawl's queue
	Effect  string   `json:"effect"`            // what this means, in words, for the UI and the CLI
}

// countOf renders "1 site" / "3 sites". The cron package's plural() is a
// different thing — it writes "Every 3 hours" — and reads as nonsense here.
func countOf(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// maxStartURLs caps a crawl's start URLs. Each one is a separate site the
// run must keep in scope, and the comparison table grows a column for it.
const maxStartURLs = 200

// normalizeURLList splits, validates and de-duplicates user-supplied URLs.
// Each entry may itself hold several whitespace-separated URLs, so pasting
// a block works the same as passing them one at a time.
func normalizeURLList(raw []string, kind string) ([]string, error) {
	var out []string
	for _, entry := range raw {
		for _, u := range SplitTargets(entry) {
			var err error
			if kind == models.JobKindBatch {
				if u, err = batchURL(u); err != nil {
					return nil, err
				}
			} else if u, _, err = ParseTarget(u); err != nil {
				return nil, err
			}
			out = append(out, u)
		}
	}
	return uniqueStrings(out), nil
}

// splitList turns an ordered list back into the target-plus-seeds shape a
// job and a template record.
func splitList(urls []string) (target string, seeds []string) {
	if len(urls) == 0 {
		return "", nil
	}
	return urls[0], urls[1:]
}

// joinList is the inverse: the ordered list a caller sees.
func joinList(target string, seeds []string) []string {
	out := make([]string, 0, len(seeds)+1)
	if strings.TrimSpace(target) != "" {
		out = append(out, target)
	}
	for _, s := range seeds {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return uniqueStrings(out)
}

// mergeURLs adds want to have, reporting which were new and which were
// already there. Order is preserved: the target stays the target.
func mergeURLs(have, want []string) (all, added, skipped []string) {
	seen := make(map[string]bool, len(have))
	for _, u := range have {
		seen[u] = true
	}
	all = append(all, have...)
	for _, u := range want {
		if seen[u] {
			skipped = append(skipped, u)
			continue
		}
		seen[u] = true
		all = append(all, u)
		added = append(added, u)
	}
	return all, added, skipped
}

// ── schedules ──────────────────────────────────────────────────────────

// ScheduleURLs lists a schedule's start URLs, target first.
func (m *Manager) ScheduleURLs(id string) ([]string, error) {
	s, err := m.GetSchedule(id)
	if err != nil {
		return nil, err
	}
	return joinList(s.Template.TargetURL, s.Template.SeedURLs), nil
}

// AddScheduleURLs adds start URLs to a schedule. They are crawled from its
// next run; the run in progress, if any, is left alone — changing what a
// run covers halfway through would make its results mean two things.
func (m *Manager) AddScheduleURLs(id string, urls []string) (*URLChange, error) {
	s, err := m.GetSchedule(id)
	if err != nil {
		return nil, err
	}
	want, err := normalizeURLList(urls, s.Template.Kind)
	if err != nil {
		return nil, err
	}
	if len(want) == 0 {
		return nil, invalidInput("no URL given")
	}
	have := joinList(s.Template.TargetURL, s.Template.SeedURLs)
	all, added, skipped := mergeURLs(have, want)
	if err := m.checkURLCount(all, s.Template.Kind); err != nil {
		return nil, err
	}
	if len(added) > 0 {
		if err := m.saveScheduleURLs(s, all); err != nil {
			return nil, err
		}
	}
	return &URLChange{
		URLs: all, Added: added, Skipped: skipped,
		Effect: scheduleEffect(len(added), s.Enabled),
	}, nil
}

// RemoveScheduleURLs takes start URLs off a schedule. What has already been
// crawled from them is kept — their pages, and the price history of their
// listings, stay readable; they simply stop being visited.
func (m *Manager) RemoveScheduleURLs(id string, urls []string) (*URLChange, error) {
	s, err := m.GetSchedule(id)
	if err != nil {
		return nil, err
	}
	// Removal matches on the normalized form, so a URL typed back slightly
	// differently still finds the one on the list.
	want, err := normalizeURLList(urls, s.Template.Kind)
	if err != nil {
		return nil, err
	}
	drop := make(map[string]bool, len(want))
	for _, u := range want {
		drop[u] = true
	}
	have := joinList(s.Template.TargetURL, s.Template.SeedURLs)
	var all, removed []string
	for _, u := range have {
		if drop[u] {
			removed = append(removed, u)
			continue
		}
		all = append(all, u)
	}
	if len(all) == 0 {
		return nil, invalidInput("a schedule needs at least one start URL; add another before removing the last")
	}
	if len(removed) > 0 {
		if err := m.saveScheduleURLs(s, all); err != nil {
			return nil, err
		}
	}
	effect := "nothing matched those URLs"
	if len(removed) > 0 {
		effect = countOf(len(removed), "site") +
			" removed — no longer crawled from the next run. What has already been recorded is kept."
	}
	return &URLChange{URLs: all, Removed: removed, Effect: effect}, nil
}

func (m *Manager) saveScheduleURLs(s *models.Schedule, all []string) error {
	target, seeds := splitList(all)
	s.Template.TargetURL, s.Template.SeedURLs = target, seeds
	return m.db.Model(&models.Schedule{}).Where("id = ?", s.ID).
		Update("template", s.Template).Error
}

// The wording avoids a pronoun, so one site and several read the same way.
func scheduleEffect(added int, enabled bool) string {
	switch {
	case added == 0:
		return "already on the list"
	case !enabled:
		return countOf(added, "site") + " added — crawled when you resume this schedule."
	}
	return countOf(added, "site") + " added — crawled from the next run."
}

// ── crawls ─────────────────────────────────────────────────────────────

// JobURLs lists a crawl's start URLs, target first.
func (m *Manager) JobURLs(id string) ([]string, error) {
	job, err := m.GetJob(id)
	if err != nil {
		return nil, err
	}
	return joinList(job.TargetURL, job.SeedURLs), nil
}

// AddJobURLs adds start URLs to a crawl.
//
// Where the URL has to go depends on whether the crawl has ever run, not
// on whether it is running now. A run that is resuming does not re-add its
// start URLs — it already has a queue, and re-seeding would re-crawl the
// site from scratch — so for any crawl that has started, a new URL has to
// be put in the queue directly. The queue is a database table, so this
// reaches a run on another worker, and the run widens its scope to the new
// site within a few seconds of noticing the list changed.
//
// Only a crawl that has never started can be left to seed itself. Doing it
// the other way for one of those would be worse than useless: the run adds
// its start URLs only when it finds the queue empty, so putting one there
// early would make it skip seeding and never fetch the original target.
//
// A crawl that has finished cannot be extended: its results describe the
// sites it covered, and quietly adding a site it never visited would make
// them describe nothing in particular. Duplicate it, or put it on a
// schedule, instead.
func (m *Manager) AddJobURLs(id string, urls []string) (*URLChange, error) {
	job, err := m.GetJob(id)
	if err != nil {
		return nil, err
	}
	switch job.Status {
	case models.JobStatusCompleted, models.JobStatusFailed, models.JobStatusCancelled:
		return nil, invalidState(
			"this crawl has already %s, so there is nothing left to crawl the URL with — "+
				"duplicate it to run again with the extra site, or put it on a schedule", job.Status)
	}
	want, err := normalizeURLList(urls, job.Kind)
	if err != nil {
		return nil, err
	}
	if len(want) == 0 {
		return nil, invalidInput("no URL given")
	}
	have := joinList(job.TargetURL, job.SeedURLs)
	all, added, skipped := mergeURLs(have, want)
	if err := m.checkURLCount(all, job.Kind); err != nil {
		return nil, err
	}
	change := &URLChange{URLs: all, Added: added, Skipped: skipped}
	if len(added) > 0 {
		// Record the list first. A running crawl re-reads it to widen its
		// scope, so if the queueing below fails the sites are still
		// reachable on the next pass rather than lost.
		target, seeds := splitList(all)
		if err := m.db.Model(&models.CrawlJob{}).Where("id = ?", job.ID).
			Updates(map[string]interface{}{"target_url": target, "seed_urls": models.StringSlice(seeds)}).Error; err != nil {
			return nil, err
		}
	}
	if job.StartedAt == nil {
		if len(added) == 0 {
			change.Effect = "already on the list"
			return change, nil
		}
		change.Effect = countOf(len(added), "site") + " added — crawled when this crawl starts."
		return change, nil
	}
	// Every URL asked for is queued, not only the ones new to the list: a
	// URL can be on the list and missing from the queue — it was added
	// while the crawl was between workers, say — and pressing "add" again
	// is then the obvious way to put that right.
	queued, err := m.queueIntoFrontier(job, want)
	if err != nil {
		return nil, err
	}
	change.Queued = queued
	change.Effect = queuedEffect(job.Status, len(added), queued)
	return change, nil
}

// queuedEffect says what putting URLs in a started crawl's queue means,
// which depends on whether the crawl is working right now.
func queuedEffect(status models.JobStatus, added, queued int) string {
	switch {
	case queued == 0 && added == 0:
		return "already on the list and already queued — nothing to do."
	case queued == 0:
		return countOf(added, "site") + " added, but nothing could be queued — the pages may already have been crawled."
	}
	what := countOf(queued, "site")
	if added == 0 {
		what += " re-queued (already on the list)"
	} else {
		what += " added and queued"
	}
	switch status {
	case models.JobStatusRunning:
		return what + " — the running crawl starts fetching within a few seconds."
	case models.JobStatusPaused:
		return what + " — fetched when you resume this crawl."
	}
	return what + " — fetched when this crawl picks up again."
}

// queueIntoFrontier pushes URLs into a crawl's queue. The queue is a
// database table keyed by crawl, so this works whichever worker owns the
// run — including one on another machine.
//
// It counts rows that are actually in the queue afterwards rather than
// calls that returned without an error: AddSeedURLs reports a failure by
// logging it and returning nil, so trusting its return value would let a
// URL be reported as queued when it never arrived.
func (m *Manager) queueIntoFrontier(job *models.CrawlJob, urls []string) (int, error) {
	cleaner := modules.NewURLCleaner()
	if err := cleaner.Initialize(); err != nil {
		return 0, err
	}
	frontier := modules.NewDBFrontier(m.db, cleaner, m.config.Crawler.TeleportProbability)
	frontier.SetCrawlJob(job.ID)
	if job.Settings != nil {
		frontier.SetURLFilters(job.Settings.URLIncludePatterns, job.Settings.URLExcludePatterns)
	}
	frontier.SetSkipExtensions(m.config.Crawler.SkipExtensions)
	queued := 0
	for _, u := range urls {
		if job.Kind == models.JobKindBatch {
			// A batch fetches its URLs only — no robots.txt or sitemap
			// discovery, exactly as at the start of a run.
			_ = frontier.AddSeedRequest(u, "", "")
		} else {
			_ = frontier.AddSeedURLs(u)
		}
		if m.queuedOrCrawled(job.ID, cleaner, u) {
			queued++
		}
	}
	return queued, nil
}

// queuedOrCrawled reports whether a URL is now in the crawl's queue, or was
// already fetched by it — either way the crawl has it, and neither counts
// as the add having failed.
func (m *Manager) queuedOrCrawled(jobID string, cleaner *modules.URLCleaner, rawURL string) bool {
	cleaned, err := cleaner.ProcessURL(rawURL)
	if err != nil {
		cleaned = rawURL
	}
	hash := modules.RequestIdentityHash(cleaned, "", "")
	var n int64
	m.db.Model(&models.FrontierURL{}).Where("crawl_job_id = ? AND url_hash = ?", jobID, hash).Count(&n)
	if n > 0 {
		return true
	}
	m.db.Model(&models.CrawledPage{}).Where("crawl_job_id = ? AND url_hash = ?", jobID, hash).Count(&n)
	return n > 0
}

func (m *Manager) checkURLCount(all []string, kind string) error {
	limit, what := maxStartURLs, "start URLs"
	if kind == models.JobKindBatch {
		limit, what = MaxBatchURLs, "URLs"
	}
	if len(all) > limit {
		return invalidInput("that would make %d %s; the limit is %d", len(all), what, limit)
	}
	return nil
}
