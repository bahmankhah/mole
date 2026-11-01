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

// ErrFrontierEmpty is returned by GetNextURL when no URL is pending.
var ErrFrontierEmpty = errors.New("no pending URLs in frontier")

// DBFrontier implements a database-backed frontier with random surfer model
type DBFrontier struct {
	db                  *gorm.DB
	crawlJobID          string
	workerID            string
	teleportProbability float64
	rng                 *rand.Rand
	urlCleaner          *URLCleaner
	skipExtensions      map[string]bool
	hostCursor          uint             // rotates over the sites with queued URLs
	hosts               []string         // the sites, as last read
	hostsFor            string           // the job f.hosts was read for
	hostsRead           time.Time        // when it was read
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

// SetWorker names the worker this frontier claims URLs for, so claims left
// behind by a worker that died can be told from those a live worker holds.
func (f *DBFrontier) SetWorker(workerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workerID = workerID
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

	// Clean and normalize the URL — preserve fragment for SPA URLs
	var cleanedURL string
	var err error
	if HasMeaningfulFragment(rawURL) {
		cleanedURL, err = f.urlCleaner.ProcessURLKeepFragment(rawURL)
	} else {
		cleanedURL, err = f.urlCleaner.ProcessURL(rawURL)
	}
	if err != nil {
		return err
	}

	return f.enqueueFrontier(cleanedURL, depth, parentURL, anchorText, "", "")
}

// AddSeedRequest adds a seed URL with an explicit HTTP method and optional body.
// Used for POST crawl jobs so the same URL can be queued with different payloads.
func (f *DBFrontier) AddSeedRequest(rawURL, method, body string) error {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()
	if crawlJobID == "" {
		return errors.New("no crawl job set")
	}

	var cleanedURL string
	var err error
	if HasMeaningfulFragment(rawURL) {
		cleanedURL, err = f.urlCleaner.ProcessURLKeepFragment(rawURL)
	} else {
		cleanedURL, err = f.urlCleaner.ProcessURL(rawURL)
	}
	if err != nil {
		cleanedURL = rawURL
	}
	return f.enqueueFrontier(cleanedURL, 0, "", "", method, body)
}

// enqueueFrontier inserts a frontier row if this request identity has not been
// crawled or queued yet. Method/body are included in the identity hash so POST
// variants of the same URL are distinct.
func (f *DBFrontier) enqueueFrontier(cleanedURL string, depth int, parentURL, anchorText, method, body string) error {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()
	if crawlJobID == "" {
		return errors.New("no crawl job set")
	}

	urlHash := RequestIdentityHash(cleanedURL, method, body)

	var crawledCount int64
	f.db.Model(&models.CrawledPage{}).
		Where("crawl_job_id = ? AND url_hash = ?", crawlJobID, urlHash).
		Count(&crawledCount)
	if crawledCount > 0 {
		return nil
	}

	frontierURL := models.FrontierURL{
		CrawlJobID:    crawlJobID,
		URL:           cleanedURL,
		URLHash:       urlHash,
		NormalizedURL: cleanedURL,
		Depth:         depth,
		Priority:      0,
		Status:        models.FrontierStatusPending,
		ParentURL:     parentURL,
		AnchorText:    anchorText,
		RequestMethod: method,
		RequestBody:   body,
		Host:          f.urlCleaner.ExtractBaseDomain(cleanedURL),
	}

	result := f.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "crawl_job_id"}, {Name: "url_hash"}},
		DoNothing: true,
	}).Create(&frontierURL)

	if result.Error != nil {
		log.Printf("[%s] DB error adding URL %s: %v", f.Name(), cleanedURL, result.Error)
		return result.Error
	}
	if result.RowsAffected > 0 {
		log.Printf("[%s] ADDED url=%s method=%s hash=%s depth=%d", f.Name(), cleanedURL, defaultMethod(method), urlHash[:12], depth)
	}
	return nil
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

