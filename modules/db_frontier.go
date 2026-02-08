package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"math/rand"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/resolver/crawler/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DBFrontier implements a database-backed frontier with random surfer model
type DBFrontier struct {
	db                  *gorm.DB
	crawlJobID          string
	teleportProbability float64
	rng                 *rand.Rand
	urlCleaner          *URLCleaner
	skipExtensions      map[string]bool
	includePatterns     []*regexp.Regexp // If set, only URLs matching these are added
	excludePatterns     []*regexp.Regexp // Ignored if includePatterns is set
	mu                  sync.Mutex
}

// NewDBFrontier creates a new database-backed frontier
func NewDBFrontier(db *gorm.DB, urlCleaner *URLCleaner, teleportProb float64) *DBFrontier {
	return &DBFrontier{
		db:                  db,
		teleportProbability: teleportProb,
		rng:                 rand.New(rand.NewSource(time.Now().UnixNano())),
		urlCleaner:          urlCleaner,
		skipExtensions:      make(map[string]bool),
	}
}

// SetSkipExtensions sets the file extensions to skip
func (f *DBFrontier) SetSkipExtensions(extensions []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.skipExtensions = make(map[string]bool)
	for _, ext := range extensions {
		// Normalize extension to lowercase with leading dot
		ext = strings.ToLower(strings.TrimSpace(ext))
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		f.skipExtensions[ext] = true
	}
	log.Printf("[%s] Configured to skip %d file extensions", f.Name(), len(f.skipExtensions))
}

// shouldSkipURL checks if a URL should be skipped based on its file extension
func (f *DBFrontier) shouldSkipURL(rawURL string) bool {
	if len(f.skipExtensions) == 0 {
		return false
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	// Get the file extension from the path
	ext := strings.ToLower(path.Ext(parsed.Path))
	if ext == "" {
		return false
	}

	return f.skipExtensions[ext]
}

// SetURLFilters sets include/exclude regex patterns for URL filtering.
// If includePatterns is non-empty, only URLs matching at least one pattern are added.
// If includePatterns is empty but excludePatterns is non-empty, URLs matching any pattern are skipped.
func (f *DBFrontier) SetURLFilters(includePatterns, excludePatterns []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.includePatterns = nil
	f.excludePatterns = nil

	for _, p := range includePatterns {
		if p == "" {
			continue
		}
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			log.Printf("[%s] Invalid include pattern '%s': %v", f.Name(), p, err)
			continue
		}
		f.includePatterns = append(f.includePatterns, re)
	}

	for _, p := range excludePatterns {
		if p == "" {
			continue
		}
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			log.Printf("[%s] Invalid exclude pattern '%s': %v", f.Name(), p, err)
			continue
		}
		f.excludePatterns = append(f.excludePatterns, re)
	}

	log.Printf("[%s] URL filters: %d include, %d exclude patterns", f.Name(), len(f.includePatterns), len(f.excludePatterns))
}

// shouldFilterURL checks if a URL should be filtered out based on include/exclude patterns
func (f *DBFrontier) shouldFilterURL(rawURL string) bool {
	// If include patterns are set, URL must match at least one
	if len(f.includePatterns) > 0 {
		for _, re := range f.includePatterns {
			if re.MatchString(rawURL) {
				return false // Matches an include pattern, allow it
			}
		}
		return true // Doesn't match any include pattern, filter it out
	}

	// If no include patterns but exclude patterns are set
	if len(f.excludePatterns) > 0 {
		for _, re := range f.excludePatterns {
			if re.MatchString(rawURL) {
				return true // Matches an exclude pattern, filter it out
			}
		}
	}

	return false // No filtering needed
}

// Name returns the module name
func (f *DBFrontier) Name() string {
	return "db_frontier"
}

// Initialize sets up the module
func (f *DBFrontier) Initialize() error {
	log.Printf("[%s] Initialized with teleport probability: %.2f", f.Name(), f.teleportProbability)
	return nil
}

// Shutdown gracefully stops the module
func (f *DBFrontier) Shutdown() error {
	return nil
}

// SetCrawlJob sets the current crawl job ID
func (f *DBFrontier) SetCrawlJob(crawlJobID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.crawlJobID = crawlJobID
}

// AddURL adds a URL to the frontier if not already exists
func (f *DBFrontier) AddURL(rawURL string, depth int, parentURL string) error {
	return f.addURLInternal(rawURL, depth, parentURL, "", true)
}

// AddURLWithAnchor adds a URL to the frontier with anchor text
func (f *DBFrontier) AddURLWithAnchor(rawURL string, depth int, parentURL string, anchorText string) error {
	return f.addURLInternal(rawURL, depth, parentURL, anchorText, true)
}

