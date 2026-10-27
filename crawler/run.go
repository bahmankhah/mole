package crawler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"gorm.io/gorm"
)

// Run is one job executing on this worker.
type Run struct {
	e  *Engine
	rs *RunSettings

	jobMu sync.Mutex
	job   *models.CrawlJob

	ctx      context.Context
	cancel   context.CancelFunc
	sideCtx  context.Context // for after-crawl plugins and embeddings, outlives ctx briefly
	sideStop context.CancelFunc
	wg       sync.WaitGroup
	sideWG   sync.WaitGroup
	finished chan struct{}

	stopMu      sync.Mutex
	stopOutcome *Outcome

	startedAt time.Time

	urlCleaner     *modules.URLCleaner
	linkExtractor  *modules.HTMLLinkExtractor
	sitemapParser  *modules.SitemapParser
	robotsParser   *modules.RobotsParser
	phraseDetector *modules.SimplePhraseDetector
	frontier       *modules.DBFrontier
	http           *modules.HTTPFetcher
	phraseIDMap    map[string]uint

	robotRules   map[string]*modules.RobotsResult
	robotRulesMu sync.RWMutex

	baseJobID string

	crawled  int64
	matches  int64
	inflight int64

	// usage counters (absolute values, seeded from the job row)
	uRequests, uPages, uBytes, uBrowserMs, uLLMTokens, uErrors, uScreenshots, uProxy int64

	changesNew, changesChanged int64

	pluginSlots    chan struct{} // bounds concurrent after-crawl processes
	pluginRuns     int64
	pluginFailures int64
	pluginErrMu    sync.Mutex
	pluginLastErr  string

	rngMu sync.Mutex
	rng   *rand.Rand
}

func newRun(e *Engine, job *models.CrawlJob) (*Run, error) {
	rs, err := BuildRunSettings(e.cfg, job.Settings, job.Limits, e.proxies)
	if err != nil {
		return nil, err
	}
	r := &Run{
		e:           e,
		rs:          rs,
		job:         job,
		finished:    make(chan struct{}),
		phraseIDMap: map[string]uint{},
		robotRules:  map[string]*modules.RobotsResult{},
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
		pluginSlots: make(chan struct{}, rs.MaxConcurrentRequests),
	}

	r.urlCleaner = modules.NewURLCleaner()
	r.urlCleaner.Initialize()
	if job.Settings != nil && len(job.Settings.ExtraTrackingParams) > 0 {
		r.urlCleaner.AddTrackingParams(job.Settings.ExtraTrackingParams)
	}
	r.linkExtractor = modules.NewHTMLLinkExtractor(r.urlCleaner)
	r.sitemapParser = modules.NewSitemapParser(r.urlCleaner)
	r.robotsParser = modules.NewRobotsParser(r.urlCleaner)
	r.phraseDetector = modules.NewSimplePhraseDetector()

	r.frontier = modules.NewDBFrontier(e.db, r.urlCleaner, rs.TeleportProbability)
	r.frontier.SetSkipExtensions(rs.SkipExtensions)
	if job.Settings != nil {
		r.frontier.SetURLFilters(job.Settings.URLIncludePatterns, job.Settings.URLExcludePatterns)
	} else {
		r.frontier.SetURLFilters(nil, nil)
	}
	r.frontier.SetCrawlJob(job.ID)

	jar, _ := cookiejar.New(nil)
	r.http = modules.NewHTTPFetcher(e.transports, e.guard, jar, e.cfg.Security.MaxBodyBytes)

	if err := r.loadPhrases(); err != nil {
		log.Printf("[Run %s] Failed to load phrases: %v", short(job.ID), err)
	}
	if rs.ChangeDetection {
		r.baseJobID = r.findBaseJob()
	}
	return r, nil
}

// loadPhrases loads job-specific phrases, or the global active phrases.
func (r *Run) loadPhrases() error {
	if r.rs.UseCrawlPhrasesOnly {
		return nil
	}
	if len(r.rs.Phrases) > 0 {
		r.phraseDetector.AddPhrases(r.rs.Phrases)
		return nil
	}
	var phrases []models.SearchPhrase
	if err := r.e.db.Where("is_active = ? AND crawl_job_id IS NULL", true).Find(&phrases).Error; err != nil {
		return err
	}
	for _, p := range phrases {
		r.phraseDetector.AddPhrase(p.Phrase)
		r.phraseIDMap[strings.ToLower(p.Phrase)] = p.ID
	}
	return nil
}