// GetNextURL retrieves the next URL to crawl, taking the sites of a
// multi-site crawl in turn and picking at random within the site chosen.
//
// Picking at random across the whole queue starves a site. The queue is
// ordered by id, so a site whose URLs were added late, or whose start page
// failed once and went back on the queue, sits at one end of a range that
// the other sites' tens of thousands of URLs have stretched — and a random
// id lands on it almost never. Taking the sites in turn is what crawlers
// do for exactly this reason, and it keeps a big site from crowding out a
// small one.
//
// Claiming uses SELECT ... FOR UPDATE SKIP LOCKED, so several workers, on
// this machine or another, pull from one job's queue without ever being
// handed the same URL twice.
func (f *DBFrontier) GetNextURL() (*models.FrontierURL, error) {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()

	if crawlJobID == "" {
		return nil, errors.New("no crawl job set")
	}

	hosts, err := f.pendingHosts(crawlJobID)
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, ErrFrontierEmpty
	}

	// Start where the last call left off, so successive pulls walk the
	// sites rather than all going to whichever comes first.
	f.mu.Lock()
	start := f.hostCursor
	f.hostCursor++
	f.mu.Unlock()

	for i := range hosts {
		host := hosts[(start+uint(i))%uint(len(hosts))]
		fu, err := f.claimFromHost(crawlJobID, host)
		if err != nil {
			return nil, err
		}
		if fu != nil {
			return fu, nil
		}
		// That site's queue emptied, or another worker took its last URL
		// between listing and claiming; try the next site.
	}
	return nil, ErrFrontierEmpty
}

// pendingHosts lists the sites with URLs still queued. It is asked for on
// every pull, so the answer is held briefly: sites come and go far more
// slowly than URLs do.
func (f *DBFrontier) pendingHosts(crawlJobID string) ([]string, error) {
	f.mu.Lock()
	if crawlJobID == f.hostsFor && time.Since(f.hostsRead) < hostListTTL {
		hosts := f.hosts
		f.mu.Unlock()
		return hosts, nil
	}
	f.mu.Unlock()

	var hosts []string
	if err := f.db.Model(&models.FrontierURL{}).
		Where("crawl_job_id = ? AND status = ?", crawlJobID, models.FrontierStatusPending).
		Distinct().Pluck("host", &hosts).Error; err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.hosts, f.hostsFor, f.hostsRead = hosts, crawlJobID, time.Now()
	f.mu.Unlock()
	return hosts, nil
}

// hostListTTL is how long the list of sites with queued URLs is reused
// before being read again.
const hostListTTL = 2 * time.Second

// claimFromHost claims one queued URL of a single site, chosen at random
// among that site's URLs. It returns nil when the site has none left.
func (f *DBFrontier) claimFromHost(crawlJobID, host string) (*models.FrontierURL, error) {
	f.mu.Lock()
	workerID := f.workerID
	f.mu.Unlock()

	// A fresh query every time: a *gorm.DB chain must not be reused across
	// final calls.
	pendingQuery := func(db *gorm.DB) *gorm.DB {
		q := db.Model(&models.FrontierURL{}).
			Where("crawl_job_id = ? AND status = ?", crawlJobID, models.FrontierStatusPending)
		if host == "" {
			// Entries queued before URLs recorded their site.
			return q.Where("host IS NULL OR host = ''")
		}
		return q.Where("host = ?", host)
	}

	// A cheaper random pick than OFFSET, which is O(n) in MySQL: take the
	// lowest and highest id of this site's queued URLs, pick an id in
	// between, and claim the nearest at or above it.
	var minID, maxID uint
	pendingQuery(f.db).Select("COALESCE(MIN(id), 0)").Scan(&minID)
	pendingQuery(f.db).Select("COALESCE(MAX(id), 0)").Scan(&maxID)
	if maxID == 0 {
		return nil, nil
	}

	f.mu.Lock() // math/rand.Rand is not safe for concurrent workers
	randomID := minID + uint(f.rng.Int63n(int64(maxID-minID+1)))
	f.mu.Unlock()

	var frontierURL models.FrontierURL
	err := f.db.Transaction(func(tx *gorm.DB) error {
		locked := func() *gorm.DB {
			return pendingQuery(tx).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}

		result := locked().Where("id >= ?", randomID).Order("id ASC").Limit(1).Find(&frontierURL)
		if result.Error != nil || frontierURL.ID == 0 {
			// Wrap around — try from this site's first queued URL.
			result = locked().Order("id ASC").Limit(1).Find(&frontierURL)
		}
		if result.Error != nil {
			return result.Error
		}
		if frontierURL.ID == 0 {
			return ErrFrontierEmpty
		}

		// Claim it within the same transaction, stamped with this worker.
		return tx.Model(&frontierURL).Updates(map[string]interface{}{
			"status":     models.FrontierStatusProcessing,
			"claimed_by": workerID,
			"updated_at": time.Now(),
		}).Error
	})
	if errors.Is(err, ErrFrontierEmpty) {
		return nil, nil // this site ran out while we were looking
	}
	if err != nil {
		return nil, err
	}
	return &frontierURL, nil
}

