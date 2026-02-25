package crawler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CrawlerState represents the current state of the crawler
type CrawlerState int32

const (
	StateIdle CrawlerState = iota
	StateRunning
	StatePaused
	StateStopping
)

// Engine is the core crawler engine with DB-backed frontier
type Engine struct {
	config  config.CrawlerConfig
	db      *gorm.DB
	fetcher modules.Fetcher

	// Modules
	urlCleaner       *modules.URLCleaner
	linkExtractor    *modules.HTMLLinkExtractor
	phraseDetector   *modules.SimplePhraseDetector
	sitemapParser    *modules.SitemapParser
	robotsParser     *modules.RobotsParser
	frontier         *modules.DBFrontier
	semanticSearcher *modules.SemanticSearcher

	// State management
	state      int32 // atomic
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	currentJob *models.CrawlJob
	jobMu      sync.RWMutex
	rng        *rand.Rand

	// Effective config for current job (merged)
	effectiveConfig config.CrawlerConfig

	// Phrase ID lookup: phrase string -> SearchPhrase.ID
	phraseIDMap map[string]uint

	// robots.txt compliance: disallowed paths per domain
	robotRules   map[string]*modules.RobotsResult
	robotRulesMu sync.RWMutex

	// Stats
	crawledCount int64
	matchCount   int64
}

// newFetcher creates the appropriate Fetcher based on config.
func newFetcher(cfg config.CrawlerConfig) modules.Fetcher {
	if cfg.UseHeadlessBrowser {
		return modules.NewHeadlessFetcher(cfg)
	}
	return modules.NewHTTPFetcher(cfg)
}

// NewEngine creates a new crawler engine
func NewEngine(cfg config.CrawlerConfig, db *gorm.DB) *Engine {
	// Initialize modules
	urlCleaner := modules.NewURLCleaner()
	urlCleaner.Initialize()

	linkExtractor := modules.NewHTMLLinkExtractor(urlCleaner)
	linkExtractor.Initialize()

	phraseDetector := modules.NewSimplePhraseDetector()
	phraseDetector.Initialize()

	sitemapParser := modules.NewSitemapParser(urlCleaner)
	sitemapParser.Initialize()

	robotsParser := modules.NewRobotsParser(urlCleaner)
	robotsParser.Initialize()

	// Create DB-backed frontier
	frontier := modules.NewDBFrontier(db, urlCleaner, cfg.TeleportProbability)
	frontier.Initialize()
	frontier.SetSkipExtensions(cfg.SkipExtensions)

	// Create the appropriate fetcher (HTTP or headless)
	fetcher := newFetcher(cfg)

	// Create semantic searcher
	semanticSearcher := modules.NewSemanticSearcher(cfg, db)

	return &Engine{
		config:           cfg,
		db:               db,
		fetcher:          fetcher,
		urlCleaner:       urlCleaner,
		linkExtractor:    linkExtractor,
		phraseDetector:   phraseDetector,
		sitemapParser:    sitemapParser,
		robotsParser:     robotsParser,
		frontier:         frontier,
		semanticSearcher: semanticSearcher,
		rng:              rand.New(rand.NewSource(time.Now().UnixNano())),
		phraseIDMap:      make(map[string]uint),
		robotRules:       make(map[string]*modules.RobotsResult),
	}
}

// LoadPhrases loads search phrases from database
func (e *Engine) LoadPhrases() error {
	var phrases []models.SearchPhrase
	if err := e.db.Where("is_active = ?", true).Find(&phrases).Error; err != nil {
		return err
	}

	// Clear and rebuild
	e.phraseIDMap = make(map[string]uint)
	for _, p := range phrases {
		e.phraseDetector.AddPhrase(p.Phrase)
		e.phraseIDMap[p.Phrase] = p.ID
	}

	log.Printf("[Engine] Loaded %d search phrases", len(phrases))
	return nil
}

