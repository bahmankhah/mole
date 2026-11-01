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

	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"gorm.io/gorm"
)

const (
	// staleClaimTimeout is how long a frontier entry may sit claimed by a
	// worker before it is treated as abandoned and offered again. It has to
	// outlast the slowest legitimate fetch, since a live worker's claims
	// must never be handed to someone else.
	staleClaimTimeout = 15 * time.Minute

	// reapInterval is how often a running crawl's owner looks for URLs left
	// claimed by a worker that has since stopped reporting in.
	reapInterval = time.Minute
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

	// seedDomains are the base domains of the job's start URLs. Every one
	// of them is in scope, so a crawl given several sites covers them all
	// without also having to list them under scope_domains.
	//
	// A start URL can be added while the run is going, so this is swapped
	// wholesale by the monitor rather than mutated: inScope reads it for
	// every link found, on every worker goroutine.
	seedDomains   atomic.Value // map[string]bool
	seedURLsKnown string       // the start-URL list the map was built from

	// A run is either the job's owner — the worker holding the job's lease,
	// which reports progress and writes the final state — or a helper, an
	// otherwise-idle worker that joined to pull from the same frontier
	// (models.CrawlJobWorker). A helper never touches the crawl_jobs row:
	// it renews its own participation lease and leaves quietly.
	isHelper    bool
	helperRowID uint

	helpersMu      sync.Mutex
	helpersActive  bool
	helpersChecked time.Time

	// inflightURLs are the frontier entries this run has claimed and not
	// finished. They are released on the way out so a pause or a shutdown
	// doesn't leave them to sit out the visibility timeout.
	inflightMu  sync.Mutex
	inflightSet map[uint]bool

	// domainPages counts the pages taken from each site, for
	// max_pages_per_domain: one site of a multi-site crawl must not be
	// able to spend the whole page budget. Several workers may be crawling
	// the same site at once, so the counts are refreshed from the database
	// rather than only counted locally — otherwise each worker would let
	// the site spend the whole budget over again.
	domainMu    sync.Mutex
	domainPages map[string]int
	domainFull  map[string]bool

	baseJobID string

	crawled int64
	kept    int64 // crawled pages not skipped by keep_tags
	matches int64

	templates *content.TemplateIndex // the site's page templates, learned during the run
	inflight  int64

	// usage counters (absolute values, seeded from the job row)
	uRequests, uPages, uBytes, uBrowserMs, uLLMTokens, uErrors, uScreenshots, uProxy, uDocPages int64

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
	// The job's max_depth applies unless its settings override it; a batch
	// fetches exactly its URLs.
	if job.Settings == nil || job.Settings.MaxDepth == nil {
		rs.MaxDepth = job.MaxDepth
	}
	if job.Kind == models.JobKindBatch {
		rs.MaxDepth = 0
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
		templates:   content.NewTemplateIndex(),
	}

	r.urlCleaner = modules.NewURLCleaner()
	r.urlCleaner.Initialize()
	if job.Settings != nil && len(job.Settings.ExtraTrackingParams) > 0 {
		r.urlCleaner.AddTrackingParams(job.Settings.ExtraTrackingParams)
	}
	r.setSeedDomains(seedDomainsOf(r.urlCleaner, job), strings.Join(job.SeedURLs, "\n"))
	r.inflightSet = map[uint]bool{}
	r.domainPages = map[string]int{}
	r.domainFull = map[string]bool{}
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
	r.frontier.SetWorker(e.workerID)

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

// setSeedDomains publishes a new in-scope set for the run's workers.
func (r *Run) setSeedDomains(domains map[string]bool, from string) {
	r.seedDomains.Store(domains)
	r.seedURLsKnown = from // monitor-only, so it needs no lock of its own
}

// inSeedDomain reports whether a base domain is one of the run's start
// sites. The map is replaced, never written in place, so reading it takes
// no lock on a path walked once per discovered link.
func (r *Run) inSeedDomain(domain string) bool {
	m, _ := r.seedDomains.Load().(map[string]bool)
	return m[domain]
}

// hasActiveHelpers reports whether another worker is still pulling from
// this job's frontier, so its owner knows not to call the crawl finished
// out from under it: a helper mid-fetch can still add links. Every idle
// worker goroutine asks this while the queue is drained, so the answer is
// held briefly rather than counted again for each of them.
func (r *Run) hasActiveHelpers() bool {
	r.helpersMu.Lock()
	defer r.helpersMu.Unlock()
	if time.Since(r.helpersChecked) < time.Second {
		return r.helpersActive
	}
	var n int64
	r.e.db.Model(&models.CrawlJobWorker{}).
		Where("crawl_job_id = ? AND status = ? AND worker_id <> ? AND (lease_expires_at IS NULL OR lease_expires_at > ?)",
			r.job.ID, models.JobWorkerRunning, r.e.workerID, time.Now()).
		Count(&n)
	r.helpersActive = n > 0
	r.helpersChecked = time.Now()
	return r.helpersActive
}

// trackInflight records a frontier entry this run has claimed, so it can be
// released if the run stops before finishing it.
func (r *Run) trackInflight(id uint) {
	r.inflightMu.Lock()
	if r.inflightSet == nil {
		r.inflightSet = map[uint]bool{}
	}
	r.inflightSet[id] = true
	r.inflightMu.Unlock()
}

func (r *Run) untrackInflight(id uint) {
	r.inflightMu.Lock()
	delete(r.inflightSet, id)
	r.inflightMu.Unlock()
}

// releaseInflight puts back every entry this run claimed but never
// finished, so a pause or shutdown doesn't strand them in processing until
// the visibility timeout expires.
func (r *Run) releaseInflight() {
	r.inflightMu.Lock()
	ids := make([]uint, 0, len(r.inflightSet))
	for id := range r.inflightSet {
		ids = append(ids, id)
	}
	r.inflightSet = map[uint]bool{}
	r.inflightMu.Unlock()
	for _, id := range ids {
		if err := r.frontier.Release(id); err != nil {
			log.Printf("[Run %s] Failed to release URL %d: %v", short(r.job.ID), id, err)
		}
	}
}

// seedDomainsOf collects the base domains of a job's start URLs, which are
// in scope alongside the target's own domain. Giving a crawl several start
// URLs is how one run covers several sites and can compare them.
func seedDomainsOf(cleaner *modules.URLCleaner, job *models.CrawlJob) map[string]bool {
	out := map[string]bool{}
	for _, seed := range job.SeedURLs {
		if domain := cleaner.ExtractBaseDomain(seed); domain != "" && domain != job.Domain {
			out[domain] = true
		}
	}
	return out
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

	var crawled, kept, matches int64
	r.e.db.Model(&models.CrawledPage{}).Where("crawl_job_id = ? AND is_archived = ?", job.ID, false).Count(&crawled)
	r.e.db.Model(&models.CrawledPage{}).Where("crawl_job_id = ? AND is_archived = ? AND skipped = ?", job.ID, false, false).Count(&kept)
	atomic.StoreInt64(&r.kept, kept)
	r.e.db.Model(&models.PhraseMatch{}).Where("crawl_job_id = ? AND is_archived = ?", job.ID, false).Count(&matches)
	atomic.StoreInt64(&r.crawled, crawled)
	atomic.StoreInt64(&r.matches, matches)
	u := job.Usage
	r.uRequests, r.uPages, r.uBytes, r.uBrowserMs = u.Requests, u.Pages, u.Bytes, u.BrowserMs
	r.uLLMTokens, r.uErrors, r.uScreenshots, r.uProxy, r.uDocPages = u.LLMTokens, u.Errors, u.Screenshots, u.ProxyRequests, u.DocumentPages
	r.pluginRuns, r.pluginFailures, r.pluginLastErr = job.AfterCrawlRuns, job.AfterCrawlFailures, job.AfterCrawlError

	// Only claims left behind by a worker that died go back on the queue:
	// another worker may be pulling from this same frontier right now.
	if n, err := r.frontier.ReleaseAbandonedClaims(r.e.liveWorkerIDs(), staleClaimTimeout); err != nil {
		log.Printf("[Run %s] Failed to release abandoned URL claims: %v", short(job.ID), err)
	} else if n > 0 {
		log.Printf("[Run %s] Put %d URL(s) back that a stopped worker had claimed", short(job.ID), n)
	}

	if r.isHelper {
		log.Printf("[Run %s] Helping with %d pending URLs (workers=%d)", short(job.ID),
			r.frontier.PendingCount(), r.rs.MaxConcurrentRequests)
	} else {
		r.e.db.Model(&models.CrawlJob{}).Where("id = ?", job.ID).Update("started_at", job.StartedAt)
		if pending := r.frontier.PendingCount(); pending > 0 {
			log.Printf("[Run %s] Resuming with %d pending URLs", short(job.ID), pending)
		}
		// Queue the start URLs every time, not only for a crawl starting
		// from an empty frontier: a resumed crawl may have been given a
		// start URL it never queued, and a URL already crawled or queued
		// is skipped here anyway.
		if err := r.addSeedURLs(); err != nil {
			r.stop(Outcome{Status: models.JobStatusFailed, Err: err})
		}
		r.e.emit(r.snapshotJob(), "job.started", map[string]interface{}{"resumed": crawled > 0})
		log.Printf("[Run %s] Started %s (workers=%d, headless=%v, fingerprint=%s)", short(job.ID), job.TargetURL,
			r.rs.MaxConcurrentRequests, r.rs.UseHeadlessBrowser, r.rs.Fingerprint)
	}

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
				// Done when nothing is pending, nothing this run fetched is
				// still in flight, and — for the job's owner — no helper on
				// another worker is still pulling, since either could yet
				// add links. A helper just leaves; the owner carries on.
				idle := atomic.LoadInt64(&r.inflight) == 0
				if idle && !r.isHelper && r.hasActiveHelpers() {
					idle = false
				}
				if idle {
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
		if r.domainBudgetSpent(fu.URL) {
			// This site has had its share of the page budget; drop the URL
			// and carry on with the others.
			if err := r.frontier.MarkCompleted(fu.ID); err != nil {
				log.Printf("[Run %s] Failed to drop over-budget URL %s: %v", short(r.job.ID), fu.URL, err)
			}
			continue
		}
		atomic.AddInt64(&r.inflight, 1)
		r.trackInflight(fu.ID)
		r.handle(id, fu)
		r.untrackInflight(fu.ID)
		atomic.AddInt64(&r.inflight, -1)

		if d := r.randomizedDelay(); d > 0 && !sleepCtx(r.ctx, d) {
			return
		}
	}
}

// domainBudgetSpent counts one page against the URL's site and reports
// whether that site is already at max_pages_per_domain. Without the cap
// a single sprawling site can starve the others in a multi-site crawl.
func (r *Run) domainBudgetSpent(rawURL string) bool {
	if r.rs.MaxPagesPerDomain <= 0 {
		return false
	}
	domain := r.urlCleaner.ExtractBaseDomain(rawURL)
	if domain == "" {
		return false
	}
	r.domainMu.Lock()
	defer r.domainMu.Unlock()
	if r.domainPages[domain] >= r.rs.MaxPagesPerDomain {
		if !r.domainFull[domain] {
			r.domainFull[domain] = true
			log.Printf("[Run %s] %s reached its %d-page share; the crawl carries on with the other sites",
				short(r.job.ID), domain, r.rs.MaxPagesPerDomain)
		}
		return true
	}
	r.domainPages[domain]++
	return false
}

// syncDomainPages refreshes the per-site page counts from the pages
// actually stored for this job. Several workers can be crawling the same
// site at once, and each only counts what it fetched itself, so without
// this each of them would let that site spend the whole
// max_pages_per_domain budget over again. Called from the monitor tick,
// off the path walked for every URL.
func (r *Run) syncDomainPages() {
	if r.rs.MaxPagesPerDomain <= 0 {
		return
	}
	var rows []struct {
		Domain string
		N      int
	}
	if err := r.e.db.Model(&models.CrawledPage{}).
		Select("domain, COUNT(*) AS n").
		Where("crawl_job_id = ? AND is_archived = ?", r.job.ID, false).
		Group("domain").Scan(&rows).Error; err != nil {
		log.Printf("[Run %s] Failed to refresh per-site page counts: %v", short(r.job.ID), err)
		return
	}
	r.domainMu.Lock()
	defer r.domainMu.Unlock()
	for _, row := range rows {
		if row.Domain != "" && row.N > r.domainPages[row.Domain] {
			r.domainPages[row.Domain] = row.N
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
	case r.rs.MaxKeptPages > 0 && atomic.LoadInt64(&r.kept) >= int64(r.rs.MaxKeptPages):
		return models.StopReasonMaxKept
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
	lastReap := time.Now()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			if !r.flush() {
				if r.isHelper {
					log.Printf("[Run %s] Stopping help: the crawl no longer needs this worker", short(r.job.ID))
				} else {
					log.Printf("[Run %s] Lease lost; another worker owns this job now", short(r.job.ID))
				}
				r.stop(Outcome{StopReason: "lease_lost"})
				return
			}
			r.applyControl()
			r.syncDomainPages()
			// A helper can die mid-fetch; put what it was holding back so
			// the crawl doesn't finish without those URLs.
			if !r.isHelper && time.Since(lastReap) >= reapInterval {
				lastReap = time.Now()
				if n, err := r.frontier.ReleaseAbandonedClaims(r.e.liveWorkerIDs(), staleClaimTimeout); err == nil && n > 0 {
					log.Printf("[Run %s] Put %d URL(s) back that a stopped worker had claimed", short(r.job.ID), n)
				}
			}
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
		DocumentPages: atomic.LoadInt64(&r.uDocPages),
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
	into["usage_document_pages"] = u.DocumentPages
}

// flush writes counters and renews the lease. It returns false when this
// worker should stop: it no longer owns the job, or — for a helper — its
// participation lease is gone.
func (r *Run) flush() bool {
	r.refreshCounters()
	if r.isHelper {
		return r.flushHelper()
	}
	pending, processing, completed, failed := r.frontier.GetStats()
	lease := time.Now().Add(r.e.cfg.Worker.LeaseTTL)
	updates := map[string]interface{}{
		"crawled_urls":     atomic.LoadInt64(&r.crawled),
		"kept_urls":        atomic.LoadInt64(&r.kept),
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
	r.job.KeptURLs = int(updates["kept_urls"].(int64))
	r.job.FoundMatches = int(updates["found_matches"].(int64))
	r.job.TotalURLs = int(pending + processing + completed + failed)
	r.job.Usage = r.usage()
	r.jobMu.Unlock()
	return true
}

// refreshCounters re-reads what the crawl has stored so far. Each worker
// only counts the pages it fetched itself, so with several of them on one
// crawl the owner would under-report progress and every one of them would
// allow the page and match limits all over again. The counts here cover
// the whole crawl, whoever fetched the page.
func (r *Run) refreshCounters() {
	var crawled, kept, matches int64
	r.e.db.Model(&models.CrawledPage{}).Where("crawl_job_id = ? AND is_archived = ?", r.job.ID, false).Count(&crawled)
	r.e.db.Model(&models.CrawledPage{}).Where("crawl_job_id = ? AND is_archived = ? AND skipped = ?", r.job.ID, false, false).Count(&kept)
	r.e.db.Model(&models.PhraseMatch{}).Where("crawl_job_id = ? AND is_archived = ?", r.job.ID, false).Count(&matches)
	// Never move a counter backwards: this run may have saved pages the
	// counts above were taken just before.
	storeIfLarger(&r.crawled, crawled)
	storeIfLarger(&r.kept, kept)
	storeIfLarger(&r.matches, matches)
}

func storeIfLarger(target *int64, v int64) {
	for {
		cur := atomic.LoadInt64(target)
		if v <= cur || atomic.CompareAndSwapInt64(target, cur, v) {
			return
		}
	}
}

// flushHelper renews a helper's participation lease. It returns false once
// this worker should stop pulling: its lease was reclaimed (it looked dead),
// or the job itself is no longer running.
func (r *Run) flushHelper() bool {
	lease := time.Now().Add(r.e.cfg.Worker.LeaseTTL)
	res := r.e.db.Model(&models.CrawlJobWorker{}).
		Where("id = ? AND worker_id = ? AND status = ?", r.helperRowID, r.e.workerID, models.JobWorkerRunning).
		Update("lease_expires_at", lease)
	if res.Error != nil {
		log.Printf("[Run %s] Failed to renew participation lease: %v", short(r.job.ID), res.Error)
		return true // transient DB error: keep crawling, retry next tick
	}
	if res.RowsAffected == 0 {
		return false
	}
	var job models.CrawlJob
	if err := r.e.db.Select("status").First(&job, "id = ?", r.job.ID).Error; err != nil {
		return !errors.Is(err, gorm.ErrRecordNotFound)
	}
	return job.Status == models.JobStatusRunning
}

// applyControl reads pause/cancel requests written by any process.
func (r *Run) applyControl() {
	var row models.CrawlJob
	if err := r.e.db.Select("control", "seed_urls").First(&row, "id = ?", r.job.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			r.stop(Outcome{Status: models.JobStatusCancelled, StopReason: "deleted"})
		}
		return
	}
	r.applyAddedStartURLs(row.SeedURLs)
	switch row.Control {
	case models.ControlPause:
		r.stop(Outcome{Status: models.JobStatusPaused, StopReason: models.StopReasonUser})
	case models.ControlCancel:
		r.stop(Outcome{Status: models.JobStatusCancelled, StopReason: models.StopReasonUser})
	}
}

// applyAddedStartURLs picks up start URLs added since the run began: it
// queues them and widens the run's scope to their sites, so the crawl
// follows links *within* a newly added site rather than fetching the one
// page and stopping.
func (r *Run) applyAddedStartURLs(seeds models.StringSlice) {
	key := strings.Join(seeds, "\n")
	if key == r.seedURLsKnown {
		return
	}
	r.jobMu.Lock()
	r.job.SeedURLs = seeds
	job := *r.job
	r.jobMu.Unlock()
	domains := seedDomainsOf(r.urlCleaner, &job)
	r.setSeedDomains(domains, key)
	if !r.isHelper && r.e != nil {
		if err := r.addSeedURLs(); err != nil {
			log.Printf("[Run %s] Failed to queue added start URLs: %v", short(r.job.ID), err)
		}
	}
	log.Printf("[Run %s] Start URLs changed; now covering %d extra site(s)", short(r.job.ID), len(domains))
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
	st := RunStats{JobID: job.ID, TargetURL: job.TargetURL, OwnerRef: job.OwnerRef,
		StartedAt: r.startedAt, Limits: r.rs.Limits, Helping: r.isHelper}
	r.jobMu.Unlock()
	st.Crawled = atomic.LoadInt64(&r.crawled)
	st.Kept = atomic.LoadInt64(&r.kept)
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

	if r.isHelper {
		r.finishHelper()
		close(r.finished)
		r.e.helperRunEnded(r)
		return
	}
	if out.Status != "" {
		r.finalize(&out)
	}
	close(r.finished)
	r.e.runEnded(r, out)
}

// finishHelper signs this worker off the job: it puts back whatever it had
// claimed and marks its participation done, so the owner stops waiting on
// it. It never touches the crawl_jobs row — only the owner writes that.
func (r *Run) finishHelper() {
	r.releaseInflight()
	r.e.db.Model(&models.CrawlJobWorker{}).
		Where("id = ? AND worker_id = ?", r.helperRowID, r.e.workerID).
		Updates(map[string]interface{}{"status": models.JobWorkerDone, "lease_expires_at": nil})
	log.Printf("[Run %s] Stopped helping: crawled %d page(s)", short(r.job.ID), atomic.LoadInt64(&r.uPages))
}

func (r *Run) finalize(out *Outcome) {
	job := r.job
	// Put back whatever this run still had claimed, so a resumed crawl
	// picks them up at once instead of waiting out the visibility timeout.
	r.releaseInflight()
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
		"kept_urls":        atomic.LoadInt64(&r.kept),
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
	job.KeptURLs = int(atomic.LoadInt64(&r.kept))
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
	// The crawl starts from its target URL and from every start URL listed
	// beside it. Dropping the target whenever a list exists would leave the
	// site the job is named after uncrawled, which is how a two-site
	// comparison ends up with only one site in it.
	seeds := startURLsOf(job)
	if method != "POST" {
		for _, seed := range seeds {
			if job.Kind == models.JobKindBatch {
				// A batch fetches its URLs only: no robots.txt or sitemap discovery.
				if err := r.frontier.AddSeedRequest(seed, "", ""); err != nil {
					log.Printf("[Run %s] Failed to add batch URL %s: %v", short(job.ID), seed, err)
				}
				continue
			}
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

// startURLsOf is every URL a job starts from: its target URL and its start
// URLs, in that order, without repeating one that appears in both.
func startURLsOf(job *models.CrawlJob) models.StringSlice {
	out := make(models.StringSlice, 0, len(job.SeedURLs)+1)
	seen := map[string]bool{}
	for _, u := range append(models.StringSlice{job.TargetURL}, job.SeedURLs...) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
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
