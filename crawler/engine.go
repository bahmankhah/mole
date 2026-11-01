// Package crawler runs crawl jobs. An Engine holds everything shared between
// jobs (network layer, browser pool, NLP modules); each running job is a Run
// with its own frontier, workers, settings and counters, so many jobs can run
// in one process at the same time.
package crawler

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"github.com/resolver/crawler/netx"
	"github.com/resolver/crawler/storage"
	"gorm.io/gorm"
)

// workerLivenessWindow is how long since its last heartbeat a worker is
// still assumed to be alive, matching what the job manager reports.
const workerLivenessWindow = 2 * time.Minute

// Outcome is how a run ended.
type Outcome struct {
	Status     models.JobStatus
	StopReason string
	Err        error
	Changes    *ChangeSummary
}

// ChangeSummary counts change-detection results of a run.
type ChangeSummary struct {
	BaseJobID string `json:"base_job_id"`
	New       int64  `json:"new"`
	Changed   int64  `json:"changed"`
	Removed   int64  `json:"removed"`
}

// Hooks let the job manager react to engine events.
type Hooks struct {
	// RunEnded is called after a run has written its final state.
	RunEnded func(job *models.CrawlJob, outcome Outcome)
	// Event is called for job/page events that may be delivered as webhooks.
	Event func(job *models.CrawlJob, event string, data map[string]interface{})
}

// Engine is the shared crawl runtime.
type Engine struct {
	cfg      *config.Config
	db       *gorm.DB
	store    storage.Store
	llm      *content.LLMClient
	workerID string

	guard       *netx.Guard
	proxies     *netx.ProxyManager
	transports  *netx.TransportPool
	hostLimiter *netx.HostLimiter
	browsers    *modules.BrowserPool

	stemmer          *modules.Stemmer
	wordExtractor    *modules.WordExtractor
	semanticSearcher *modules.SemanticSearcher
	cookieDir        string

	hooks Hooks

	mu         sync.RWMutex
	runs       map[string]*Run
	helperRuns map[string]*Run // jobs owned by another worker that this one is helping crawl
}

// Deps are the engine's external dependencies.
type Deps struct {
	Config   *config.Config
	DB       *gorm.DB
	Store    storage.Store
	WorkerID string
}

// NewEngine builds the shared runtime.
func NewEngine(d Deps) (*Engine, error) {
	cfg := d.Config
	guard, err := netx.NewGuard(cfg.Security.AllowPrivateNetworks, cfg.Security.AllowedCIDRs)
	if err != nil {
		return nil, err
	}
	routes := make([]netx.ProxyRoute, 0, len(cfg.Network.ProxyRoutes))
	for _, r := range cfg.Network.ProxyRoutes {
		routes = append(routes, netx.ProxyRoute{Match: r.Match, Pool: r.Pool, Region: r.Region})
	}
	proxies, err := netx.NewProxyManager(cfg.Network.ProxyPools, routes, cfg.Network.DefaultProxyPool)
	if err != nil {
		return nil, err
	}

	stemmer := modules.NewStemmer(cfg.Crawler)
	stemmer.Initialize()
	wordExtractor := modules.NewWordExtractor(d.DB, stemmer)
	wordExtractor.Initialize()

	script := modules.FindPluginScript("headless_fetch.py", cfg.Crawler.HeadlessScriptPath)
	e := &Engine{
		cfg:      cfg,
		db:       d.DB,
		store:    d.Store,
		llm:      content.NewLLMClient(cfg.LLM),
		workerID: d.WorkerID,
		guard:    guard,
		proxies:  proxies,
		transports: netx.NewTransportPool(guard, netx.TransportOptions{
			ResponseTimeout: cfg.Crawler.RequestTimeout,
		}),
		hostLimiter:      netx.NewHostLimiter(cfg.Network.HostRateLimit),
		browsers:         modules.NewBrowserPool(modules.FindPython(cfg.Crawler.PythonPath), script, cfg.Headless.PoolSize, !guard.AllowsPrivate()),
		stemmer:          stemmer,
		wordExtractor:    wordExtractor,
		semanticSearcher: modules.NewSemanticSearcher(cfg.Crawler, d.DB),
		cookieDir:        filepath.Join(filepath.Dir(script), ".cookies"),
		runs:             map[string]*Run{},
		helperRuns:       map[string]*Run{},
	}
	return e, nil
}

// SetHooks registers manager callbacks. Call before starting runs.
func (e *Engine) SetHooks(h Hooks) { e.hooks = h }

// Config returns the engine configuration.
func (e *Engine) Config() *config.Config { return e.cfg }

// WorkerID returns this process's worker id.
func (e *Engine) WorkerID() string { return e.workerID }