// findBaseJob picks the job this run's pages are compared against.
func (r *Run) findBaseJob() string {
	if r.rs.CompareWithJobID != "" && r.rs.CompareWithJobID != r.job.ID {
		return r.rs.CompareWithJobID
	}
	if r.job.ScheduleID == nil {
		return ""
	}
	var prev models.CrawlJob
	err := r.e.db.Select("id").
		Where("schedule_id = ? AND id <> ? AND status = ? AND created_at <= ?", *r.job.ScheduleID, r.job.ID, models.JobStatusCompleted, r.job.CreatedAt).
		Order("created_at DESC").First(&prev).Error
	if err != nil {
		return ""
	}
	return prev.ID
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (r *Run) start() error {
	job := r.job
	now := time.Now()
	if job.StartedAt == nil {
		job.StartedAt = &now
	}
	r.startedAt = now

	parent := context.Background()
	var ctx context.Context
	var cancel context.CancelFunc
	if r.rs.Limits.MaxDurationSec > 0 {
		ctx, cancel = context.WithDeadline(parent, job.StartedAt.Add(time.Duration(r.rs.Limits.MaxDurationSec)*time.Second))
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	r.ctx, r.cancel = ctx, cancel
	r.sideCtx, r.sideStop = context.WithCancel(context.Background())

	var crawled, matches int64
	r.e.db.Model(&models.CrawledPage{}).Where("crawl_job_id = ? AND is_archived = ?", job.ID, false).Count(&crawled)
	r.e.db.Model(&models.PhraseMatch{}).Where("crawl_job_id = ? AND is_archived = ?", job.ID, false).Count(&matches)
	atomic.StoreInt64(&r.crawled, crawled)
	atomic.StoreInt64(&r.matches, matches)
	u := job.Usage
	r.uRequests, r.uPages, r.uBytes, r.uBrowserMs = u.Requests, u.Pages, u.Bytes, u.BrowserMs
	r.uLLMTokens, r.uErrors, r.uScreenshots, r.uProxy = u.LLMTokens, u.Errors, u.Screenshots, u.ProxyRequests
	r.pluginRuns, r.pluginFailures, r.pluginLastErr = job.AfterCrawlRuns, job.AfterCrawlFailures, job.AfterCrawlError

	r.frontier.ResetProcessingURLs()
	r.e.db.Model(&models.CrawlJob{}).Where("id = ?", job.ID).Update("started_at", job.StartedAt)

	if r.frontier.PendingCount() == 0 {
		if err := r.addSeedURLs(); err != nil {
			r.stop(Outcome{Status: models.JobStatusFailed, Err: err})
		}
	} else {
		log.Printf("[Run %s] Resuming with %d pending URLs", short(job.ID), r.frontier.PendingCount())
	}

	r.e.emit(r.snapshotJob(), "job.started", map[string]interface{}{"resumed": crawled > 0})
	log.Printf("[Run %s] Started %s (workers=%d, headless=%v, fingerprint=%s)", short(job.ID), job.TargetURL,
		r.rs.MaxConcurrentRequests, r.rs.UseHeadlessBrowser, r.rs.Fingerprint)

	r.wg.Add(r.rs.MaxConcurrentRequests)
	for i := 0; i < r.rs.MaxConcurrentRequests; i++ {
		go r.worker(i)
	}
	monitorDone := make(chan struct{})
	go r.monitor(monitorDone)
	go func() {
		r.wg.Wait()
		r.cancel()
		<-monitorDone
		r.finish()
	}()
	return nil
}

// stop ends the run with outcome; the first stop wins.
func (r *Run) stop(out Outcome) {
	r.stopMu.Lock()
	if r.stopOutcome == nil {
		o := out
		r.stopOutcome = &o
	}
	r.stopMu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *Run) stopping() bool { return r.ctx.Err() != nil }

func (r *Run) worker(id int) {
	defer r.wg.Done()
	emptyChecks := 0
	for {
		if r.stopping() {
			return
		}
		if reason := r.limitReached(); reason != "" {
			r.stop(Outcome{Status: models.JobStatusCompleted, StopReason: reason})
			return
		}
		fu, err := r.frontier.GetNextURL()
		if err != nil {
			if errors.Is(err, modules.ErrFrontierEmpty) {
				// Done when nothing is pending and no other worker is
				// processing a page that could still add links.
				if atomic.LoadInt64(&r.inflight) == 0 {
					emptyChecks++
					if emptyChecks >= 6 {
						return
					}
				} else {
					emptyChecks = 0
				}
			} else {
				log.Printf("[Run %s] Frontier error: %v", short(r.job.ID), err)
			}
			if !sleepCtx(r.ctx, 500*time.Millisecond) {
				return
			}
			continue
		}
		emptyChecks = 0
		atomic.AddInt64(&r.inflight, 1)
		r.handle(id, fu)
		atomic.AddInt64(&r.inflight, -1)

		if d := r.randomizedDelay(); d > 0 && !sleepCtx(r.ctx, d) {
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// randomizedDelay varies the politeness delay by ±1s (minimum 0).
func (r *Run) randomizedDelay() time.Duration {
	baseMs := r.rs.PolitenessDelay.Milliseconds()
	minMs := baseMs - 1000
	if minMs < 0 {
		minMs = 0
	}
	maxMs := baseMs + 1000
	if baseMs == 0 {
		return 0
	}
	r.rngMu.Lock()
	ms := minMs + r.rng.Int63n(maxMs-minMs+1)
	r.rngMu.Unlock()
	return time.Duration(ms) * time.Millisecond
}

// limitReached returns the stop reason of the first exhausted limit, or "".
func (r *Run) limitReached() string {
	l := r.rs.Limits
	switch {
	case r.rs.MaxPages > 0 && atomic.LoadInt64(&r.crawled) >= int64(r.rs.MaxPages):
		return models.StopReasonMaxPages
	case l.MaxRequests > 0 && atomic.LoadInt64(&r.uRequests) >= int64(l.MaxRequests):
		return models.StopReasonMaxRequests
	case l.MaxBytes > 0 && atomic.LoadInt64(&r.uBytes) >= l.MaxBytes:
		return models.StopReasonMaxBytes
	case l.MaxBrowserSeconds > 0 && atomic.LoadInt64(&r.uBrowserMs) >= int64(l.MaxBrowserSeconds)*1000:
		return models.StopReasonMaxBrowser
	case l.MaxLLMTokens > 0 && atomic.LoadInt64(&r.uLLMTokens) >= int64(l.MaxLLMTokens):
		return models.StopReasonMaxLLM
	}
	return ""
}

func (r *Run) handle(workerID int, fu *models.FrontierURL) {
	if fu.Depth > r.rs.MaxDepth {
		r.frontier.MarkCompleted(fu.ID)
		return
	}
	if r.rs.RespectRobotsTxt && r.isDisallowedByRobots(fu.URL) {
		log.Printf("[Run %s] robots.txt disallows %s", short(r.job.ID), fu.URL)
		r.frontier.MarkCompleted(fu.ID)
		return
	}
	if reason := r.limitReached(); reason != "" {
		// Keep the URL so a resumed job with a higher limit continues from here.
		r.frontier.Release(fu.ID)
		r.stop(Outcome{Status: models.JobStatusCompleted, StopReason: reason})
		return
	}
	r.crawl(workerID, fu)
}

// fetch downloads one frontier entry with the job's fetch strategy.
func (r *Run) fetch(fu *models.FrontierURL) *modules.FetchResult {
	return r.e.fetchWith(r.ctx, r.rs, r.http, fu.URL, fu.RequestMethod, fu.RequestBody, r.job.ID, r.meter)
}

func (r *Run) meter(res *modules.FetchResult) {
	atomic.AddInt64(&r.uRequests, 1)
	atomic.AddInt64(&r.uBytes, int64(len(res.Body)))
	if res.FetchedWith == "browser" {
		atomic.AddInt64(&r.uBrowserMs, res.Duration.Milliseconds())
	}
	if res.ViaProxy {
		atomic.AddInt64(&r.uProxy, 1)
	}
	if res.Error != nil && r.ctx.Err() == nil {
		atomic.AddInt64(&r.uErrors, 1)
	}
}

func needsBrowser(res *modules.FetchResult) bool {
	if res.Error != nil && res.StatusCode == 0 {
		return false // network failure; the browser would fail the same way
	}
	if modules.LooksBlocked(res.StatusCode, res.Body) {
		return true
	}
	return strings.Contains(strings.ToLower(res.ContentType), "html") && modules.LooksLikeSPAShell(res.Body)
}

func retryAfter(res *modules.FetchResult) time.Duration {
	if res.Header != nil {
		if v := res.Header.Get("Retry-After"); v != "" {
			if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
				return time.Duration(secs) * time.Second
			}
			if t, err := time.Parse(time.RFC1123, v); err == nil {
				return time.Until(t)
			}
		}
	}
	return 5 * time.Second
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// monitor flushes progress, renews the lease and applies control requests.
func (r *Run) monitor(done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastProgress := time.Now()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			if !r.flush() {
				log.Printf("[Run %s] Lease lost; another worker owns this job now", short(r.job.ID))
				r.stop(Outcome{StopReason: "lease_lost"})
				return
			}
			r.applyControl()
			if time.Since(lastProgress) >= 30*time.Second {
				lastProgress = time.Now()
				r.e.emit(r.snapshotJob(), "job.progress", map[string]interface{}{
					"crawled": atomic.LoadInt64(&r.crawled),
					"matches": atomic.LoadInt64(&r.matches),
				})
			}
		}
	}
}

func (r *Run) usage() models.JobUsage {
	return models.JobUsage{
		Requests:      atomic.LoadInt64(&r.uRequests),
		Pages:         atomic.LoadInt64(&r.uPages),
		Bytes:         atomic.LoadInt64(&r.uBytes),
		BrowserMs:     atomic.LoadInt64(&r.uBrowserMs),
		LLMTokens:     atomic.LoadInt64(&r.uLLMTokens),
		Errors:        atomic.LoadInt64(&r.uErrors),
		Screenshots:   atomic.LoadInt64(&r.uScreenshots),
		ProxyRequests: atomic.LoadInt64(&r.uProxy),
	}
}

// pluginColumns adds after-crawl plugin statistics to an update.
func (r *Run) pluginColumns(into map[string]interface{}) {
	into["after_crawl_runs"] = atomic.LoadInt64(&r.pluginRuns)
	into["after_crawl_failures"] = atomic.LoadInt64(&r.pluginFailures)
	r.pluginErrMu.Lock()
	into["after_crawl_error"] = r.pluginLastErr
	r.pluginErrMu.Unlock()
}

func usageColumns(u models.JobUsage, into map[string]interface{}) {
	into["usage_requests"] = u.Requests
	into["usage_pages"] = u.Pages
	into["usage_bytes"] = u.Bytes
	into["usage_browser_ms"] = u.BrowserMs
	into["usage_llm_tokens"] = u.LLMTokens
	into["usage_errors"] = u.Errors
	into["usage_screenshots"] = u.Screenshots
	into["usage_proxy_requests"] = u.ProxyRequests
}

// flush writes counters and renews the lease. It returns false when this
// worker no longer owns the job.
func (r *Run) flush() bool {
	pending, processing, completed, failed := r.frontier.GetStats()
	lease := time.Now().Add(r.e.cfg.Worker.LeaseTTL)
	updates := map[string]interface{}{
		"crawled_urls":     atomic.LoadInt64(&r.crawled),
		"found_matches":    atomic.LoadInt64(&r.matches),
		"total_urls":       pending + processing + completed + failed,
		"lease_expires_at": lease,
	}
	usageColumns(r.usage(), updates)
	r.pluginColumns(updates)
	res := r.e.db.Model(&models.CrawlJob{}).
		Where("id = ? AND worker_id = ? AND status = ?", r.job.ID, r.e.workerID, models.JobStatusRunning).
		Updates(updates)
	if res.Error != nil {
		log.Printf("[Run %s] Failed to flush progress: %v", short(r.job.ID), res.Error)
		return true // transient DB error: keep crawling, retry next tick
	}
	if res.RowsAffected == 0 {
		var owner models.CrawlJob
		if err := r.e.db.Select("worker_id", "status").First(&owner, "id = ?", r.job.ID).Error; err != nil {
			return !errors.Is(err, gorm.ErrRecordNotFound)
		}
		return owner.WorkerID == r.e.workerID && owner.Status == models.JobStatusRunning
	}
	r.jobMu.Lock()
	r.job.CrawledURLs = int(updates["crawled_urls"].(int64))
	r.job.FoundMatches = int(updates["found_matches"].(int64))
	r.job.TotalURLs = int(pending + processing + completed + failed)
	r.job.Usage = r.usage()
	r.jobMu.Unlock()
	return true
}

// applyControl reads pause/cancel requests written by any process.
func (r *Run) applyControl() {
	var row models.CrawlJob
	if err := r.e.db.Select("control").First(&row, "id = ?", r.job.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			r.stop(Outcome{Status: models.JobStatusCancelled, StopReason: "deleted"})
		}
		return
	}
	switch row.Control {
	case models.ControlPause:
		r.stop(Outcome{Status: models.JobStatusPaused, StopReason: models.StopReasonUser})
	case models.ControlCancel:
		r.stop(Outcome{Status: models.JobStatusCancelled, StopReason: models.StopReasonUser})
	}
}

// snapshotJob returns a copy of the job safe to hand to hooks.
func (r *Run) snapshotJob() *models.CrawlJob {
	r.jobMu.Lock()
	defer r.jobMu.Unlock()
	j := *r.job
	return &j
}

func (r *Run) stats() RunStats {
	r.jobMu.Lock()
	job := r.job
	st := RunStats{JobID: job.ID, TargetURL: job.TargetURL, OwnerRef: job.OwnerRef, StartedAt: r.startedAt, Limits: r.rs.Limits}
	r.jobMu.Unlock()
	st.Crawled = atomic.LoadInt64(&r.crawled)
	st.Matches = atomic.LoadInt64(&r.matches)
	st.InFlight = atomic.LoadInt64(&r.inflight)
	st.Usage = r.usage()
	if mins := time.Since(r.startedAt).Minutes(); mins > 0.05 {
		st.PagesPerMin = float64(st.Usage.Pages) / mins
	}
	if r.rs.MaxPages > 0 {
		st.Limits.MaxPages = r.rs.MaxPages
	}
	return st
}

// finish runs once all workers have exited.
func (r *Run) finish() {
	// Give after-crawl plugins and embeddings a bounded time to complete.
	sideDone := make(chan struct{})
	go func() { r.sideWG.Wait(); close(sideDone) }()
	select {
	case <-sideDone:
	case <-time.After(2 * time.Minute):
		log.Printf("[Run %s] Side tasks still running after 2m; finishing anyway", short(r.job.ID))
	}
	r.sideStop()

	r.stopMu.Lock()
	var out Outcome
	if r.stopOutcome != nil {
		out = *r.stopOutcome
	} else if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
		out = Outcome{Status: models.JobStatusCompleted, StopReason: models.StopReasonMaxDuration}
	} else {
		out = Outcome{Status: models.JobStatusCompleted, StopReason: models.StopReasonFrontier}
	}
	r.stopMu.Unlock()

	if out.Status != "" {
		r.finalize(&out)
	}
	close(r.finished)
	r.e.runEnded(r, out)
}

func (r *Run) finalize(out *Outcome) {
	job := r.job
	if out.Status == models.JobStatusPaused || out.Status == models.JobStatusQueued {
		r.frontier.ResetProcessingURLs()
	}
	if r.baseJobID != "" && out.Status == models.JobStatusCompleted && out.StopReason == models.StopReasonFrontier {
		r.detectRemovedPages()
	}
	if r.baseJobID != "" {
		out.Changes = &ChangeSummary{BaseJobID: r.baseJobID, New: atomic.LoadInt64(&r.changesNew), Changed: atomic.LoadInt64(&r.changesChanged)}
		r.e.db.Model(&models.PageChange{}).Where("crawl_job_id = ? AND change_type = ?", job.ID, models.ChangeRemoved).Count(&out.Changes.Removed)
	}

	pending, processing, completed, failed := r.frontier.GetStats()
	now := time.Now()
	updates := map[string]interface{}{
		"status":           out.Status,
		"crawled_urls":     atomic.LoadInt64(&r.crawled),
		"found_matches":    atomic.LoadInt64(&r.matches),
		"total_urls":       pending + processing + completed + failed,
		"stop_reason":      out.StopReason,
		"worker_id":        "",
		"lease_expires_at": nil,
		"control":          "",
	}
	usageColumns(r.usage(), updates)
	r.pluginColumns(updates)
	if out.Err != nil {
		updates["error_message"] = out.Err.Error()
	}
	if out.Status.IsTerminal() {
		updates["completed_at"] = now
		if r.rs.AfterJobPlugin != "" && out.Status != models.JobStatusFailed {
			due := now.Add(r.e.cfg.Plugins.AfterJobDelay)
			updates["after_job_plugin"] = r.rs.AfterJobPlugin
			updates["after_job_due_at"] = due
			updates["after_job_state"] = "scheduled"
			updates["after_job_worker"] = r.e.workerID
		}
	}
	if out.Status == models.JobStatusQueued && job.QueuedAt == nil {
		updates["queued_at"] = now
	}
	res := r.e.db.Model(&models.CrawlJob{}).Where("id = ? AND worker_id = ?", job.ID, r.e.workerID).Updates(updates)
	if res.Error != nil {
		log.Printf("[Run %s] Failed to save final state: %v", short(job.ID), res.Error)
	} else if res.RowsAffected == 0 {
		log.Printf("[Run %s] Final state not saved: job no longer owned by this worker", short(job.ID))
	}

	r.jobMu.Lock()
	job.Status = out.Status
	job.StopReason = out.StopReason
	job.CrawledURLs = int(atomic.LoadInt64(&r.crawled))
	job.FoundMatches = int(atomic.LoadInt64(&r.matches))
	job.TotalURLs = int(pending + processing + completed + failed)
	job.Usage = r.usage()
	job.WorkerID = ""
	job.LeaseExpiresAt = nil
	if out.Status.IsTerminal() {
		job.CompletedAt = &now
	}
	if out.Err != nil {
		job.ErrorMessage = out.Err.Error()
	}
	r.jobMu.Unlock()

	log.Printf("[Run %s] Finished: status=%s reason=%s crawled=%d requests=%d", short(job.ID), out.Status,
		out.StopReason, job.CrawledURLs, job.Usage.Requests)

	if r.rs.EnableSemanticSearch && out.Status != models.JobStatusQueued {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if err := r.e.semanticSearcher.RebuildIndex(ctx); err != nil {
				log.Printf("[SemanticSearch] Failed to rebuild FAISS index: %v", err)
			}
		}()
	}
}