// addSeedURL adds a URL to the frontier, bypassing extension and URL pattern filters.
// Used for seed URLs that the user explicitly configured (target URL, robots.txt, sitemaps).
func (f *DBFrontier) addSeedURL(rawURL string, depth int, parentURL string) error {
	return f.addURLInternal(rawURL, depth, parentURL, "", false)
}

// addURLInternal is the core implementation for adding URLs to the frontier.
// When applyFilters is true, extension and include/exclude pattern checks are applied.
// When false, the URL is added unconditionally (used for seed URLs).
func (f *DBFrontier) addURLInternal(rawURL string, depth int, parentURL string, anchorText string, applyFilters bool) error {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()

	if crawlJobID == "" {
		return errors.New("no crawl job set")
	}

	if applyFilters {
		// Check if URL should be skipped based on file extension
		if f.shouldSkipURL(rawURL) {
			return nil // Silently skip URLs with excluded extensions
		}

		// Check URL include/exclude patterns
		if f.shouldFilterURL(rawURL) {
			return nil // Silently skip filtered URLs
		}
	}

	// Clean and normalize the URL
	cleanedURL, err := f.urlCleaner.ProcessURL(rawURL)
	if err != nil {
		return err
	}

	// Generate URL hash
	urlHash := hashURL(cleanedURL)

	// Check if URL was already crawled IN THIS JOB (crawled_pages contains completed URLs)
	var crawledCount int64
	f.db.Model(&models.CrawledPage{}).
		Where("crawl_job_id = ? AND url_hash = ?", crawlJobID, urlHash).
		Count(&crawledCount)
	if crawledCount > 0 {
		return nil // Already crawled in this job, skip
	}

	// Use upsert to avoid duplicates in frontier (per job)
	frontierURL := models.FrontierURL{
		CrawlJobID:    crawlJobID,
		URL:           rawURL,
		URLHash:       urlHash,
		NormalizedURL: cleanedURL,
		Depth:         depth,
		Priority:      0,
		Status:        models.FrontierStatusPending,
		ParentURL:     parentURL,
		AnchorText:    anchorText,
	}

	// Insert only if not exists (based on composite unique: crawl_job_id + url_hash)
	result := f.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "crawl_job_id"}, {Name: "url_hash"}},
		DoNothing: true,
	}).Create(&frontierURL)

	return result.Error
}

// AddURLs adds multiple URLs to the frontier
func (f *DBFrontier) AddURLs(urls []string, depth int, parentURL string) (int, error) {
	added := 0
	for _, url := range urls {
		if err := f.AddURL(url, depth, parentURL); err == nil {
			added++
		}
	}
	return added, nil
}

// GetNextURL retrieves the next URL to crawl using random surfer model
func (f *DBFrontier) GetNextURL() (*models.FrontierURL, error) {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()

	if crawlJobID == "" {
		return nil, errors.New("no crawl job set")
	}

	// Check if we should teleport
	shouldTeleport := f.rng.Float64() < f.teleportProbability

	var frontierURL models.FrontierURL
	var result *gorm.DB

	// Always require pending URLs to proceed
	var pendingCount int64
	f.db.Model(&models.FrontierURL{}).
		Where("crawl_job_id = ? AND status = ?", crawlJobID, models.FrontierStatusPending).
		Count(&pendingCount)

	if pendingCount == 0 {
		return nil, errors.New("no pending URLs in frontier")
	}

	// Teleport: still pick a random pending URL (frontier only contains pending URLs)
	_ = shouldTeleport
	offset := f.rng.Intn(int(pendingCount))
	result = f.db.Where("crawl_job_id = ? AND status = ?", crawlJobID, models.FrontierStatusPending).
		Offset(offset).
		Limit(1).
		First(&frontierURL)

	if result.Error != nil {
		return nil, result.Error
	}

	// Mark as processing
	f.db.Model(&frontierURL).Update("status", models.FrontierStatusProcessing)

	return &frontierURL, nil
}

// MarkCompleted removes a successfully crawled URL from the frontier queue
// The URL is already recorded in crawled_pages, so we delete it from the queue
func (f *DBFrontier) MarkCompleted(urlID uint) error {
	return f.db.Delete(&models.FrontierURL{}, "id = ?", urlID).Error
}

// MarkFailed marks a URL as failed
func (f *DBFrontier) MarkFailed(urlID uint, retryCount int, maxRetries int) error {
	if retryCount < maxRetries {
		// Mark as pending for retry
		return f.db.Model(&models.FrontierURL{}).
			Where("id = ?", urlID).
			Updates(map[string]interface{}{
				"status":      models.FrontierStatusPending,
				"retry_count": retryCount + 1,
			}).Error
	}
	// Mark as failed permanently
	return f.db.Model(&models.FrontierURL{}).
		Where("id = ?", urlID).
		Update("status", models.FrontierStatusFailed).Error
}