// MarkCompleted removes a successfully crawled URL from the frontier queue
// The URL is already recorded in crawled_pages, so we delete it from the queue
func (f *DBFrontier) MarkCompleted(urlID uint) error {
	return f.db.Delete(&models.FrontierURL{}, "id = ?", urlID).Error
}

// Release puts a claimed URL back to pending without counting a retry.
func (f *DBFrontier) Release(urlID uint) error {
	return f.db.Model(&models.FrontierURL{}).
		Where("id = ?", urlID).
		Updates(map[string]interface{}{"status": models.FrontierStatusPending, "claimed_by": ""}).Error
}

// MarkFailed marks a URL as failed
func (f *DBFrontier) MarkFailed(urlID uint, retryCount int, maxRetries int) error {
	if retryCount < maxRetries {
		// Mark as pending for retry
		return f.db.Model(&models.FrontierURL{}).
			Where("id = ?", urlID).
			Updates(map[string]interface{}{
				"status":      models.FrontierStatusPending,
				"claimed_by":  "",
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
	urlHash := RequestIdentityHash(cleanedURL, "", "")
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

// ReleaseAbandonedClaims puts URLs back on the queue that a worker claimed
// and never finished because it died. A claim counts as abandoned when the
// worker holding it is not among liveWorkers, or when it has gone untouched
// for staleAfter regardless — the backstop for a worker that is alive but
// wedged, and for claims taken before claims recorded a worker at all.
// Claims held by a live worker are left alone: pulling those back would
// hand the same URL to two workers at once.
func (f *DBFrontier) ReleaseAbandonedClaims(liveWorkers []string, staleAfter time.Duration) (int64, error) {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()

	q := f.db.Model(&models.FrontierURL{}).
		Where("crawl_job_id = ? AND status = ?", crawlJobID, models.FrontierStatusProcessing)
	if len(liveWorkers) > 0 {
		q = q.Where("(claimed_by NOT IN ? OR updated_at < ?)", liveWorkers, time.Now().Add(-staleAfter))
	}
	res := q.Updates(map[string]interface{}{"status": models.FrontierStatusPending, "claimed_by": ""})
	return res.RowsAffected, res.Error
}

// RequestIdentityHash uniquely identifies a fetch.
// GET requests hash the URL alone (same as historical url_hash).
// POST requests include method and body so distinct payloads are not collapsed.
func RequestIdentityHash(rawURL, method, body string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" || method == "GET" {
		hash := sha256.Sum256([]byte(rawURL))
		return hex.EncodeToString(hash[:])
	}
	hash := sha256.Sum256([]byte(rawURL + "\n" + method + "\n" + body))
	return hex.EncodeToString(hash[:])
}

func defaultMethod(method string) string {
	if strings.TrimSpace(method) == "" {
		return "GET"
	}
	return strings.ToUpper(method)
}

// AddSeedURLs adds initial seed URLs for a crawl job.
// Seed URLs bypass include/exclude URL pattern filters since they are explicitly
// provided by the user as the starting point for the crawl.
// Extra discovery seeds (robots.txt, sitemaps) are only added when the target
// URL points to the domain root (e.g. https://example.com/ ) AND has no
// meaningful fragment (SPA route).
func (f *DBFrontier) AddSeedURLs(targetURL string) error {
	// Check if the URL has a meaningful fragment (SPA route) BEFORE normalization
	// strips it. Examples: https://example.com/#/search?q=foo
	hasFragment := HasMeaningfulFragment(targetURL)

	// Normalize the target URL — preserve fragment for SPA URLs
	var normalized string
	var err error
	if hasFragment {
		normalized, err = f.urlCleaner.ProcessURLKeepFragment(targetURL)
	} else {
		normalized, err = f.urlCleaner.ProcessURL(targetURL)
	}
	if err != nil {
		normalized = targetURL // fallback to raw if normalization fails
	}

	// Add the target URL itself (bypass filters).
	// For fragment URLs we use addSeedURLDirect since the URL is already cleaned
	// and we don't want ProcessURL to strip the fragment again.
	if hasFragment {
		if err := f.addSeedURLDirect(normalized, 0, ""); err != nil {
			log.Printf("[%s] Warning: failed to add target URL: %v", f.Name(), err)
		}
	} else {
		if err := f.addSeedURL(normalized, 0, ""); err != nil {
			log.Printf("[%s] Warning: failed to add target URL: %v", f.Name(), err)
		}
	}

	// Only add discovery seeds (robots.txt, sitemaps) if the target is a root
	// URL AND does not have an SPA fragment. A URL like
	// https://example.com/#/search?q=foo is NOT a root — it's a specific page.
	if !hasFragment && isRootURL(normalized) {
		// Derive the origin (scheme + host) for building seed URLs
		origin := extractOrigin(normalized)

		robotsSeed := origin + "/robots.txt"
		if err := f.addSeedURL(robotsSeed, 0, normalized); err != nil {
			log.Printf("[%s] Warning: failed to add robots.txt: %v", f.Name(), err)
		}

		sitemapSeed := origin + "/sitemap.xml"
		if err := f.addSeedURL(sitemapSeed, 0, normalized); err != nil {
			log.Printf("[%s] Warning: failed to add sitemap.xml: %v", f.Name(), err)
		}

		sitemapIndexSeed := origin + "/sitemap_index.xml"
		if err := f.addSeedURL(sitemapIndexSeed, 0, normalized); err != nil {
			log.Printf("[%s] Warning: failed to add sitemap_index.xml: %v", f.Name(), err)
		}

		log.Printf("[%s] Added seed URLs for %s (root domain, discovery seeds included)", f.Name(), normalized)
	} else {
		log.Printf("[%s] Added seed URL %s (non-root path or SPA fragment, skipping discovery seeds)", f.Name(), normalized)
	}

	return nil
}

// addSeedURLDirect adds a pre-cleaned URL to the frontier, bypassing URL
// normalization. Used for SPA URLs with fragments that have already been
// processed by ProcessURLKeepFragment.
func (f *DBFrontier) addSeedURLDirect(cleanedURL string, depth int, parentURL string) error {
	f.mu.Lock()
	crawlJobID := f.crawlJobID
	f.mu.Unlock()

	if crawlJobID == "" {
		return errors.New("no crawl job set")
	}

	return f.enqueueFrontier(cleanedURL, depth, parentURL, "", "", "")
}

// isRootURL returns true if the URL points to the domain root with no meaningful path.
// e.g. https://example.com, https://example.com/, https://sub.example.com/ → true
// e.g. https://example.com/page, https://example.com/path/to → false
func isRootURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	p := parsed.Path
	return p == "" || p == "/"
}

// extractOrigin returns scheme://host from a URL.
func extractOrigin(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return parsed.Scheme + "://" + parsed.Host
}