// addSeedURLs adds initial seed URLs for the job.
func (r *Run) addSeedURLs() error {
	job := r.job
	method, bodies, err := expandSeedRequests(job.Settings)
	if err != nil {
		return err
	}
	seeds := job.SeedURLs
	if len(seeds) == 0 {
		seeds = models.StringSlice{job.TargetURL}
	}
	if method != "POST" {
		for _, seed := range seeds {
			r.frontier.AddSeedURLs(seed)
		}
		return nil
	}
	// POST seeds skip robots.txt/sitemap discovery — those are GET-only documents.
	for _, seed := range seeds {
		for _, body := range bodies {
			if err := r.frontier.AddSeedRequest(seed, "POST", body); err != nil {
				log.Printf("[Run %s] Failed to add POST seed %s: %v", short(job.ID), seed, err)
			}
		}
	}
	return nil
}

// expandSeedRequests returns the HTTP method and expanded POST bodies for seed URLs.
// GET jobs return method "GET" and a nil body list.
func expandSeedRequests(s *models.JobSettings) (string, []string, error) {
	if s == nil || s.RequestMethod == nil {
		return "GET", nil, nil
	}
	method := strings.ToUpper(strings.TrimSpace(*s.RequestMethod))
	if method != "POST" {
		return "GET", nil, nil
	}
	body := ""
	if s.RequestBody != nil {
		body = *s.RequestBody
	}
	if !modules.HasTemplateVars(body) {
		return "POST", []string{body}, nil
	}
	names := modules.ExtractTemplateVars(body)
	expandedVars := make(map[string][]string, len(names))
	for _, name := range names {
		expr := ""
		if s.RequestBodyVars != nil {
			expr = s.RequestBodyVars[name]
		}
		vals, err := modules.ExpandValueExpr(expr)
		if err != nil {
			return "", nil, fmt.Errorf("error expanding payload variable {{%s}}: %w", name, err)
		}
		if len(vals) == 0 {
			return "", nil, fmt.Errorf("payload variable {{%s}} has no values", name)
		}
		expandedVars[name] = vals
	}
	bodies, err := modules.ExpandTemplate(body, expandedVars, 50000)
	if err != nil {
		return "", nil, fmt.Errorf("error expanding payload template: %w", err)
	}
	return "POST", bodies, nil
}

func requestMethodLabel(method string) string {
	if strings.TrimSpace(method) == "" {
		return "GET"
	}
	return strings.ToUpper(method)
}
