package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/crawler"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"gorm.io/gorm"
)

// Manager manages crawl jobs and subdomain discovery
type Manager struct {
	db               *gorm.DB
	config           *config.Config
	engine           *crawler.Engine
	stemmer          *modules.Stemmer
	wordExtractor    *modules.WordExtractor
	subdomainScanner *modules.SubdomainScanner

	mu              sync.Mutex
	activeJob       *models.CrawlJob
	subdomainCtx    context.Context
	subdomainCancel context.CancelFunc
}

// NewManager creates a new job manager
func NewManager(db *gorm.DB, cfg *config.Config, engine *crawler.Engine) *Manager {
	scanner := modules.NewSubdomainScanner(cfg.Subdomain)
	scanner.Initialize()

	m := &Manager{
		db:               db,
		config:           cfg,
		engine:           engine,
		stemmer:          engine.GetStemmer(),
		wordExtractor:    engine.GetWordExtractor(),
		subdomainScanner: scanner,
	}

	// Clean up any stale running/paused jobs from previous runs
	m.cleanupStaleJobs()

	return m
}

// cleanupStaleJobs resets any jobs that were running or paused when the server stopped
func (m *Manager) cleanupStaleJobs() {
	// Update any running jobs to cancelled status
	result := m.db.Model(&models.CrawlJob{}).
		Where("status IN ?", []string{string(models.JobStatusRunning), string(models.JobStatusPaused)}).
		Updates(map[string]interface{}{
			"status":        models.JobStatusCancelled,
			"error_message": "Job interrupted by server restart",
		})
	if result.RowsAffected > 0 {
		log.Printf("[JobManager] Cleaned up %d stale jobs from previous run", result.RowsAffected)
	}

	// Also clean up any stale discovery jobs
	m.db.Model(&models.DiscoveryJob{}).
		Where("status IN ?", []string{string(models.JobStatusRunning), string(models.JobStatusPaused)}).
		Updates(map[string]interface{}{
			"status":        models.JobStatusCancelled,
			"error_message": "Job interrupted by server restart",
		})
}

// CreateJob creates a new crawl job for a URL or domain
func (m *Manager) CreateJob(targetURL string, maxDepth int) (*models.CrawlJob, error) {
	return m.CreateJobWithSettings(targetURL, maxDepth, nil)
}

// CreateJobWithSettings creates a new crawl job with optional per-job settings
func (m *Manager) CreateJobWithSettings(targetURL string, maxDepth int, settings *models.JobSettings) (*models.CrawlJob, error) {
	// Parse the URL to extract domain
	var domain string
	var fullURL string

	// Check if it's a full URL or just a domain
	if strings.HasPrefix(targetURL, "http://") || strings.HasPrefix(targetURL, "https://") {
		parsed, err := url.Parse(targetURL)
		if err != nil {
			return nil, fmt.Errorf("invalid URL: %v", err)
		}
		// Store the base domain (e.g. "bayut.com" not "www.bayut.com")
		// so that link filtering works correctly across subdomains
		urlCleaner := modules.NewURLCleaner()
		domain = urlCleaner.ExtractBaseDomain(parsed.Scheme + "://" + parsed.Host)
		if domain == "" {
			domain = parsed.Host // fallback
		}
		fullURL = targetURL
	} else {
		// It's just a domain
		domain = modules.CleanDomain(targetURL)
		fullURL = "https://" + domain
	}

	job := &models.CrawlJob{
		Domain:    domain,
		TargetURL: fullURL,
		Status:    models.JobStatusPending,
		MaxDepth:  maxDepth,
		Settings:  settings,
	}

	if err := m.db.Create(job).Error; err != nil {
		return nil, err
	}

	log.Printf("[JobManager] Created job %s for URL %s (domain: %s)", job.ID, fullURL, domain)
	return job, nil
}

// UpdateJobSeedURLs saves the expanded seed URLs for a job.
func (m *Manager) UpdateJobSeedURLs(jobID string, seeds models.StringSlice) error {
	return m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Update("seed_urls", seeds).Error
}

// StartJob starts a crawl job
func (m *Manager) StartJob(jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeJob != nil && m.activeJob.Status == models.JobStatusRunning {
		return fmt.Errorf("a job is already running")
	}

	var job models.CrawlJob
	if err := m.db.First(&job, "id = ?", jobID).Error; err != nil {
		return err
	}

	// Load phrases
	if err := m.engine.LoadPhrases(); err != nil {
		log.Printf("[JobManager] Warning: failed to load phrases: %v", err)
	}

	// Start the crawler engine
	if err := m.engine.Start(&job); err != nil {
		return err
	}

	m.activeJob = &job
	log.Printf("[JobManager] Started job %s", jobID)
	return nil
}

// StopJob stops the current crawl job
func (m *Manager) StopJob() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check engine state directly
	engineState := m.engine.GetState()
	if engineState == crawler.StateIdle {
		// Engine is idle, but check if there's a job in the database that needs cleanup
		var runningJob models.CrawlJob
		if err := m.db.Where("status IN ?", []string{string(models.JobStatusRunning), string(models.JobStatusPaused)}).First(&runningJob).Error; err == nil {
			// Found a stale job, update its status
			runningJob.Status = models.JobStatusCancelled
			runningJob.ErrorMessage = "Job stopped manually"
			m.db.Save(&runningJob)
			log.Printf("[JobManager] Cleaned up stale job %s", runningJob.ID)
			return nil
		}
		return fmt.Errorf("no active job")
	}

	m.engine.Stop()
	m.activeJob = nil
	return nil
}