// Start begins crawling for a job
func (e *Engine) Start(job *models.CrawlJob) error {
	if !atomic.CompareAndSwapInt32(&e.state, int32(StateIdle), int32(StateRunning)) {
		return fmt.Errorf("crawler is already running")
	}

	e.jobMu.Lock()
	e.currentJob = job
	e.jobMu.Unlock()

	// Merge job settings with default config
	e.effectiveConfig = e.config
	if job.Settings != nil {
		e.mergeJobSettings(job.Settings)
	}

	// Recreate fetcher with effective config so per-job overrides take effect
	e.fetcher = newFetcher(e.effectiveConfig)

	// Reset robots.txt rules for new job
	e.robotRulesMu.Lock()
	e.robotRules = make(map[string]*modules.RobotsResult)
	e.robotRulesMu.Unlock()

	// Apply URL filters from job settings
	if job.Settings != nil {
		e.frontier.SetURLFilters(job.Settings.URLIncludePatterns, job.Settings.URLExcludePatterns)
		// Apply any extra tracking params to the URL cleaner
		if len(job.Settings.ExtraTrackingParams) > 0 {
			e.urlCleaner.AddTrackingParams(job.Settings.ExtraTrackingParams)
		}
	} else {
		e.frontier.SetURLFilters(nil, nil)
	}

	// Apply skip extensions from effective config
	e.frontier.SetSkipExtensions(e.effectiveConfig.SkipExtensions)

	e.ctx, e.cancel = context.WithCancel(context.Background())
	atomic.StoreInt64(&e.crawledCount, 0)
	atomic.StoreInt64(&e.matchCount, 0)

	var crawledCount int64
	e.db.Model(&models.CrawledPage{}).Where("crawl_job_id = ? AND is_archived = ?", job.ID, false).Count(&crawledCount)
	atomic.StoreInt64(&e.crawledCount, crawledCount)

	var matchCount int64
	e.db.Model(&models.PhraseMatch{}).Where("crawl_job_id = ? AND is_archived = ?", job.ID, false).Count(&matchCount)
	atomic.StoreInt64(&e.matchCount, matchCount)

	// Set the crawl job in frontier
	e.frontier.SetCrawlJob(job.ID)

	// Reset any stuck processing URLs
	e.frontier.ResetProcessingURLs()

	// Update job status
	now := time.Now()
	job.Status = models.JobStatusRunning
	job.StartedAt = &now
	e.db.Save(job)

	// Check if frontier already has URLs (resuming) or needs seeding
	if e.frontier.PendingCount() == 0 {
		// Add seed URLs
		if err := e.addSeedURLs(job); err != nil {
			log.Printf("[Engine] Error adding seed URLs: %v", err)
		}
	} else {
		log.Printf("[Engine] Resuming with %d pending URLs in frontier", e.frontier.PendingCount())
	}

	// Start worker goroutines
	workerCount := e.effectiveConfig.MaxConcurrentRequests
	e.wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go e.worker(i)
	}

	// Start monitoring goroutine
	go e.monitor()

	// Start completion watcher
	go e.watchForCompletion()

	log.Printf("[Engine] Started crawling job %s for target %s", job.ID, job.TargetURL)
	return nil
}

// mergeJobSettings merges per-job settings into the effective config
func (e *Engine) mergeJobSettings(s *models.JobSettings) {
	if s.MaxConcurrentRequests != nil {
		e.effectiveConfig.MaxConcurrentRequests = *s.MaxConcurrentRequests
	}
	if s.RequestTimeoutSec != nil {
		e.effectiveConfig.RequestTimeout = time.Duration(*s.RequestTimeoutSec) * time.Second
	}
	if s.PolitenessDelayMs != nil {
		e.effectiveConfig.PolitenessDelay = time.Duration(*s.PolitenessDelayMs) * time.Millisecond
	}
	if s.MaxDepth != nil {
		e.effectiveConfig.MaxDepth = *s.MaxDepth
	}
	if s.MaxPages != nil {
		e.effectiveConfig.MaxPages = *s.MaxPages
	}
	if s.UserAgent != nil {
		e.effectiveConfig.UserAgent = *s.UserAgent
	}
	if s.MaxRetries != nil {
		e.effectiveConfig.MaxRetries = *s.MaxRetries
	}
	if s.RespectRobotsTxt != nil {
		e.effectiveConfig.RespectRobotsTxt = *s.RespectRobotsTxt
	}
	if len(s.SkipExtensions) > 0 {
		e.effectiveConfig.SkipExtensions = s.SkipExtensions
	}
	if s.SkipContentDuplicates != nil {
		e.effectiveConfig.SkipContentDuplicates = *s.SkipContentDuplicates
	}
	if s.UseHeadlessBrowser != nil {
		e.effectiveConfig.UseHeadlessBrowser = *s.UseHeadlessBrowser
	}
	if s.HeadlessWaitSelector != nil {
		e.effectiveConfig.HeadlessWaitSelector = *s.HeadlessWaitSelector
	}
	if s.EnableSemanticSearch != nil {
		e.effectiveConfig.EnableSemanticSearch = *s.EnableSemanticSearch
	}
	if s.SaveTextContent != nil {
		e.effectiveConfig.SaveTextContent = *s.SaveTextContent
	}
}