// Proxies returns the proxy manager.
func (e *Engine) Proxies() *netx.ProxyManager { return e.proxies }

// Guard returns the SSRF guard.
func (e *Engine) Guard() *netx.Guard { return e.guard }

// Store returns the object store.
func (e *Engine) Store() storage.Store { return e.store }

// LLM returns the LLM client (nil when not configured).
func (e *Engine) LLM() *content.LLMClient { return e.llm }

// documentOptions bounds PDF/DOCX conversion.
func (e *Engine) documentOptions() content.DocumentOptions {
	return content.DocumentOptions{MaxPages: e.cfg.Documents.MaxPages, Timeout: e.cfg.Documents.Timeout}
}

// BrowserPoolSize returns the number of headless browser slots.
func (e *Engine) BrowserPoolSize() int { return e.browsers.Size() }

// GetSemanticSearcher returns the semantic searcher module.
func (e *Engine) GetSemanticSearcher() *modules.SemanticSearcher { return e.semanticSearcher }

// GetStemmer returns the stemmer module (may be disabled).
func (e *Engine) GetStemmer() *modules.Stemmer { return e.stemmer }

// GetWordExtractor returns the word extractor module.
func (e *Engine) GetWordExtractor() *modules.WordExtractor { return e.wordExtractor }

// StartRun starts crawling a job this worker has already claimed (status
// running, worker_id set). It returns once the run's workers are started.
func (e *Engine) StartRun(job *models.CrawlJob) error {
	e.mu.Lock()
	if _, exists := e.runs[job.ID]; exists {
		e.mu.Unlock()
		return fmt.Errorf("job %s is already running on this worker", job.ID)
	}
	// This worker may have been helping with the job before it took it
	// over — a crawl whose owner died is one any idle worker joins, and
	// then one of them claims. Its own helper run is now redundant, so it
	// is told to wind down; no need to wait for it, since the frontier
	// hands the two of them different URLs either way.
	if helper := e.helperRuns[job.ID]; helper != nil {
		helper.stop(Outcome{StopReason: "took over the crawl"})
	}
	r, err := newRun(e, job)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	e.runs[job.ID] = r
	e.mu.Unlock()

	if err := r.start(); err != nil {
		e.mu.Lock()
		delete(e.runs, job.ID)
		e.mu.Unlock()
		return err
	}
	return nil
}

func (e *Engine) runEnded(r *Run, outcome Outcome) {
	e.mu.Lock()
	delete(e.runs, r.job.ID)
	e.mu.Unlock()
	if e.hooks.RunEnded != nil {
		e.hooks.RunEnded(r.job, outcome)
	}
}

// StartHelping starts pulling from a running job's frontier alongside the
// worker that owns it. The frontier hands every worker different URLs, so
// this simply adds this machine's capacity to that crawl. participantID is
// the models.CrawlJobWorker row registering this worker on the job.
func (e *Engine) StartHelping(job *models.CrawlJob, participantID uint) error {
	e.mu.Lock()
	if _, exists := e.helperRuns[job.ID]; exists {
		e.mu.Unlock()
		return fmt.Errorf("job %s is already being helped by this worker", job.ID)
	}
	if _, owned := e.runs[job.ID]; owned {
		e.mu.Unlock()
		return fmt.Errorf("job %s already runs on this worker", job.ID)
	}
	r, err := newRun(e, job)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	r.isHelper = true
	r.helperRowID = participantID
	e.helperRuns[job.ID] = r
	e.mu.Unlock()

	if err := r.start(); err != nil {
		e.mu.Lock()
		delete(e.helperRuns, job.ID)
		e.mu.Unlock()
		return err
	}
	return nil
}

// liveWorkerIDs lists the workers that have reported in recently. A claim
// held by anyone else has been abandoned and may be handed out again.
func (e *Engine) liveWorkerIDs() []string {
	var ids []string
	if err := e.db.Model(&models.WorkerHeartbeat{}).
		Where("last_seen_at > ?", time.Now().Add(-workerLivenessWindow)).
		Pluck("id", &ids).Error; err != nil {
		log.Printf("[Engine] Failed to list live workers: %v", err)
		return nil
	}
	if !slices.Contains(ids, e.workerID) {
		ids = append(ids, e.workerID) // this process is alive whatever the heartbeat table says
	}
	return ids
}

func (e *Engine) helperRunEnded(r *Run) {
	e.mu.Lock()
	delete(e.helperRuns, r.job.ID)
	e.mu.Unlock()
}

// HelpingCount returns the number of other workers' jobs this process is
// currently helping crawl.
func (e *Engine) HelpingCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.helperRuns)
}