// PauseJob pauses the current crawl job
func (m *Manager) PauseJob() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check engine state directly
	engineState := m.engine.GetState()
	if engineState != crawler.StateRunning {
		// Engine is not running, but check if there's a job in the database that needs cleanup
		if engineState == crawler.StateIdle {
			var runningJob models.CrawlJob
			if err := m.db.Where("status = ?", string(models.JobStatusRunning)).First(&runningJob).Error; err == nil {
				// Found a stale running job, update its status to paused
				runningJob.Status = models.JobStatusPaused
				m.db.Save(&runningJob)
				log.Printf("[JobManager] Marked stale job %s as paused", runningJob.ID)
				return nil
			}
		}
		return fmt.Errorf("no running job to pause")
	}

	m.engine.Pause()
	return nil
}

// ResumeJob resumes the current crawl job
func (m *Manager) ResumeJob() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check engine state directly
	if m.engine.GetState() != crawler.StatePaused {
		return fmt.Errorf("no paused job to resume")
	}

	m.engine.Resume()
	return nil
}

// GetJob retrieves a job by ID
func (m *Manager) GetJob(jobID string) (*models.CrawlJob, error) {
	var job models.CrawlJob
	if err := m.db.First(&job, "id = ?", jobID).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

// GetJobs retrieves all jobs
func (m *Manager) GetJobs(limit, offset int) ([]models.CrawlJob, int64, error) {
	var jobs []models.CrawlJob
	var total int64

	m.db.Model(&models.CrawlJob{}).Count(&total)

	if err := m.db.Order("created_at DESC").Limit(limit).Offset(offset).Find(&jobs).Error; err != nil {
		return nil, 0, err
	}

	return jobs, total, nil
}

// DeleteJob deletes a job and its associated data
func (m *Manager) DeleteJob(jobID string) error {
	// Delete associated data
	m.db.Where("discovery_job_id = ?", jobID).Delete(&models.Subdomain{})
	m.db.Where("crawl_job_id = ?", jobID).Delete(&models.CrawledPage{})
	m.db.Where("crawl_job_id = ?", jobID).Delete(&models.FrontierURL{})
	m.db.Where("crawl_job_id = ?", jobID).Delete(&models.PhraseMatch{})

	// Delete the job
	return m.db.Delete(&models.CrawlJob{}, "id = ?", jobID).Error
}

// StartSubdomainDiscovery starts subdomain discovery for a job
func (m *Manager) StartSubdomainDiscovery(jobID string) error {
	var crawlJob models.CrawlJob
	if err := m.db.First(&crawlJob, "id = ?", jobID).Error; err != nil {
		return err
	}

	// Create a discovery job first
	discoveryJob := &models.DiscoveryJob{
		Domain: crawlJob.Domain,
		Status: models.JobStatusRunning,
	}
	now := time.Now()
	discoveryJob.StartedAt = &now
	if err := m.db.Create(discoveryJob).Error; err != nil {
		return err
	}

	// Link the discovery job to the crawl job
	m.db.Model(&crawlJob).Update("discovery_job_id", discoveryJob.ID)

	m.subdomainCtx, m.subdomainCancel = context.WithCancel(context.Background())

	go func() {
		foundCount := 0

		// Add the main domain itself as a subdomain
		mainSub := &models.Subdomain{
			DiscoveryJobID: discoveryJob.ID,
			Domain:         crawlJob.Domain,
			Subdomain:      crawlJob.Domain,
			FullURL:        "https://" + crawlJob.Domain,
			IsActive:       true,
		}
		if err := m.db.Create(mainSub).Error; err == nil {
			foundCount++
		}

		m.subdomainScanner.DiscoverSubdomains(crawlJob.Domain, func(subdomain string) {
			// Check context
			select {
			case <-m.subdomainCtx.Done():
				return
			default:
			}

			// Save discovered subdomain
			sub := &models.Subdomain{
				DiscoveryJobID: discoveryJob.ID,
				Domain:         crawlJob.Domain,
				Subdomain:      subdomain,
				FullURL:        "https://" + subdomain,
				IsActive:       true,
			}
			if err := m.db.Create(sub).Error; err == nil {
				foundCount++
				// Update count
				m.db.Model(&models.DiscoveryJob{}).Where("id = ?", discoveryJob.ID).Update("subdomains_found", foundCount)
			}
			log.Printf("[JobManager] Discovered subdomain: %s", subdomain)
		})

		// Mark discovery as completed
		now := time.Now()
		m.db.Model(&models.DiscoveryJob{}).Where("id = ?", discoveryJob.ID).Updates(map[string]interface{}{
			"status":           models.JobStatusCompleted,
			"completed_at":     &now,
			"subdomains_found": foundCount,
		})
		log.Printf("[JobManager] Discovery completed for %s. Found %d subdomains.", crawlJob.Domain, foundCount)
	}()

	return nil
}

// CreateDiscoveryJobForDomain creates and starts a discovery job directly for a domain
func (m *Manager) CreateDiscoveryJobForDomain(domain string) (*models.DiscoveryJob, error) {
	// Clean domain
	domain = modules.CleanDomain(domain)

	// Create a discovery job
	discoveryJob := &models.DiscoveryJob{
		Domain: domain,
		Status: models.JobStatusRunning,
	}
	now := time.Now()
	discoveryJob.StartedAt = &now
	if err := m.db.Create(discoveryJob).Error; err != nil {
		return nil, err
	}

	m.subdomainCtx, m.subdomainCancel = context.WithCancel(context.Background())

	go func() {
		foundCount := 0

		// Add the main domain itself as a subdomain
		mainSub := &models.Subdomain{
			DiscoveryJobID: discoveryJob.ID,
			Domain:         domain,
			Subdomain:      domain,
			FullURL:        "https://" + domain,
			IsActive:       true,
		}
		if err := m.db.Create(mainSub).Error; err == nil {
			foundCount++
		}

		m.subdomainScanner.DiscoverSubdomains(domain, func(subdomain string) {
			// Check context
			select {
			case <-m.subdomainCtx.Done():
				return
			default:
			}

			// Save discovered subdomain
			sub := &models.Subdomain{
				DiscoveryJobID: discoveryJob.ID,
				Domain:         domain,
				Subdomain:      subdomain,
				FullURL:        "https://" + subdomain,
				IsActive:       true,
			}
			if err := m.db.Create(sub).Error; err == nil {
				foundCount++
				// Update count
				m.db.Model(&models.DiscoveryJob{}).Where("id = ?", discoveryJob.ID).Update("subdomains_found", foundCount)
			}
			log.Printf("[JobManager] Discovered subdomain: %s", subdomain)
		})

		// Mark discovery as completed
		now := time.Now()
		m.db.Model(&models.DiscoveryJob{}).Where("id = ?", discoveryJob.ID).Updates(map[string]interface{}{
			"status":           models.JobStatusCompleted,
			"completed_at":     &now,
			"subdomains_found": foundCount,
		})
		log.Printf("[JobManager] Discovery completed for %s. Found %d subdomains.", domain, foundCount)
	}()

	return discoveryJob, nil
}

// StopSubdomainDiscovery stops subdomain discovery
func (m *Manager) StopSubdomainDiscovery() {
	if m.subdomainCancel != nil {
		m.subdomainCancel()
	}
}

// GetDiscoveryJobs retrieves all discovery jobs
func (m *Manager) GetDiscoveryJobs(limit, offset int) ([]models.DiscoveryJob, int64, error) {
	var jobs []models.DiscoveryJob
	var total int64

	m.db.Model(&models.DiscoveryJob{}).Count(&total)

	if err := m.db.Order("created_at DESC").Limit(limit).Offset(offset).Find(&jobs).Error; err != nil {
		return nil, 0, err
	}

	return jobs, total, nil
}

// GetDiscoveryJob retrieves a discovery job by ID
func (m *Manager) GetDiscoveryJob(jobID string) (*models.DiscoveryJob, error) {
	var job models.DiscoveryJob
	if err := m.db.First(&job, "id = ?", jobID).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

// GetSubdomainsByDiscoveryJob retrieves subdomains for a discovery job
func (m *Manager) GetSubdomainsByDiscoveryJob(discoveryJobID string) ([]models.Subdomain, error) {
	var subdomains []models.Subdomain
	if err := m.db.Where("discovery_job_id = ?", discoveryJobID).Order("subdomain ASC").Find(&subdomains).Error; err != nil {
		return nil, err
	}
	return subdomains, nil
}

// GetSubdomains retrieves subdomains for a job (checks both discovery_job_id for discovery jobs or crawl_job_id for crawl jobs)
func (m *Manager) GetSubdomains(jobID string) ([]models.Subdomain, error) {
	var subdomains []models.Subdomain
	// Check discovery_job_id first (for main jobs), otherwise check via the crawl_job's discovery_job_id
	if err := m.db.Where("discovery_job_id = ?", jobID).Find(&subdomains).Error; err != nil {
		return nil, err
	}
	// If no results, try to find via the crawl job's linked discovery job
	if len(subdomains) == 0 {
		var job models.CrawlJob
		if err := m.db.First(&job, "id = ?", jobID).Error; err == nil && job.DiscoveryJobID != "" {
			m.db.Where("discovery_job_id = ?", job.DiscoveryJobID).Find(&subdomains)
		}
	}
	return subdomains, nil
}

// CreateCrawlJobFromSubdomain creates a new crawl job for a specific subdomain
func (m *Manager) CreateCrawlJobFromSubdomain(subdomainID uint, maxDepth int) (*models.CrawlJob, error) {
	var subdomain models.Subdomain
	if err := m.db.First(&subdomain, "id = ?", subdomainID).Error; err != nil {
		return nil, err
	}

	// Create the crawl job
	job := &models.CrawlJob{
		DiscoveryJobID: subdomain.DiscoveryJobID,
		Domain:         subdomain.Domain,
		TargetURL:      subdomain.FullURL,
		Status:         models.JobStatusPending,
		MaxDepth:       maxDepth,
	}

	if err := m.db.Create(job).Error; err != nil {
		return nil, err
	}

	// Link the crawl job to the subdomain
	m.db.Model(&subdomain).Update("crawl_job_id", job.ID)

	log.Printf("[JobManager] Created crawl job %s for subdomain %s", job.ID, subdomain.Subdomain)
	return job, nil
}

// GetSubdomain retrieves a single subdomain by ID
func (m *Manager) GetSubdomain(id uint) (*models.Subdomain, error) {
	var subdomain models.Subdomain
	if err := m.db.First(&subdomain, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &subdomain, nil
}

// GetPhraseMatches retrieves phrase matches for a job
func (m *Manager) GetPhraseMatches(jobID string, limit, offset int) ([]models.PhraseMatch, int64, error) {
	var matches []models.PhraseMatch
	var total int64

	m.db.Model(&models.PhraseMatch{}).Where("crawl_job_id = ? AND is_archived = ?", jobID, false).Count(&total)

	if err := m.db.Where("crawl_job_id = ? AND is_archived = ?", jobID, false).
		Order("found_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&matches).Error; err != nil {
		return nil, 0, err
	}

	return matches, total, nil
}

// GetAllPhraseMatches retrieves all phrase matches across all jobs
func (m *Manager) GetAllPhraseMatches(limit, offset int) ([]models.PhraseMatch, int64, error) {
	var matches []models.PhraseMatch
	var total int64

	m.db.Model(&models.PhraseMatch{}).Where("is_archived = ?", false).Count(&total)

	if err := m.db.Where("is_archived = ?", false).
		Order("found_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&matches).Error; err != nil {
		return nil, 0, err
	}

	return matches, total, nil
}

// GetCrawledPages retrieves crawled pages for a job
func (m *Manager) GetCrawledPages(jobID string, limit, offset int) ([]models.CrawledPage, int64, error) {
	var pages []models.CrawledPage
	var total int64

	m.db.Model(&models.CrawledPage{}).Where("crawl_job_id = ? AND is_archived = ?", jobID, false).Count(&total)

	if err := m.db.Where("crawl_job_id = ? AND is_archived = ?", jobID, false).
		Order("crawled_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&pages).Error; err != nil {
		return nil, 0, err
	}

	return pages, total, nil
}

// GetJobStats retrieves statistics for a job
func (m *Manager) GetJobStats(jobID string) (*models.CrawlStats, error) {
	stats := &models.CrawlStats{CrawlJobID: jobID, JobID: jobID}

	// Count URLs in frontier (pending, processing, failed)
	m.db.Model(&models.FrontierURL{}).Where("crawl_job_id = ?", jobID).Count(&stats.TotalURLsInFrontier)
	m.db.Model(&models.FrontierURL{}).Where("crawl_job_id = ? AND status = ?", jobID, "pending").Count(&stats.PendingURLs)
	m.db.Model(&models.FrontierURL{}).Where("crawl_job_id = ? AND status = ?", jobID, "processing").Count(&stats.ProcessingURLs)
	// Completed URLs are deleted from frontier and stored in crawled_pages
	m.db.Model(&models.CrawledPage{}).Where("crawl_job_id = ? AND is_archived = ?", jobID, false).Count(&stats.CompletedURLs)
	m.db.Model(&models.FrontierURL{}).Where("crawl_job_id = ? AND status = ?", jobID, "failed").Count(&stats.FailedURLs)

	// Count matches
	m.db.Model(&models.PhraseMatch{}).Where("crawl_job_id = ? AND is_archived = ?", jobID, false).Count(&stats.TotalMatches)

	// Count subdomains (check discovery_job_id)
	m.db.Model(&models.Subdomain{}).Where("discovery_job_id = ?", jobID).Count(&stats.SubdomainsFound)

	// Calculate average response time (handle NULL when no rows)
	var avgResponseTime sql.NullFloat64
	m.db.Model(&models.CrawledPage{}).
		Where("crawl_job_id = ? AND response_time > 0 AND is_archived = ?", jobID, false).
		Select("AVG(response_time)").
		Scan(&avgResponseTime)
	if avgResponseTime.Valid {
		stats.AverageResponseTime = avgResponseTime.Float64
	}

	return stats, nil
}

// GetSearchPhrases retrieves all search phrases
func (m *Manager) GetSearchPhrases() ([]models.SearchPhrase, error) {
	var phrases []models.SearchPhrase
	if err := m.db.Order("created_at DESC").Find(&phrases).Error; err != nil {
		return nil, err
	}
	return phrases, nil
}

// GetRecentSearchPhrases retrieves the most recent search phrases up to the given limit
func (m *Manager) GetRecentSearchPhrases(limit int) ([]models.SearchPhrase, error) {
	var phrases []models.SearchPhrase
	if err := m.db.Order("created_at DESC").Limit(limit).Find(&phrases).Error; err != nil {
		return nil, err
	}
	return phrases, nil
}

// GetSearchPhrasesWithStats retrieves all search phrases with match and URL counts
func (m *Manager) GetSearchPhrasesWithStats() ([]models.PhraseWithStats, error) {
	var results []models.PhraseWithStats

	err := m.db.Raw(`
		SELECT 
			sp.id,
			sp.phrase,
			sp.is_active,
			sp.created_at,
			sp.crawl_job_id,
			COALESCE(stats.match_count, 0) AS match_count,
			COALESCE(stats.url_count, 0) AS url_count
		FROM search_phrases sp
		LEFT JOIN (
			SELECT 
				phrase,
				SUM(occurrences) AS match_count,
				COUNT(DISTINCT url) AS url_count
			FROM phrase_matches
			WHERE is_archived = 0
			GROUP BY phrase
		) stats ON sp.phrase = stats.phrase
		ORDER BY match_count DESC, sp.phrase ASC
	`).Scan(&results).Error

	if err != nil {
		return nil, err
	}
	return results, nil
}

// AddSearchPhrase adds a new search phrase.
// If the phrase already exists (e.g. auto-extracted), it is promoted to a
// manually-added phrase (crawl_job_id set to NULL).
func (m *Manager) AddSearchPhrase(phrase string) (*models.SearchPhrase, error) {
	// Check if the phrase already exists.
	var existing models.SearchPhrase
	err := m.db.Where("phrase = ?", phrase).First(&existing).Error
	if err == nil {
		// Promote to manual phrase if it was auto-extracted.
		if existing.CrawlJobID != nil {
			m.db.Exec("UPDATE search_phrases SET crawl_job_id = NULL, is_active = ? WHERE id = ?", true, existing.ID)
			existing.CrawlJobID = nil
			existing.IsActive = true
		}
		return &existing, nil
	}

	sp := &models.SearchPhrase{
		Phrase:   phrase,
		IsActive: true,
	}
	if err := m.db.Create(sp).Error; err != nil {
		return nil, err
	}
	return sp, nil
}

// UpdateSearchPhrase updates a search phrase's active status
func (m *Manager) UpdateSearchPhrase(id uint, isActive bool) error {
	return m.db.Model(&models.SearchPhrase{}).Where("id = ?", id).Update("is_active", isActive).Error
}

// DeleteSearchPhrase deletes a search phrase
func (m *Manager) DeleteSearchPhrase(id uint) error {
	return m.db.Delete(&models.SearchPhrase{}, "id = ?", id).Error
}

// GetJobExtractedPhrases returns search phrases that were actually found (have
// PhraseMatch records) in the given crawl job, with pagination.
// This uses the phrase_matches table to determine which phrases belong to the
// crawl, rather than relying on search_phrases.crawl_job_id (which only records
// the crawl that first created the phrase row).
func (m *Manager) GetJobExtractedPhrases(crawlJobID string, search string, limit, offset int) ([]models.SearchPhrase, int64, error) {
	var phrases []models.SearchPhrase
	var total int64

	// Subquery: distinct phrase IDs found in this crawl job's matches.
	subq := m.db.Model(&models.PhraseMatch{}).
		Select("DISTINCT search_phrase_id").
		Where("crawl_job_id = ? AND search_phrase_id IS NOT NULL", crawlJobID)

	q := m.db.Model(&models.SearchPhrase{}).Where("id IN (?)", subq)
	if search != "" {
		q = q.Where("phrase LIKE ?", "%"+search+"%")
	}
	q.Count(&total)

	dq := m.db.Where("id IN (?)", subq)
	if search != "" {
		dq = dq.Where("phrase LIKE ?", "%"+search+"%")
	}
	if err := dq.Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&phrases).Error; err != nil {
		return nil, 0, err
	}

	return phrases, total, nil
}

// GetActiveJob returns the currently active job
func (m *Manager) GetActiveJob() *models.CrawlJob {
	// Get the job directly from the engine for accurate state
	return m.engine.GetCurrentJob()
}

// GetEngineStats returns current engine statistics
func (m *Manager) GetEngineStats() map[string]interface{} {
	return m.engine.GetStats()
}

// UpdateJobSettings updates the settings for a pending job
func (m *Manager) UpdateJobSettings(jobID string, settings *models.JobSettings) error {
	var job models.CrawlJob
	if err := m.db.First(&job, "id = ?", jobID).Error; err != nil {
		return err
	}
	if job.Status != models.JobStatusPending {
		return fmt.Errorf("can only update settings for pending jobs")
	}
	if settings == nil {
		// Reset to defaults: set settings column to NULL
		return m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Update("settings", nil).Error
	}
	job.Settings = settings
	return m.db.Save(&job).Error
}

// DuplicateJob creates a new pending job by copying the target URL, domain, max depth, and settings from an existing job.
func (m *Manager) DuplicateJob(jobID string) (*models.CrawlJob, error) {
	var src models.CrawlJob
	if err := m.db.First(&src, "id = ?", jobID).Error; err != nil {
		return nil, fmt.Errorf("source job not found: %w", err)
	}

	// Deep-copy settings so the new job is independent
	var settingsCopy *models.JobSettings
	if src.Settings != nil {
		s := *src.Settings // shallow copy
		if s.SkipExtensions != nil {
			s.SkipExtensions = append([]string(nil), src.Settings.SkipExtensions...)
		}
		if s.URLIncludePatterns != nil {
			s.URLIncludePatterns = append([]string(nil), src.Settings.URLIncludePatterns...)
		}
		if s.URLExcludePatterns != nil {
			s.URLExcludePatterns = append([]string(nil), src.Settings.URLExcludePatterns...)
		}
		if s.ExtraTrackingParams != nil {
			s.ExtraTrackingParams = append([]string(nil), src.Settings.ExtraTrackingParams...)
		}
		settingsCopy = &s
	}

	newJob := &models.CrawlJob{
		TargetURL: src.TargetURL,
		Domain:    src.Domain,
		Status:    models.JobStatusPending,
		MaxDepth:  src.MaxDepth,
		Settings:  settingsCopy,
	}

	if err := m.db.Create(newJob).Error; err != nil {
		return nil, err
	}

	log.Printf("[JobManager] Duplicated job %s → %s (target: %s)", src.ID, newJob.ID, newJob.TargetURL)
	return newJob, nil
}

// GetDefaultJobSettings returns the default crawler config as JobSettings
func (m *Manager) GetDefaultJobSettings() *models.JobSettings {
	cfg := m.config.Crawler
	maxConcurrent := cfg.MaxConcurrentRequests
	timeout := int(cfg.RequestTimeout.Seconds())
	delayMs := int(cfg.PolitenessDelay.Milliseconds())
	maxDepth := cfg.MaxDepth
	maxPages := cfg.MaxPages
	userAgent := cfg.UserAgent
	maxRetries := cfg.MaxRetries
	skipContentDup := cfg.SkipContentDuplicates
	useHeadless := cfg.UseHeadlessBrowser
	headlessSelector := cfg.HeadlessWaitSelector
	enableSemantic := cfg.EnableSemanticSearch
	afterCrawlScript := cfg.AfterCrawlScript
	enableWordExtraction := cfg.EnableWordExtraction
	useCrawlPhrasesOnly := cfg.UseCrawlPhrasesOnly

	return &models.JobSettings{
		MaxConcurrentRequests: &maxConcurrent,
		RequestTimeoutSec:     &timeout,
		PolitenessDelayMs:     &delayMs,
		MaxDepth:              &maxDepth,
		MaxPages:              &maxPages,
		UserAgent:             &userAgent,
		MaxRetries:            &maxRetries,
		SkipContentDuplicates: &skipContentDup,
		UseHeadlessBrowser:    &useHeadless,
		HeadlessWaitSelector:  &headlessSelector,
		EnableSemanticSearch:  &enableSemantic,
		AfterCrawlScript:      &afterCrawlScript,
		EnableWordExtraction:  &enableWordExtraction,
		UseCrawlPhrasesOnly:   &useCrawlPhrasesOnly,
		SkipExtensions:        cfg.SkipExtensions,
	}
}

// SemanticSearch performs a vector-based semantic search across all crawled pages.
// If crawlJobID is non-empty only embeddings from that job are searched.
func (m *Manager) SemanticSearch(query string, topK int, crawlJobID string) ([]models.SemanticSearchResult, error) {
	searcher := m.engine.GetSemanticSearcher()
	if searcher == nil {
		return nil, fmt.Errorf("semantic search is not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if crawlJobID != "" {
		return searcher.SearchForCrawlJob(ctx, query, topK, crawlJobID)
	}
	return searcher.Search(ctx, query, topK)
}

// RebuildSemanticIndex triggers a rebuild of the FAISS index.
func (m *Manager) RebuildSemanticIndex() error {
	return m.RebuildSemanticIndexForCrawlJob("")
}

// RebuildSemanticIndexForCrawlJob rebuilds the FAISS index.
// If crawlJobID is empty the global index is rebuilt; otherwise a per-crawl index.
func (m *Manager) RebuildSemanticIndexForCrawlJob(crawlJobID string) error {
	searcher := m.engine.GetSemanticSearcher()
	if searcher == nil {
		return fmt.Errorf("semantic search is not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if crawlJobID != "" {
		return searcher.RebuildIndexForCrawlJob(ctx, crawlJobID)
	}
	return searcher.RebuildIndex(ctx)
}

// GetSemanticSearchStats returns semantic search statistics.
func (m *Manager) GetSemanticSearchStats() map[string]interface{} {
	searcher := m.engine.GetSemanticSearcher()
	if searcher == nil {
		return map[string]interface{}{
			"available":       false,
			"embedding_count": 0,
			"has_index":       false,
		}
	}
	return map[string]interface{}{
		"available":       true,
		"embedding_count": searcher.EmbeddingCount(),
		"has_index":       searcher.HasIndex(),
	}
}

// SearchPhraseMatches performs TF-IDF ranked keyword search over phrase_matches.
//
// Scoring model (per page):
//   - TF(t,d) = 1 + log10(occurrences)          (log-scaled term frequency)
//   - IDF(t)  = log10(N / df(t))                (inverse document frequency)
//   - score   = Σ TF(t,d) × IDF(t)              summed across ALL matched query terms
//   - Exact-match boost: if the indexed phrase exactly equals a query n-gram → ×3
//   - Match-type boost: URL matches ×2, anchor matches ×1.5
//   - Multi-term bonus: pages matching K distinct query n-grams get ×(1 + 0.5*(K-1))
//
// Results are grouped by page URL — each result lists ALL matched phrases.
// If crawlJobID is non-empty, results are restricted to that crawl job.
func (m *Manager) SearchPhraseMatches(query string, crawlJobID string, limit, offset int) ([]models.SearchResult, int64, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, 0, fmt.Errorf("search query cannot be empty")
	}

	// Normalise query using the same pipeline as the word extractor
	// (lowercase, strip punctuation, remove stop words, stem).
	var words []string
	if m.wordExtractor != nil {
		words = m.wordExtractor.NormalizeQueryTokens(query)
	} else {
		words = strings.Fields(strings.ToLower(query))
		if m.stemmer != nil && m.stemmer.Enabled() && len(words) > 0 {
			words = m.stemmer.StemTokens(words)
		}
	}

	ngrams := generateNgrams(words)
	if len(ngrams) == 0 {
		return nil, 0, nil
	}

	// Build a set of query n-grams for exact-match detection.
	ngramSet := make(map[string]bool, len(ngrams))
	for _, ng := range ngrams {
		ngramSet[ng] = true
	}

	// ── 1. Total document count (N) for IDF ──────────────────────────
	var totalDocs int64
	docCountQ := m.db.Model(&models.CrawledPage{}).Where("is_archived = ?", false)
	if crawlJobID != "" {
		docCountQ = docCountQ.Where("crawl_job_id = ?", crawlJobID)
	}
	docCountQ.Count(&totalDocs)
	if totalDocs == 0 {
		totalDocs = 1
	}

	// ── 2. Fetch matching phrase_match rows ──────────────────────────
	var conditions []string
	var ngramArgs []interface{}
	for _, ngram := range ngrams {
		conditions = append(conditions, "pm.phrase LIKE ?")
		ngramArgs = append(ngramArgs, ngram+"%")
	}
	ngramClause := strings.Join(conditions, " OR ")

	baseFilter := "pm.is_archived = 0"
	var baseArgs []interface{}
	if crawlJobID != "" {
		baseFilter += " AND pm.crawl_job_id = ?"
		baseArgs = append(baseArgs, crawlJobID)
	}

	allArgs := append(baseArgs, ngramArgs...)

	selectSQL := fmt.Sprintf(`
		SELECT 
			pm.phrase,
			pm.url,
			pm.match_type,
			pm.context,
			pm.occurrences,
			pm.crawl_job_id,
			pm.page_id,
			COALESCE(cj.domain, '') as domain,
			pm.found_at
		FROM phrase_matches pm
		LEFT JOIN crawl_jobs cj ON pm.crawl_job_id = cj.id
		WHERE %s AND (%s)
	`, baseFilter, ngramClause)

	type rawMatch struct {
		Phrase      string    `gorm:"column:phrase"`
		URL         string    `gorm:"column:url"`
		MatchType   string    `gorm:"column:match_type"`
		Context     string    `gorm:"column:context"`
		Occurrences int       `gorm:"column:occurrences"`
		CrawlJobID  string    `gorm:"column:crawl_job_id"`
		PageID      uint      `gorm:"column:page_id"`
		Domain      string    `gorm:"column:domain"`
		FoundAt     time.Time `gorm:"column:found_at"`
	}

	var rows []rawMatch
	if err := m.db.Raw(selectSQL, allArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	if len(rows) == 0 {
		return nil, 0, nil
	}

	// ── 3. Compute document frequency (df) per phrase ────────────────
	phraseSet := make(map[string]struct{})
	for _, r := range rows {
		phraseSet[r.Phrase] = struct{}{}
	}
	phraseList := make([]string, 0, len(phraseSet))
	for p := range phraseSet {
		phraseList = append(phraseList, p)
	}

	type dfRow struct {
		Phrase string `gorm:"column:phrase"`
		Df     int64  `gorm:"column:df"`
	}
	dfMap := make(map[string]int64, len(phraseList))

	for i := 0; i < len(phraseList); i += 500 {
		end := i + 500
		if end > len(phraseList) {
			end = len(phraseList)
		}
		chunk := phraseList[i:end]

		var dfRows []dfRow
		dfSQL := "SELECT phrase, COUNT(DISTINCT page_id) as df FROM phrase_matches WHERE phrase IN ? AND is_archived = 0"
		dfArgs := []interface{}{chunk}
		if crawlJobID != "" {
			dfSQL += " AND crawl_job_id = ?"
			dfArgs = append(dfArgs, crawlJobID)
		}
		dfSQL += " GROUP BY phrase"
		m.db.Raw(dfSQL, dfArgs...).Scan(&dfRows)
		for _, d := range dfRows {
			dfMap[d.Phrase] = d.Df
		}
	}

	// ── 4. Group by URL, aggregate scores across ALL matched phrases ─
	// Each page gets a combined score from every matched phrase.
	type phraseInfo struct {
		Phrase      string
		MatchType   string
		Context     string
		Occurrences int
		Score       float64
		TF          float64
		IDF         float64
		TFIDF       float64
		MatchBoost  float64
		ExactBoost  float64
	}
	type pageResult struct {
		URL        string
		Domain     string
		CrawlJobID string
		FoundAt    time.Time
		TotalScore float64
		// key: phrase+"|"+matchType to deduplicate
		Phrases map[string]*phraseInfo
		// track which query n-grams matched (for multi-term bonus)
		MatchedNgrams map[string]bool
	}

	pageMap := make(map[string]*pageResult)

	for _, r := range rows {
		pr, exists := pageMap[r.URL]
		if !exists {
			pr = &pageResult{
				URL:           r.URL,
				Domain:        r.Domain,
				CrawlJobID:    r.CrawlJobID,
				FoundAt:       r.FoundAt,
				Phrases:       make(map[string]*phraseInfo),
				MatchedNgrams: make(map[string]bool),
			}
			pageMap[r.URL] = pr
		}
		if r.FoundAt.After(pr.FoundAt) {
			pr.FoundAt = r.FoundAt
		}

		// TF-IDF score for this (phrase, page) pair
		tf := 1.0
		if r.Occurrences > 1 {
			tf = 1.0 + math.Log10(float64(r.Occurrences))
		}

		df := dfMap[r.Phrase]
		if df < 1 {
			df = 1
		}
		idf := math.Log10(float64(totalDocs) / float64(df))
		if idf < 0 {
			idf = 0
		}

		baseTFIDF := tf * idf

		// Match-type boost
		matchBoost := 1.0
		switch r.MatchType {
		case string(models.MatchTypeURL):
			matchBoost = 2.0
		case string(models.MatchTypeAnchor):
			matchBoost = 1.5
		}

		// Exact-match boost
		exactBoost := 1.0
		if ngramSet[r.Phrase] {
			exactBoost = 3.0
		}

		finalScore := baseTFIDF * matchBoost * exactBoost

		// Track which query n-gram this phrase satisfies (for multi-term bonus)
		for _, ng := range ngrams {
			if strings.HasPrefix(r.Phrase, ng) {
				pr.MatchedNgrams[ng] = true
			}
		}

		// Accumulate into page-level phrase info
		pKey := r.Phrase + "|" + r.MatchType
		pi, pExists := pr.Phrases[pKey]
		if !pExists {
			pi = &phraseInfo{
				Phrase:     r.Phrase,
				MatchType:  r.MatchType,
				Context:    r.Context,
				MatchBoost: matchBoost,
				ExactBoost: exactBoost,
			}
			pr.Phrases[pKey] = pi
		}
		pi.Occurrences += r.Occurrences
		pi.TF += tf
		pi.IDF = idf // same for all rows of same phrase
		pi.TFIDF += baseTFIDF
		pi.Score += finalScore
		pr.TotalScore += finalScore
	}

	// Apply multi-term bonus: pages matching more distinct query n-grams
	// get a multiplier of 1 + 0.5*(K-1) where K is the number of matched n-grams.
	for _, pr := range pageMap {
		k := len(pr.MatchedNgrams)
		if k > 1 {
			pr.TotalScore *= 1.0 + 0.5*float64(k-1)
		}
	}

	// ── 5. Sort by score descending ──────────────────────────────────
	sorted := make([]*pageResult, 0, len(pageMap))
	for _, pr := range pageMap {
		sorted = append(sorted, pr)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].TotalScore != sorted[j].TotalScore {
			return sorted[i].TotalScore > sorted[j].TotalScore
		}
		return sorted[i].FoundAt.After(sorted[j].FoundAt)
	})

	maxScore := 0.0
	if len(sorted) > 0 {
		maxScore = sorted[0].TotalScore
	}

	// ── 6. Paginate and build output ─────────────────────────────────
	var total int64 = int64(len(sorted))
	if offset >= len(sorted) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(sorted) {
		end = len(sorted)
	}
	page := sorted[offset:end]

	results := make([]models.SearchResult, 0, len(page))
	for _, pr := range page {
		pct := 0.0
		if maxScore > 0 {
			pct = (pr.TotalScore / maxScore) * 100.0
		}

		// Build matched phrases list sorted by individual score descending.
		mpList := make([]models.MatchedPhrase, 0, len(pr.Phrases))
		for _, pi := range pr.Phrases {
			mpList = append(mpList, models.MatchedPhrase{
				Phrase:      pi.Phrase,
				MatchType:   pi.MatchType,
				Context:     pi.Context,
				Occurrences: pi.Occurrences,
				TF:          math.Round(pi.TF*1000) / 1000,
				IDF:         math.Round(pi.IDF*1000) / 1000,
				TFIDF:       math.Round(pi.TFIDF*1000) / 1000,
				MatchBoost:  pi.MatchBoost,
				ExactBoost:  pi.ExactBoost,
				PhraseScore: math.Round(pi.Score*1000) / 1000,
			})
		}
		sort.Slice(mpList, func(i, j int) bool {
			if mpList[i].PhraseScore != mpList[j].PhraseScore {
				return mpList[i].PhraseScore > mpList[j].PhraseScore
			}
			iExact := ngramSet[mpList[i].Phrase]
			jExact := ngramSet[mpList[j].Phrase]
			if iExact != jExact {
				return iExact
			}
			return mpList[i].Occurrences > mpList[j].Occurrences
		})

		// Primary phrase = best match
		primary := mpList[0]

		// Total occurrences across all matched phrases
		totalOcc := 0
		for _, mp := range mpList {
			totalOcc += mp.Occurrences
		}

		// Multi-term bonus info
		k := len(pr.MatchedNgrams)
		multiTermBonus := 1.0
		if k > 1 {
			multiTermBonus = 1.0 + 0.5*float64(k-1)
		}

		// rawScore = sum of phrase scores before multi-term bonus
		rawScore := 0.0
		for _, mp := range mpList {
			rawScore += mp.PhraseScore
		}

		results = append(results, models.SearchResult{
			URL:          pr.URL,
			Domain:       pr.Domain,
			CrawlJobID:   pr.CrawlJobID,
			FoundAt:      pr.FoundAt.Format("2006-01-02 15:04:05"),
			Score:        math.Round(pr.TotalScore*1000) / 1000,
			ScorePercent: math.Round(pct*10) / 10,
			ScoreDetail: models.ScoreBreakdown{
				TotalDocs:      totalDocs,
				MatchedTerms:   k,
				MultiTermBonus: math.Round(multiTermBonus*1000) / 1000,
				RawScore:       math.Round(rawScore*1000) / 1000,
				FinalScore:     math.Round(pr.TotalScore*1000) / 1000,
			},
			MatchedPhrases: mpList,
			Phrase:         primary.Phrase,
			MatchType:      primary.MatchType,
			Context:        primary.Context,
			Occurrences:    totalOcc,
		})
	}

	return results, total, nil
}

// generateNgrams generates all n-grams from a list of words
func generateNgrams(words []string) []string {
	if len(words) == 0 {
		return nil
	}

	var ngrams []string
	seen := make(map[string]bool)

	// Generate n-grams from length len(words) down to 1
	for n := len(words); n >= 1; n-- {
		for i := 0; i <= len(words)-n; i++ {
			ngram := strings.Join(words[i:i+n], " ")
			if !seen[ngram] {
				ngrams = append(ngrams, ngram)
				seen[ngram] = true
			}
		}
	}

	return ngrams
}

// Shutdown gracefully shuts down the job manager
func (m *Manager) Shutdown() {
	m.StopSubdomainDiscovery()
	if m.activeJob != nil {
		m.StopJob()
	}
	m.subdomainScanner.Shutdown()
}

// Helper function to clean domain (exposed for use)
func CleanDomain(domain string) string {
	return modules.CleanDomain(domain)
}