// randomizedDelay returns a randomized delay based on the politeness delay.
// It varies from (delay - 1s) to (delay + 1s) in milliseconds, minimum 0.
// Uses sync-safe approach to avoid race conditions on rng.
func (e *Engine) randomizedDelay() time.Duration {
	baseMs := e.effectiveConfig.PolitenessDelay.Milliseconds()
	minMs := baseMs - 1000
	if minMs < 0 {
		minMs = 0
	}
	maxMs := baseMs + 1000
	if maxMs <= minMs {
		return time.Duration(minMs) * time.Millisecond
	}
	// Use jobMu to synchronize access to rng (it's not heavily contended here)
	e.jobMu.RLock()
	delayMs := minMs + int64(e.rng.Intn(int(maxMs-minMs+1)))
	e.jobMu.RUnlock()
	return time.Duration(delayMs) * time.Millisecond
}

// addSeedURLs adds initial seed URLs for the job
func (e *Engine) addSeedURLs(job *models.CrawlJob) error {
	// Add the target URL and its basic resources
	e.frontier.AddSeedURLs(job.TargetURL)

	log.Printf("[Engine] Added seed URLs for %s, frontier size: %d", job.TargetURL, e.frontier.PendingCount())
	return nil
}

// Stop stops the crawler
func (e *Engine) Stop() {
	if atomic.LoadInt32(&e.state) == int32(StateIdle) {
		return
	}

	atomic.StoreInt32(&e.state, int32(StateStopping))
	if e.cancel != nil {
		e.cancel()
	}

	e.wg.Wait()

	// Check if semantic search was enabled before clearing the job
	shouldRebuildIndex := e.effectiveConfig.EnableSemanticSearch

	// Update job status
	e.jobMu.Lock()
	if e.currentJob != nil {
		now := time.Now()
		e.currentJob.Status = models.JobStatusCancelled
		e.currentJob.CompletedAt = &now
		e.currentJob.CrawledURLs = int(atomic.LoadInt64(&e.crawledCount))
		e.currentJob.FoundMatches = int(atomic.LoadInt64(&e.matchCount))
		e.db.Save(e.currentJob)
		e.currentJob = nil
	}
	e.jobMu.Unlock()

	// Rebuild FAISS index if semantic search was enabled (index pages crawled before cancellation)
	if shouldRebuildIndex {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if err := e.semanticSearcher.RebuildIndex(ctx); err != nil {
				log.Printf("[Engine] Failed to rebuild FAISS index after stop: %v", err)
			}
		}()
	}

	atomic.StoreInt32(&e.state, int32(StateIdle))
	log.Printf("[Engine] Stopped")
}

// Pause pauses the crawler
func (e *Engine) Pause() {
	atomic.StoreInt32(&e.state, int32(StatePaused))

	e.jobMu.Lock()
	if e.currentJob != nil {
		e.currentJob.Status = models.JobStatusPaused
		e.db.Save(e.currentJob)
	}
	e.jobMu.Unlock()

	log.Printf("[Engine] Paused")
}

// Resume resumes the crawler
func (e *Engine) Resume() {
	if atomic.CompareAndSwapInt32(&e.state, int32(StatePaused), int32(StateRunning)) {
		// Reset any URLs stuck in "processing" state back to pending.
		// Workers that had grabbed URLs before the pause may have already
		// finished, but others could still be in flight.
		e.frontier.ResetProcessingURLs()

		e.jobMu.Lock()
		if e.currentJob != nil {
			e.currentJob.Status = models.JobStatusRunning
			e.db.Save(e.currentJob)
		}
		e.jobMu.Unlock()
		log.Printf("[Engine] Resumed")
	}
}

// GetState returns the current crawler state
func (e *Engine) GetState() CrawlerState {
	return CrawlerState(atomic.LoadInt32(&e.state))
}

