package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// JobType represents the type of job
type JobType string

const (
	JobTypeDiscovery JobType = "discovery" // Subdomain discovery job
	JobTypeCrawl     JobType = "crawl"     // Actual crawling job
)

// JobStatus represents the current state of a job
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusPaused    JobStatus = "paused"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

// DiscoveryJob represents a subdomain discovery job for a domain
type DiscoveryJob struct {
	ID                string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	Domain            string    `gorm:"type:varchar(255);index;not null" json:"domain"`
	Status            JobStatus `gorm:"type:varchar(20);index;default:'pending'" json:"status"`
	SubdomainsFound   int       `gorm:"default:0" json:"subdomains_found"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	StartedAt         *time.Time `json:"started_at"`
	CompletedAt       *time.Time `json:"completed_at"`
	ErrorMessage      string    `gorm:"type:text" json:"error_message,omitempty"`
}

// BeforeCreate generates UUID for new discovery jobs
func (j *DiscoveryJob) BeforeCreate(tx *gorm.DB) error {
	if j.ID == "" {
		j.ID = uuid.New().String()
	}
	return nil
}

// CrawlJob represents a crawling job for a specific target (domain or subdomain)
type CrawlJob struct {
	ID             string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	DiscoveryJobID string    `gorm:"type:varchar(36);index" json:"discovery_job_id,omitempty"`
	TargetURL      string    `gorm:"type:varchar(512);not null" json:"target_url"`
	Domain         string    `gorm:"type:varchar(255);index;not null" json:"domain"`
	Status         JobStatus `gorm:"type:varchar(20);index;default:'pending'" json:"status"`
	TotalURLs      int       `gorm:"default:0" json:"total_urls"`
	CrawledURLs    int       `gorm:"default:0" json:"crawled_urls"`
	FoundMatches   int       `gorm:"default:0" json:"found_matches"`
	MaxDepth       int       `gorm:"default:10" json:"max_depth"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	StartedAt      *time.Time `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at"`
	ErrorMessage   string    `gorm:"type:text" json:"error_message,omitempty"`
}

// BeforeCreate generates UUID for new crawl jobs
func (j *CrawlJob) BeforeCreate(tx *gorm.DB) error {
	if j.ID == "" {
		j.ID = uuid.New().String()
	}
	return nil
}

// Subdomain represents a discovered subdomain
type Subdomain struct {
	ID               uint         `gorm:"primaryKey" json:"id"`
	DiscoveryJobID   string       `gorm:"type:varchar(36);index;not null" json:"discovery_job_id"`
	Domain           string       `gorm:"type:varchar(255);index;not null" json:"domain"`
	Subdomain        string       `gorm:"type:varchar(255);not null" json:"subdomain"`
	FullURL          string       `gorm:"type:varchar(512)" json:"full_url"`
	IPAddress        string       `gorm:"type:varchar(45)" json:"ip_address,omitempty"`
	IsActive         bool         `gorm:"default:true" json:"is_active"`
	CrawlJobID       *string      `gorm:"type:varchar(36);index" json:"crawl_job_id,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	DiscoveryJob     DiscoveryJob `gorm:"foreignKey:DiscoveryJobID" json:"-"`
}

// CrawledPage represents a crawled web page
type CrawledPage struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	CrawlJobID    string    `gorm:"type:varchar(36);index;not null" json:"crawl_job_id"`
	URL           string    `gorm:"type:varchar(2048);index:idx_url,length:255;not null" json:"url"`
	URLHash       string    `gorm:"type:varchar(64);uniqueIndex;not null" json:"url_hash"`
	NormalizedURL string    `gorm:"type:varchar(2048)" json:"normalized_url"`
	Title         string    `gorm:"type:varchar(512)" json:"title,omitempty"`
	StatusCode    int       `json:"status_code"`
	ContentType   string    `gorm:"type:varchar(128)" json:"content_type,omitempty"`
	ContentLength int64     `json:"content_length"`
	Depth         int       `json:"depth"`
	ParentURL     string    `gorm:"type:varchar(2048)" json:"parent_url,omitempty"`
	CrawledAt     time.Time `json:"crawled_at"`
	ResponseTime  int64     `json:"response_time_ms"`
	ErrorMessage  string    `gorm:"type:text" json:"error_message,omitempty"`
	CrawlJob      CrawlJob  `gorm:"foreignKey:CrawlJobID" json:"-"`
}

// FrontierURL represents a URL in the crawl frontier queue
type FrontierURL struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	CrawlJobID    string    `gorm:"type:varchar(36);index:idx_job_status;not null" json:"crawl_job_id"`
	URL           string    `gorm:"type:varchar(2048);not null" json:"url"`
	URLHash       string    `gorm:"type:varchar(64);uniqueIndex;not null" json:"url_hash"`
	NormalizedURL string    `gorm:"type:varchar(2048)" json:"normalized_url"`
	Depth         int       `json:"depth"`
	Priority      int       `gorm:"index;default:0" json:"priority"`
	Status        string    `gorm:"type:varchar(20);index:idx_job_status;default:'pending'" json:"status"` // pending, processing, completed, failed
	ParentURL     string    `gorm:"type:varchar(2048)" json:"parent_url,omitempty"`
	RetryCount    int       `gorm:"default:0" json:"retry_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	CrawlJob      CrawlJob  `gorm:"foreignKey:CrawlJobID" json:"-"`
}

// FrontierStatus constants
const (
	FrontierStatusPending    = "pending"
	FrontierStatusProcessing = "processing"
	FrontierStatusCompleted  = "completed"
	FrontierStatusFailed     = "failed"
)

// PhraseMatch represents a detected phrase in crawled content
type PhraseMatch struct {
	ID          uint        `gorm:"primaryKey" json:"id"`
	CrawlJobID  string      `gorm:"type:varchar(36);index;not null" json:"crawl_job_id"`
	PageID      uint        `gorm:"index" json:"page_id"`
	URL         string      `gorm:"type:varchar(2048);not null" json:"url"`
	Phrase      string      `gorm:"type:varchar(255);index;not null" json:"phrase"`
	Context     string      `gorm:"type:text" json:"context,omitempty"`
	Occurrences int         `gorm:"default:1" json:"occurrences"`
	FoundAt     time.Time   `json:"found_at"`
	CrawlJob    CrawlJob    `gorm:"foreignKey:CrawlJobID" json:"-"`
	Page        CrawledPage `gorm:"foreignKey:PageID" json:"-"`
}

// SearchPhrase represents a phrase to search for during crawling
type SearchPhrase struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Phrase    string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"phrase"`
	IsActive  bool      `gorm:"default:true" json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

// CrawlStats holds statistics for a crawl job
type CrawlStats struct {
	CrawlJobID          string  `json:"crawl_job_id"`
	JobID               string  `json:"job_id"` // Alias for backwards compatibility
	TotalURLsInFrontier int64   `json:"total_urls_in_frontier"`
	PendingURLs         int64   `json:"pending_urls"`
	ProcessingURLs      int64   `json:"processing_urls"`
	CompletedURLs       int64   `json:"completed_urls"`
	FailedURLs          int64   `json:"failed_urls"`
	TotalMatches        int64   `json:"total_matches"`
	SubdomainsFound     int64   `json:"subdomains_found"`
	AverageResponseTime float64 `json:"average_response_time_ms"`
}
