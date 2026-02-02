package crawler

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"gorm.io/gorm"
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
	config     config.CrawlerConfig
	db         *gorm.DB
	httpClient *http.Client

	// Modules
	urlCleaner     *modules.URLCleaner
	linkExtractor  *modules.HTMLLinkExtractor
	phraseDetector *modules.SimplePhraseDetector
	sitemapParser  *modules.SitemapParser
	robotsParser   *modules.RobotsParser
	frontier       *modules.DBFrontier

	// State management
	state      int32 // atomic
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	currentJob *models.CrawlJob
	jobMu      sync.RWMutex

	// Stats
	crawledCount int64
	matchCount   int64
}

// NewEngine creates a new crawler engine
func NewEngine(cfg config.CrawlerConfig, db *gorm.DB) *Engine {
	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: cfg.RequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

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

	return &Engine{
		config:         cfg,
		db:             db,
		httpClient:     client,
		urlCleaner:     urlCleaner,
		linkExtractor:  linkExtractor,
		phraseDetector: phraseDetector,
		sitemapParser:  sitemapParser,
		robotsParser:   robotsParser,
		frontier:       frontier,
	}
}

// LoadPhrases loads search phrases from database
func (e *Engine) LoadPhrases() error {
	var phrases []models.SearchPhrase
	if err := e.db.Where("is_active = ?", true).Find(&phrases).Error; err != nil {
		return err
	}

	for _, p := range phrases {
		e.phraseDetector.AddPhrase(p.Phrase)
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

	e.ctx, e.cancel = context.WithCancel(context.Background())
	atomic.StoreInt64(&e.crawledCount, 0)
	atomic.StoreInt64(&e.matchCount, 0)

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
	e.wg.Add(e.config.MaxConcurrentRequests)
	for i := 0; i < e.config.MaxConcurrentRequests; i++ {
		go e.worker(i)
	}

	// Start monitoring goroutine
	go e.monitor()

	log.Printf("[Engine] Started crawling job %s for target %s", job.ID, job.TargetURL)
	return nil
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

	// Update job status
	e.jobMu.Lock()
	if e.currentJob != nil {
		now := time.Now()
		e.currentJob.Status = models.JobStatusCompleted
		e.currentJob.CompletedAt = &now
		e.currentJob.CrawledURLs = int(atomic.LoadInt64(&e.crawledCount))
		e.currentJob.FoundMatches = int(atomic.LoadInt64(&e.matchCount))
		e.db.Save(e.currentJob)
		e.currentJob = nil
	}
	e.jobMu.Unlock()

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

		// Check if paused
		if atomic.LoadInt32(&e.state) == int32(StatePaused) {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Get next URL from DB frontier
		frontierURL, err := e.frontier.GetNextURL()
		if err != nil {
			emptyCount++
			if emptyCount >= maxEmptyCount {
				log.Printf("[Worker %d] Frontier empty for too long, stopping", id)
				return
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		emptyCount = 0

		// Check depth limit
		if frontierURL.Depth > e.config.MaxDepth {
			e.frontier.MarkCompleted(frontierURL.ID)
			continue
		}

		// Crawl the URL
		e.crawl(id, frontierURL)

		// Politeness delay
		time.Sleep(e.config.PolitenessDelay)
	}
}

// crawl fetches and processes a single URL
func (e *Engine) crawl(workerID int, frontierURL *models.FrontierURL) {
	startTime := time.Now()
	url := frontierURL.URL
	depth := frontierURL.Depth

	// Create request
	req, err := http.NewRequestWithContext(e.ctx, "GET", url, nil)
	if err != nil {
		log.Printf("[Worker %d] Failed to create request for %s: %v", workerID, url, err)
		e.frontier.MarkFailed(frontierURL.ID, frontierURL.RetryCount, 3)
		return
	}

	req.Header.Set("User-Agent", e.config.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	// Execute request
	resp, err := e.httpClient.Do(req)
	if err != nil {
		log.Printf("[Worker %d] Failed to fetch %s: %v", workerID, url, err)
		e.saveCrawledPage(url, 0, "", 0, depth, err.Error(), startTime)
		e.frontier.MarkFailed(frontierURL.ID, frontierURL.RetryCount, 3)
		return
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		log.Printf("[Worker %d] Failed to read body for %s: %v", workerID, url, err)
		e.saveCrawledPage(url, resp.StatusCode, resp.Header.Get("Content-Type"), 0, depth, err.Error(), startTime)
		e.frontier.MarkFailed(frontierURL.ID, frontierURL.RetryCount, 3)
		return
	}

	contentType := resp.Header.Get("Content-Type")

	// Save crawled page
	page := e.saveCrawledPage(url, resp.StatusCode, contentType, int64(len(body)), depth, "", startTime)
	atomic.AddInt64(&e.crawledCount, 1)

	// Mark as completed in frontier
	e.frontier.MarkCompleted(frontierURL.ID)

	// Process content based on type
	e.processContent(workerID, url, body, contentType, depth, page)
}

// processContent processes fetched content
func (e *Engine) processContent(workerID int, url string, body []byte, contentType string, depth int, page *models.CrawledPage) {
	// Check for phrase matches
	textContent := e.linkExtractor.ExtractTextContent(body)
	matches := e.phraseDetector.DetectPhrases(textContent)

	for _, match := range matches {
		e.savePhraseMatch(page.ID, url, match.Phrase, match.Context, match.Occurrences)
		atomic.AddInt64(&e.matchCount, 1)
		log.Printf("[Worker %d] Found phrase '%s' in %s (%d occurrences)",
			workerID, match.Phrase, url, match.Occurrences)
	}

	// Process based on content type
	if strings.Contains(contentType, "text/html") {
		e.processHTML(url, body, depth)
	} else if strings.Contains(url, "sitemap") && strings.Contains(contentType, "xml") {
		e.processSitemap(url, body, depth)
	} else if strings.Contains(url, "robots.txt") {
		e.processRobots(url, body, depth)
	}
}

// processHTML processes HTML content and extracts links
func (e *Engine) processHTML(url string, body []byte, depth int) {
	links, err := e.linkExtractor.ExtractLinks(url, body)
	if err != nil {
		log.Printf("[Engine] Failed to extract links from %s: %v", url, err)
		return
	}

	// Get base domain for the current job
	e.jobMu.RLock()
	baseDomain := ""
	if e.currentJob != nil {
		baseDomain = e.currentJob.Domain
	}
	e.jobMu.RUnlock()

	// Filter and add links
	added := 0
	for _, link := range links {
		// Only add links from the same base domain
		linkDomain := e.urlCleaner.ExtractBaseDomain(link)
		if linkDomain == "" || !strings.Contains(linkDomain, baseDomain) {
			continue
		}

		if err := e.frontier.AddURL(link, depth+1, url); err == nil {
			added++
		}
	}

	log.Printf("[Engine] Added %d/%d links from %s", added, len(links), url)
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

// processRobots processes robots.txt
func (e *Engine) processRobots(url string, body []byte, depth int) {
	result := e.robotsParser.ParseRobots(url, body)

	// Add sitemaps from robots.txt
	for _, sitemap := range result.Sitemaps {
		e.frontier.AddURL(sitemap, depth+1, url)
	}

	log.Printf("[Engine] Parsed robots.txt %s: %d sitemaps", url, len(result.Sitemaps))
}

// saveCrawledPage saves a crawled page to database
func (e *Engine) saveCrawledPage(url string, statusCode int, contentType string, contentLength int64, depth int, errorMsg string, startTime time.Time) *models.CrawledPage {
	normalizedURL, _ := e.urlCleaner.ProcessURL(url)
	urlHash := e.urlCleaner.HashURL(normalizedURL)

	e.jobMu.RLock()
	crawlJobID := ""
	if e.currentJob != nil {
		crawlJobID = e.currentJob.ID
	}
	e.jobMu.RUnlock()

	page := &models.CrawledPage{
		CrawlJobID:    crawlJobID,
		URL:           url,
		URLHash:       urlHash,
		NormalizedURL: normalizedURL,
		StatusCode:    statusCode,
		ContentType:   contentType,
		ContentLength: contentLength,
		Depth:         depth,
		CrawledAt:     time.Now(),
		ResponseTime:  time.Since(startTime).Milliseconds(),
		ErrorMessage:  errorMsg,
	}

	e.db.Create(page)
	return page
}

// savePhraseMatch saves a phrase match to database
func (e *Engine) savePhraseMatch(pageID uint, url, phrase, context string, occurrences int) {
	e.jobMu.RLock()
	crawlJobID := ""
	if e.currentJob != nil {
		crawlJobID = e.currentJob.ID
	}
	e.jobMu.RUnlock()

	match := &models.PhraseMatch{
		CrawlJobID:  crawlJobID,
		PageID:      pageID,
		URL:         url,
		Phrase:      phrase,
		Context:     context,
		Occurrences: occurrences,
		FoundAt:     time.Now(),
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