// watchForCompletion waits for all workers to finish and marks the job as complete
func (e *Engine) watchForCompletion() {
	// Wait for all workers to finish
	e.wg.Wait()

	currentState := atomic.LoadInt32(&e.state)

	// Check if we were stopped manually (state would be StateStopping or StateIdle)
	// If state is still Running, workers exited naturally due to empty frontier
	if currentState == int32(StateRunning) {
		log.Printf("[Engine] All workers finished, marking job as completed")

		// Cancel the context to stop the monitor goroutine
		if e.cancel != nil {
			e.cancel()
		}

		// Update job status to completed
		e.jobMu.Lock()
		if e.currentJob != nil {
			now := time.Now()
			e.currentJob.Status = models.JobStatusCompleted
			e.currentJob.CompletedAt = &now
			e.currentJob.CrawledURLs = int(atomic.LoadInt64(&e.crawledCount))
			e.currentJob.FoundMatches = int(atomic.LoadInt64(&e.matchCount))
			e.db.Save(e.currentJob)
			log.Printf("[Engine] Job %s completed. Crawled: %d, Matches: %d",
				e.currentJob.ID, e.currentJob.CrawledURLs, e.currentJob.FoundMatches)
			e.currentJob = nil
		}
		e.jobMu.Unlock()

		// Rebuild FAISS index if semantic search was enabled
		if e.effectiveConfig.EnableSemanticSearch {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				if err := e.semanticSearcher.RebuildIndex(ctx); err != nil {
					log.Printf("[Engine] Failed to rebuild FAISS index: %v", err)
				}
			}()
		}

		atomic.StoreInt32(&e.state, int32(StateIdle))
	}
}