func (e *Engine) emit(job *models.CrawlJob, event string, data map[string]interface{}) {
	if e.hooks.Event != nil {
		e.hooks.Event(job, event, data)
	}
}

// Signal asks a local run to pause or cancel. It reports whether the job is running here.
func (e *Engine) Signal(jobID, control string) bool {
	e.mu.RLock()
	r := e.runs[jobID]
	e.mu.RUnlock()
	if r == nil {
		return false
	}
	switch control {
	case models.ControlPause:
		r.stop(Outcome{Status: models.JobStatusPaused, StopReason: models.StopReasonUser})
	case models.ControlCancel:
		r.stop(Outcome{Status: models.JobStatusCancelled, StopReason: models.StopReasonUser})
	default:
		return false
	}
	return true
}

// IsRunning reports whether jobID runs in this process.
func (e *Engine) IsRunning(jobID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.runs[jobID]
	return ok
}

// IsHelping reports whether this process is currently pulling from jobID's
// frontier as a helper for the worker that owns it.
func (e *Engine) IsHelping(jobID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.helperRuns[jobID]
	return ok
}

// RunningCount returns the number of runs in this process.
func (e *Engine) RunningCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.runs)
}

// RunStats is a live snapshot of a run.
type RunStats struct {
	JobID       string           `json:"job_id"`
	TargetURL   string           `json:"target_url"`
	OwnerRef    string           `json:"owner_ref,omitempty"`
	StartedAt   time.Time        `json:"started_at"`
	Crawled     int64            `json:"crawled"`
	Kept        int64            `json:"kept"` // crawled pages not skipped by keep_tags
	Matches     int64            `json:"matches"`
	InFlight    int64            `json:"in_flight"`
	Helping     bool             `json:"helping"` // this worker is helping with a crawl another worker owns
	Usage       models.JobUsage  `json:"usage"`
	Limits      models.JobLimits `json:"limits"`
	PagesPerMin float64          `json:"pages_per_minute"`
}

// Runs returns snapshots of every run in this process, newest first,
// including crawls this worker only helps with — they are as much work as
// the ones it owns, and a worker that showed none of them would look idle
// while fetching flat out.
func (e *Engine) Runs() []RunStats {
	e.mu.RLock()
	runs := make([]*Run, 0, len(e.runs)+len(e.helperRuns))
	for _, r := range e.runs {
		runs = append(runs, r)
	}
	for _, r := range e.helperRuns {
		runs = append(runs, r)
	}
	e.mu.RUnlock()
	out := make([]RunStats, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.stats())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// RunStatsFor returns the live snapshot of one run.
func (e *Engine) RunStatsFor(jobID string) (RunStats, bool) {
	e.mu.RLock()
	r := e.runs[jobID]
	e.mu.RUnlock()
	if r == nil {
		return RunStats{}, false
	}
	return r.stats(), true
}

// RequeueAll stops every run and hands its job back to the queue without
// waiting for the runs to finish. It returns how many runs it stopped.
func (e *Engine) RequeueAll(reason string) int {
	e.mu.RLock()
	runs := make([]*Run, 0, len(e.runs)+len(e.helperRuns))
	for _, r := range e.runs {
		runs = append(runs, r)
	}
	for _, r := range e.helperRuns {
		runs = append(runs, r)
	}
	e.mu.RUnlock()
	for _, r := range runs {
		r.stop(Outcome{Status: models.JobStatusQueued, StopReason: reason})
	}
	return len(runs)
}

// NeedsBrowser reports whether a job's effective settings use the headless
// browser (always or as a fallback). Invalid settings report false; starting
// the job reports them.
func (e *Engine) NeedsBrowser(settings *models.JobSettings) bool {
	rs, err := BuildRunSettings(e.cfg, settings, nil, e.proxies)
	if err != nil {
		return false
	}
	return rs.UseHeadlessBrowser || rs.HeadlessFallback
}

// Shutdown stops every run and hands its job back to the queue so another
// worker (or this one after a restart) continues it.
func (e *Engine) Shutdown(ctx context.Context) {
	e.mu.RLock()
	runs := make([]*Run, 0, len(e.runs)+len(e.helperRuns))
	for _, r := range e.runs {
		runs = append(runs, r)
	}
	for _, r := range e.helperRuns {
		runs = append(runs, r)
	}
	e.mu.RUnlock()
	for _, r := range runs {
		r.stop(Outcome{Status: models.JobStatusQueued, StopReason: "worker_shutdown"})
	}
	for _, r := range runs {
		select {
		case <-r.finished:
		case <-ctx.Done():
			log.Printf("[Engine] Timed out waiting for job %s to stop", r.job.ID)
		}
	}
	e.browsers.Close()
	e.transports.CloseIdle()
}
