package jobs

import (
	"context"
	"fmt"
	"log"
	"sync"

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

	return &Manager{
		db:               db,
		config:           cfg,
		engine:           engine,
		subdomainScanner: scanner,
	}
}

// CreateJob creates a new crawl job for a domain
func (m *Manager) CreateJob(domain string, maxDepth int) (*models.CrawlJob, error) {
	// Clean domain
	domain = modules.CleanDomain(domain)

	job := &models.CrawlJob{
		Domain:    domain,
		TargetURL: "https://" + domain,
		Status:    models.JobStatusPending,
		MaxDepth:  maxDepth,
	}

	if err := m.db.Create(job).Error; err != nil {
		return nil, err
	}

	log.Printf("[JobManager] Created job %s for domain %s", job.ID, domain)
	return job, nil
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

	if m.activeJob == nil {
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

	if m.activeJob == nil {
		return fmt.Errorf("no active job")
	}

	m.engine.Pause()
	return nil
}

// ResumeJob resumes the current crawl job
func (m *Manager) ResumeJob() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeJob == nil {
		return fmt.Errorf("no active job")
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
	var job models.CrawlJob
	if err := m.db.First(&job, "id = ?", jobID).Error; err != nil {
		return err
	}

	m.subdomainCtx, m.subdomainCancel = context.WithCancel(context.Background())

	go func() {
		// Add the main domain itself as a subdomain
		mainSub := &models.Subdomain{
			DiscoveryJobID: jobID,
			Domain:         job.Domain,
			Subdomain:      job.Domain,
			FullURL:        "https://" + job.Domain,
			IsActive:       true,
		}
		m.db.Create(mainSub)

		m.subdomainScanner.DiscoverSubdomains(job.Domain, func(subdomain string) {
			// Save discovered subdomain
			sub := &models.Subdomain{
				DiscoveryJobID: jobID,
				Domain:         job.Domain,
				Subdomain:      subdomain,
				FullURL:        "https://" + subdomain,
				IsActive:       true,
			}
			m.db.Create(sub)
			log.Printf("[JobManager] Discovered subdomain: %s", subdomain)
		})
	}()

	return nil
}

// StopSubdomainDiscovery stops subdomain discovery
func (m *Manager) StopSubdomainDiscovery() {
	if m.subdomainCancel != nil {
		m.subdomainCancel()
	}
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

	m.db.Model(&models.PhraseMatch{}).Where("crawl_job_id = ?", jobID).Count(&total)

	if err := m.db.Where("crawl_job_id = ?", jobID).
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

	m.db.Model(&models.PhraseMatch{}).Count(&total)

	if err := m.db.Order("found_at DESC").
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

	m.db.Model(&models.CrawledPage{}).Where("crawl_job_id = ?", jobID).Count(&total)

	if err := m.db.Where("crawl_job_id = ?", jobID).
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

	// Count URLs in frontier
	m.db.Model(&models.FrontierURL{}).Where("crawl_job_id = ?", jobID).Count(&stats.TotalURLsInFrontier)
	m.db.Model(&models.FrontierURL{}).Where("crawl_job_id = ? AND status = ?", jobID, "pending").Count(&stats.PendingURLs)
	m.db.Model(&models.FrontierURL{}).Where("crawl_job_id = ? AND status = ?", jobID, "processing").Count(&stats.ProcessingURLs)
	m.db.Model(&models.FrontierURL{}).Where("crawl_job_id = ? AND status = ?", jobID, "completed").Count(&stats.CompletedURLs)
	m.db.Model(&models.FrontierURL{}).Where("crawl_job_id = ? AND status = ?", jobID, "failed").Count(&stats.FailedURLs)

	// Count matches
	m.db.Model(&models.PhraseMatch{}).Where("crawl_job_id = ?", jobID).Count(&stats.TotalMatches)

	// Count subdomains (check discovery_job_id)
	m.db.Model(&models.Subdomain{}).Where("discovery_job_id = ?", jobID).Count(&stats.SubdomainsFound)

	// Calculate average response time
	var avgResponseTime float64
	m.db.Model(&models.CrawledPage{}).
		Where("crawl_job_id = ? AND response_time > 0", jobID).
		Select("AVG(response_time)").
		Scan(&avgResponseTime)
	stats.AverageResponseTime = avgResponseTime

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

// AddSearchPhrase adds a new search phrase
func (m *Manager) AddSearchPhrase(phrase string) (*models.SearchPhrase, error) {
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

// GetActiveJob returns the currently active job
func (m *Manager) GetActiveJob() *models.CrawlJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeJob
}

// GetEngineStats returns current engine statistics
func (m *Manager) GetEngineStats() map[string]interface{} {
	return m.engine.GetStats()
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