// worker is a crawl worker goroutine
func (e *Engine) worker(id int) {
	defer e.wg.Done()

	emptyCount := 0
	maxEmptyCount := 30 // Stop after 30 empty checks (about 15 seconds)

	for {
		select {
		case <-e.ctx.Done():
			log.Printf("[Worker %d] Stopping", id)
			return
		default:
		}

		// Check if paused — reset emptyCount so workers don't exit
		// while the crawler is intentionally idle.
		if atomic.LoadInt32(&e.state) == int32(StatePaused) {
			emptyCount = 0
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Get next URL from DB frontier
		frontierURL, err := e.frontier.GetNextURL()
		if err != nil {
			emptyCount++
			if emptyCount >= maxEmptyCount {
				log.Printf("[Worker %d] Frontier empty for too long, stopping (emptyCount=%d)", id, emptyCount)
				return
			}
			if emptyCount%5 == 1 {
				log.Printf("[Worker %d] Frontier empty, waiting... (emptyCount=%d, err=%v)", id, emptyCount, err)
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		emptyCount = 0

		// Check depth limit
		if frontierURL.Depth > e.effectiveConfig.MaxDepth {
			e.frontier.MarkCompleted(frontierURL.ID)
			continue
		}

		// Check robots.txt compliance before crawling
		if e.effectiveConfig.RespectRobotsTxt && e.isDisallowedByRobots(frontierURL.URL) {
			log.Printf("[Worker %d] Skipping URL disallowed by robots.txt: %s", id, frontierURL.URL)
			e.frontier.MarkCompleted(frontierURL.ID)
			continue
		}

		// Check max pages limit
		if e.effectiveConfig.MaxPages > 0 && atomic.LoadInt64(&e.crawledCount) >= int64(e.effectiveConfig.MaxPages) {
			log.Printf("[Worker %d] Max pages limit (%d) reached, stopping", id, e.effectiveConfig.MaxPages)
			e.frontier.MarkCompleted(frontierURL.ID)
			return
		}

		// Crawl the URL
		e.crawl(id, frontierURL)

		// Randomized politeness delay
		delay := e.randomizedDelay()
		if delay > 0 {
			time.Sleep(delay)
		}
	}
}

// crawl fetches and processes a single URL
func (e *Engine) crawl(workerID int, frontierURL *models.FrontierURL) {
	startTime := time.Now()
	url := frontierURL.URL
	depth := frontierURL.Depth

	log.Printf("[Worker %d] >>> CRAWLING url=%s depth=%d", workerID, url, depth)

	// Fetch using the configured fetcher (HTTP or headless)
	result := e.fetcher.Fetch(e.ctx, url)
	if result.Error != nil {
		log.Printf("[Worker %d] Failed to fetch %s: %v", workerID, url, result.Error)
		_, _ = e.saveCrawledPage(url, result.StatusCode, result.ContentType, 0, depth, result.Error.Error(), startTime, result.Body)
		e.frontier.MarkFailed(frontierURL.ID, frontierURL.RetryCount, e.effectiveConfig.MaxRetries)
		return
	}

	log.Printf("[Worker %d] <<< RESPONSE url=%s status=%d content-type=%q size=%d",
		workerID, url, result.StatusCode, result.ContentType, len(result.Body))

	// Treat server errors (502, 503, 429) as retriable — the server may be
	// temporarily overloaded or issuing a challenge that failed.
	if result.StatusCode == 429 || result.StatusCode == 502 || result.StatusCode == 503 {
		log.Printf("[Worker %d] Retriable status %d for %s (retry %d/%d)",
			workerID, result.StatusCode, url, frontierURL.RetryCount, e.effectiveConfig.MaxRetries)
		_, _ = e.saveCrawledPage(url, result.StatusCode, result.ContentType, int64(len(result.Body)), depth,
			fmt.Sprintf("HTTP %d", result.StatusCode), startTime, result.Body)
		e.frontier.MarkFailed(frontierURL.ID, frontierURL.RetryCount, e.effectiveConfig.MaxRetries)
		return
	}

	contentType := result.ContentType

	// Content-Type pre-check: skip binary content early
	if !isProcessableContentType(contentType) {
		log.Printf("[Worker %d] Skipping non-processable content type %q for %s", workerID, contentType, url)
		_, _ = e.saveCrawledPage(url, result.StatusCode, contentType, 0, depth, "", startTime, nil)
		e.frontier.MarkCompleted(frontierURL.ID)
		return
	}

	body := result.Body
	log.Printf("[Worker %d] BODY url=%s size=%d bytes, first100=%q", workerID, url, len(body), string(body[:min(100, len(body))]))

	// Save crawled page (with title extraction for HTML)
	page, created := e.saveCrawledPage(url, result.StatusCode, contentType, int64(len(body)), depth, "", startTime, body)
	if created {
		atomic.AddInt64(&e.crawledCount, 1)
	}

	// Mark as completed in frontier
	e.frontier.MarkCompleted(frontierURL.ID)

	// Process content based on type
	e.processContent(workerID, url, body, contentType, depth, page, frontierURL.AnchorText)
}

// processContent processes fetched content
func (e *Engine) processContent(workerID int, url string, body []byte, contentType string, depth int, page *models.CrawledPage, anchorText string) {
	// 1. Check for phrase matches in page content
	textContent := e.linkExtractor.ExtractTextContent(body)
	matches := e.phraseDetector.DetectPhrases(textContent)

	for _, match := range matches {
		e.savePhraseMatch(page.ID, url, match.Phrase, match.Context, match.Occurrences, models.MatchTypeContent)
		atomic.AddInt64(&e.matchCount, 1)
		log.Printf("[Worker %d] Found phrase '%s' in content of %s (%d occurrences)",
			workerID, match.Phrase, url, match.Occurrences)
	}

	// 2. Check for phrase matches in URL
	urlMatches := e.phraseDetector.DetectPhrasesInURL(url)
	for _, match := range urlMatches {
		e.savePhraseMatch(page.ID, url, match.Phrase, match.Context, match.Occurrences, models.MatchTypeURL)
		atomic.AddInt64(&e.matchCount, 1)
		log.Printf("[Worker %d] Found phrase '%s' in URL %s (%d occurrences)",
			workerID, match.Phrase, url, match.Occurrences)
	}

	// 3. Check for phrase matches in anchor text pointing to this page
	if anchorText != "" {
		anchorMatches := e.phraseDetector.DetectPhrasesInAnchor(anchorText)
		for _, match := range anchorMatches {
			e.savePhraseMatch(page.ID, url, match.Phrase, "Anchor: "+match.Context, match.Occurrences, models.MatchTypeAnchor)
			atomic.AddInt64(&e.matchCount, 1)
			log.Printf("[Worker %d] Found phrase '%s' in anchor text for %s (%d occurrences)",
				workerID, match.Phrase, url, match.Occurrences)
		}
	}

	// Process based on content type
	if strings.Contains(contentType, "text/html") {
		e.processHTML(url, body, depth)

		// 4. Generate semantic embedding if enabled
		if e.effectiveConfig.EnableSemanticSearch && page != nil && page.ID > 0 {
			go func(p *models.CrawledPage, text string) {
				if err := e.semanticSearcher.EmbedAndStore(e.ctx, p, text); err != nil {
					log.Printf("[SemanticSearch] Failed to embed page %d (%s): %v", p.ID, p.URL, err)
				}
			}(page, textContent)
		}
	} else if strings.Contains(url, "sitemap") && strings.Contains(contentType, "xml") {
		e.processSitemap(url, body, depth)
	} else if strings.Contains(url, "robots.txt") {
		e.processRobots(url, body, depth)
	}
}

// processHTML processes HTML content and extracts links
func (e *Engine) processHTML(url string, body []byte, depth int) {
	linksWithAnchors, err := e.linkExtractor.ExtractLinksWithAnchors(url, body)
	if err != nil {
		log.Printf("[Engine] Failed to extract links from %s: %v", url, err)
		return
	}

	log.Printf("[Engine] ExtractLinksWithAnchors returned %d raw links from %s", len(linksWithAnchors), url)

	// Get base domain for the current job
	e.jobMu.RLock()
	baseDomain := ""
	if e.currentJob != nil {
		baseDomain = e.currentJob.Domain
	}
	e.jobMu.RUnlock()

	log.Printf("[Engine] Job baseDomain=%q for filtering links", baseDomain)

	// Filter and add links
	added := 0
	skippedDomain := 0
	skippedOther := 0
	for _, la := range linksWithAnchors {
		// Only add links from the same base domain
		linkDomain := e.urlCleaner.ExtractBaseDomain(la.URL)
		if linkDomain == "" || linkDomain != baseDomain {
			skippedDomain++
			if skippedDomain <= 5 {
				log.Printf("[Engine] SKIP domain mismatch: linkDomain=%q baseDomain=%q url=%s", linkDomain, baseDomain, la.URL)
			}
			continue
		}

		if err := e.frontier.AddURLWithAnchor(la.URL, depth+1, url, la.AnchorText); err == nil {
			added++
		} else {
			skippedOther++
			if skippedOther <= 5 {
				log.Printf("[Engine] SKIP frontier rejected: err=%v url=%s", err, la.URL)
			}
		}
	}

	log.Printf("[Engine] Added %d/%d links from %s (skippedDomain=%d, skippedOther=%d)", added, len(linksWithAnchors), url, skippedDomain, skippedOther)
}

// processSitemap processes sitemap XML
func (e *Engine) processSitemap(url string, body []byte, depth int) {
	urls, sitemaps, err := e.sitemapParser.ParseSitemap(body)
	if err != nil {
		log.Printf("[Engine] Failed to parse sitemap %s: %v", url, err)
		return
	}

	// Add discovered URLs
	for _, u := range urls {
		e.frontier.AddURL(u, depth+1, url)
	}

	// Add nested sitemaps
	for _, s := range sitemaps {
		e.frontier.AddURL(s, depth+1, url)
	}

	log.Printf("[Engine] Parsed sitemap %s: %d URLs, %d nested sitemaps", url, len(urls), len(sitemaps))
}

// processRobots processes robots.txt and stores rules for compliance
func (e *Engine) processRobots(url string, body []byte, depth int) {
	result := e.robotsParser.ParseRobots(url, body)

	// Store robots rules for this domain
	domain := e.urlCleaner.ExtractDomain(url)
	if domain != "" {
		e.robotRulesMu.Lock()
		e.robotRules[domain] = result
		e.robotRulesMu.Unlock()
		log.Printf("[Engine] Stored robots.txt rules for %s: %d disallow, %d allow paths",
			domain, len(result.DisallowedPaths), len(result.AllowedPaths))
	}

	// Add sitemaps from robots.txt
	for _, sitemap := range result.Sitemaps {
		e.frontier.AddURL(sitemap, depth+1, url)
	}

	log.Printf("[Engine] Parsed robots.txt %s: %d sitemaps", url, len(result.Sitemaps))
}

// isDisallowedByRobots checks whether a URL is disallowed by the stored robots.txt rules.
// Implements path-prefix matching: an Allow directive takes precedence if its path is
// longer (more specific) than the matching Disallow directive.
func (e *Engine) isDisallowedByRobots(rawURL string) bool {
	domain := e.urlCleaner.ExtractDomain(rawURL)
	if domain == "" {
		return false
	}

	e.robotRulesMu.RLock()
	rules, exists := e.robotRules[domain]
	e.robotRulesMu.RUnlock()

	if !exists || rules == nil {
		return false // No rules loaded yet, allow by default
	}

	// Extract path from URL
	urlPath := extractPathFromURL(rawURL)
	if urlPath == "" {
		urlPath = "/"
	}

	// Find the longest matching disallow path
	longestDisallow := ""
	for _, path := range rules.DisallowedPaths {
		if path == "" {
			continue
		}
		if strings.HasPrefix(urlPath, path) && len(path) > len(longestDisallow) {
			longestDisallow = path
		}
	}

	if longestDisallow == "" {
		return false // Not disallowed
	}

	// Check if there's a more specific Allow that overrides
	for _, path := range rules.AllowedPaths {
		if path == "" {
			continue
		}
		if strings.HasPrefix(urlPath, path) && len(path) > len(longestDisallow) {
			return false // Allow overrides
		}
	}

	return true // Disallowed
}

// isProcessableContentType checks if a Content-Type header indicates content
// worth reading and processing (HTML, XML, plain text, JSON).
func isProcessableContentType(contentType string) bool {
	if contentType == "" {
		return true // Unknown content type, try to process
	}
	ct := strings.ToLower(contentType)
	processable := []string{
		"text/html",
		"application/xhtml+xml",
		"application/xml",
		"text/xml",
		"text/plain",
		"application/json",
		"application/rss+xml",
		"application/atom+xml",
	}
	for _, p := range processable {
		if strings.Contains(ct, p) {
			return true
		}
	}
	return false
}

// extractPathFromURL extracts just the path component from a URL string
func extractPathFromURL(rawURL string) string {
	// Find the path after the host
	idx := strings.Index(rawURL, "://")
	if idx == -1 {
		return "/"
	}
	rest := rawURL[idx+3:]
	slashIdx := strings.Index(rest, "/")
	if slashIdx == -1 {
		return "/"
	}
	path := rest[slashIdx:]
	// Remove query string
	if qIdx := strings.Index(path, "?"); qIdx != -1 {
		path = path[:qIdx]
	}
	// Remove fragment
	if fIdx := strings.Index(path, "#"); fIdx != -1 {
		path = path[:fIdx]
	}
	return path
}

// saveCrawledPage saves a crawled page to database
func (e *Engine) saveCrawledPage(url string, statusCode int, contentType string, contentLength int64, depth int, errorMsg string, startTime time.Time, body []byte) (*models.CrawledPage, bool) {
	// Use fragment-preserving normalization for SPA URLs
	var normalizedURL string
	if modules.HasMeaningfulFragment(url) {
		normalizedURL, _ = e.urlCleaner.ProcessURLKeepFragment(url)
	} else {
		normalizedURL, _ = e.urlCleaner.ProcessURL(url)
	}
	urlHash := e.urlCleaner.HashURL(normalizedURL)

	// Compute document content hash
	docHash := ""
	if len(body) > 0 {
		h := sha256.Sum256(body)
		docHash = hex.EncodeToString(h[:])
	}

	// Detect unrendered SPA shells: when headless mode is active, pages that
	// have an ng-app attribute but no ng-scope class are AngularJS apps where
	// the framework never bootstrapped (CDN challenge, timeout, etc.).  These
	// all hash to the same value.  Treat them as empty so content-dedup does
	// not discard real pages later on.
	isSPAShell := false
	if e.effectiveConfig.UseHeadlessBrowser && len(body) > 0 {
		bodyStr := string(body)
		if strings.Contains(bodyStr, "ng-app=") && !strings.Contains(bodyStr, "ng-scope") {
			isSPAShell = true
		}
	}

	e.jobMu.RLock()
	crawlJobID := ""
	if e.currentJob != nil {
		crawlJobID = e.currentJob.ID
	}
	e.jobMu.RUnlock()

	// If we have a doc hash, check if this exact content already exists for this job.
	// Skip the dedup check for detected SPA shells – they all share the same
	// server-side HTML shell and should not block unique rendered pages.
	if docHash != "" && crawlJobID != "" && e.effectiveConfig.SkipContentDuplicates && !isSPAShell {
		var dupCount int64
		e.db.Model(&models.CrawledPage{}).
			Where("crawl_job_id = ? AND doc_hash = ? AND is_archived = ?", crawlJobID, docHash, false).
			Count(&dupCount)
		if dupCount > 0 {
			log.Printf("[Engine] Skipping duplicate content (doc_hash=%s) for URL %s", docHash[:12], url)
			// Still return a page reference so the caller can proceed
			var existing models.CrawledPage
			if err := e.db.Where("crawl_job_id = ? AND doc_hash = ? AND is_archived = ?", crawlJobID, docHash, false).First(&existing).Error; err == nil {
				return &existing, false
			}
		}
	}

	page := &models.CrawledPage{
		CrawlJobID:    crawlJobID,
		URL:           url,
		URLHash:       urlHash,
		DocHash:       docHash,
		NormalizedURL: normalizedURL,
		StatusCode:    statusCode,
		ContentType:   contentType,
		ContentLength: contentLength,
		Depth:         depth,
		CrawledAt:     time.Now(),
		ResponseTime:  time.Since(startTime).Milliseconds(),
		ErrorMessage:  errorMsg,
	}

	// Extract title and optionally save raw fetched content
	if len(body) > 0 && strings.Contains(contentType, "text/html") {
		if title := e.linkExtractor.ExtractTitle(body); title != "" {
			page.Title = title
		}
	}
	if e.effectiveConfig.SaveTextContent && len(body) > 0 && strings.Contains(contentType, "text/") {
		raw := string(body)
		page.TextContent = &raw
	}

	result := e.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "crawl_job_id"}, {Name: "url_hash"}},
		DoNothing: true,
	}).Create(page)

	if result.Error != nil {
		return page, false
	}

	if result.RowsAffected == 0 {
		// Row already exists — update it if the new data is better
		// (larger content or successful fetch replacing an error).
		var existing models.CrawledPage
		if err := e.db.Where("crawl_job_id = ? AND url_hash = ?", crawlJobID, urlHash).First(&existing).Error; err == nil {
			shouldUpdate := false

			// Prefer larger content (real page vs CDN error / empty shell)
			if contentLength > existing.ContentLength {
				shouldUpdate = true
			}
			// Prefer successful fetch over error
			if errorMsg == "" && existing.ErrorMessage != "" {
				shouldUpdate = true
			}

			if shouldUpdate {
				updates := map[string]interface{}{
					"status_code":    statusCode,
					"content_type":   contentType,
					"content_length": contentLength,
					"doc_hash":       docHash,
					"response_time":  page.ResponseTime,
					"error_message":  errorMsg,
					"crawled_at":     page.CrawledAt,
				}
				if page.Title != "" {
					updates["title"] = page.Title
				}
				if page.TextContent != nil {
					updates["text_content"] = *page.TextContent
				}
				e.db.Model(&existing).Updates(updates)
				log.Printf("[Engine] Updated crawled page %d for %s (content_length %d -> %d)",
					existing.ID, url, existing.ContentLength, contentLength)
			}
			return &existing, false
		}
		return page, false
	}

	return page, true
}