// PendingCount returns the number of pending URLs
func (f *DBFrontier) PendingCount() int64 {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()

	var count int64
	f.db.Model(&models.FrontierURL{}).
		Where("crawl_job_id = ? AND status = ?", crawlJobID, models.FrontierStatusPending).
		Count(&count)
	return count
}

// TotalCount returns the total number of URLs in frontier
func (f *DBFrontier) TotalCount() int64 {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()

	var count int64
	f.db.Model(&models.FrontierURL{}).
		Where("crawl_job_id = ?", crawlJobID).
		Count(&count)
	return count
}

// GetStats returns frontier statistics
// Note: completed URLs are deleted from frontier, so we get that count from crawled_pages
func (f *DBFrontier) GetStats() (pending, processing, completed, failed int64) {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()

	f.db.Model(&models.FrontierURL{}).
		Where("crawl_job_id = ? AND status = ?", crawlJobID, models.FrontierStatusPending).
		Count(&pending)
	f.db.Model(&models.FrontierURL{}).
		Where("crawl_job_id = ? AND status = ?", crawlJobID, models.FrontierStatusProcessing).
		Count(&processing)
	// Completed URLs are removed from frontier and recorded in crawled_pages
	f.db.Model(&models.CrawledPage{}).
		Where("crawl_job_id = ? AND error_message = ''", crawlJobID).
		Count(&completed)
	f.db.Model(&models.FrontierURL{}).
		Where("crawl_job_id = ? AND status = ?", crawlJobID, models.FrontierStatusFailed).
		Count(&failed)
	return
}

// IsURLSeen checks if a URL has been seen in the current job (exists in frontier OR crawled_pages)
func (f *DBFrontier) IsURLSeen(rawURL string) bool {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()

	if crawlJobID == "" {
		return false
	}

	cleanedURL, err := f.urlCleaner.ProcessURL(rawURL)
	if err != nil {
		return false
	}
	urlHash := hashURL(cleanedURL)

	// Check if URL is in the frontier queue for this job (pending/processing/failed)
	var frontierCount int64
	f.db.Model(&models.FrontierURL{}).
		Where("crawl_job_id = ? AND url_hash = ?", crawlJobID, urlHash).
		Count(&frontierCount)
	if frontierCount > 0 {
		return true
	}

	// Check if URL was already crawled in this job (completed URLs are in crawled_pages)
	var crawledCount int64
	f.db.Model(&models.CrawledPage{}).
		Where("crawl_job_id = ? AND url_hash = ?", crawlJobID, urlHash).
		Count(&crawledCount)
	return crawledCount > 0
}

// ResetProcessingURLs resets any URLs stuck in processing state (for recovery)
func (f *DBFrontier) ResetProcessingURLs() error {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()

	return f.db.Model(&models.FrontierURL{}).
		Where("crawl_job_id = ? AND status = ?", crawlJobID, models.FrontierStatusProcessing).
		Update("status", models.FrontierStatusPending).Error
}

// hashURL creates a SHA256 hash of the URL
func hashURL(url string) string {
	hash := sha256.Sum256([]byte(url))
	return hex.EncodeToString(hash[:])
}

// AddSeedURLs adds initial seed URLs for a crawl job.
// Seed URLs bypass include/exclude URL pattern filters since they are explicitly
// provided by the user as the starting point for the crawl.
func (f *DBFrontier) AddSeedURLs(targetURL string) error {
	// Add the target URL itself (bypass filters)
	if err := f.addSeedURL(targetURL, 0, ""); err != nil {
		log.Printf("[%s] Warning: failed to add target URL: %v", f.Name(), err)
	}

	// Add robots.txt (bypass filters)
	robotsURL := targetURL + "/robots.txt"
	if err := f.addSeedURL(robotsURL, 0, targetURL); err != nil {
		log.Printf("[%s] Warning: failed to add robots.txt: %v", f.Name(), err)
	}

	// Add sitemap.xml (bypass filters)
	sitemapURL := targetURL + "/sitemap.xml"
	if err := f.addSeedURL(sitemapURL, 0, targetURL); err != nil {
		log.Printf("[%s] Warning: failed to add sitemap.xml: %v", f.Name(), err)
	}

	// Add sitemap_index.xml (bypass filters)
	sitemapIndexURL := targetURL + "/sitemap_index.xml"
	if err := f.addSeedURL(sitemapIndexURL, 0, targetURL); err != nil {
		log.Printf("[%s] Warning: failed to add sitemap_index.xml: %v", f.Name(), err)
	}

	log.Printf("[%s] Added seed URLs for %s (filters bypassed for seeds)", f.Name(), targetURL)
	return nil
}