// savePhraseMatch saves a phrase match to database
func (e *Engine) savePhraseMatch(pageID uint, url, phrase, context string, occurrences int, matchType models.MatchType) {
	e.jobMu.RLock()
	crawlJobID := ""
	if e.currentJob != nil {
		crawlJobID = e.currentJob.ID
	}
	e.jobMu.RUnlock()

	// Resolve search phrase ID
	var searchPhraseID *uint
	if id, ok := e.phraseIDMap[phrase]; ok {
		searchPhraseID = &id
	}

	match := &models.PhraseMatch{
		CrawlJobID:     crawlJobID,
		PageID:         pageID,
		SearchPhraseID: searchPhraseID,
		URL:            url,
		Phrase:         phrase,
		MatchType:      matchType,
		Context:        context,
		Occurrences:    occurrences,
		FoundAt:        time.Now(),
	}

	e.db.Create(match)
}

// monitor monitors crawl progress
func (e *Engine) monitor() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			crawled := atomic.LoadInt64(&e.crawledCount)
			matches := atomic.LoadInt64(&e.matchCount)
			pending, processing, completed, failed := e.frontier.GetStats()

			log.Printf("[Monitor] Crawled: %d, Matches: %d, Frontier: pending=%d, processing=%d, completed=%d, failed=%d",
				crawled, matches, pending, processing, completed, failed)

			// Update job stats in database
			e.jobMu.Lock()
			if e.currentJob != nil {
				e.currentJob.CrawledURLs = int(crawled)
				e.currentJob.FoundMatches = int(matches)
				e.currentJob.TotalURLs = int(pending + processing + completed + failed)
				e.db.Save(e.currentJob)
			}
			e.jobMu.Unlock()
		}
	}
}

// GetStats returns current crawl statistics
func (e *Engine) GetStats() map[string]interface{} {
	pending, processing, completed, failed := e.frontier.GetStats()
	return map[string]interface{}{
		"crawled":       atomic.LoadInt64(&e.crawledCount),
		"matches":       atomic.LoadInt64(&e.matchCount),
		"frontier_size": pending,
		"processing":    processing,
		"completed":     completed,
		"failed":        failed,
		"total_known":   pending + processing + completed + failed,
		"state":         e.GetState(),
	}
}

// GetCurrentJob returns the current job being crawled
func (e *Engine) GetCurrentJob() *models.CrawlJob {
	e.jobMu.RLock()
	defer e.jobMu.RUnlock()
	return e.currentJob
}

// GetSemanticSearcher returns the semantic searcher module
func (e *Engine) GetSemanticSearcher() *modules.SemanticSearcher {
	return e.semanticSearcher
}
