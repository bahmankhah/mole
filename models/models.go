package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
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

// MatchType represents where a phrase was found
type MatchType string

const (
	MatchTypeContent MatchType = "content" // Found in page body content
	MatchTypeURL     MatchType = "url"     // Found in the page URL
	MatchTypeAnchor  MatchType = "anchor"  // Found in anchor text pointing to the page
)

// JobSettings holds per-job crawler settings that override global defaults.
// A nil/null value means "use global defaults".
type JobSettings struct {
	MaxConcurrentRequests *int     `json:"max_concurrent_requests,omitempty"`
	RequestTimeoutSec     *int     `json:"request_timeout_seconds,omitempty"`
	PolitenessDelayMs     *int     `json:"politeness_delay_ms,omitempty"`
	MaxDepth              *int     `json:"max_depth,omitempty"`
	MaxPages              *int     `json:"max_pages,omitempty"` // 0 = unlimited
	UserAgent             *string  `json:"user_agent,omitempty"`
	MaxRetries            *int     `json:"max_retries,omitempty"`
	RespectRobotsTxt      *bool    `json:"respect_robots_txt,omitempty"`
	SkipContentDuplicates *bool    `json:"skip_content_duplicates,omitempty"` // Skip pages whose body hash already seen
	SkipExtensions        []string `json:"skip_extensions,omitempty"`
	URLIncludePatterns    []string `json:"url_include_patterns,omitempty"`   // Regex; if set, only matching URLs are crawled
	URLExcludePatterns    []string `json:"url_exclude_patterns,omitempty"`   // Regex; skipped if include is set
	ExtraTrackingParams   []string `json:"extra_tracking_params,omitempty"`  // Extra query params to strip from URLs
	UseHeadlessBrowser    *bool    `json:"use_headless_browser,omitempty"`   // Use headless browser for JS-rendered pages
	HeadlessWaitSelector  *string  `json:"headless_wait_selector,omitempty"` // CSS selector to wait for before capturing
	EnableSemanticSearch  *bool    `json:"enable_semantic_search,omitempty"` // Enable semantic vector search for this job
	SaveTextContent       *bool    `json:"save_text_content,omitempty"`      // Save extracted text content of pages
	EnableWordExtraction  *bool    `json:"enable_word_extraction,omitempty"` // Extract words and build inverted index
	EnableStemming        *bool    `json:"enable_stemming,omitempty"`        // Stem/lemmatise words during indexing and search
	EnableLemmatization   *bool    `json:"enable_lemmatization,omitempty"`   // Use lemmatization vs pure stemming
	DefaultLanguage       *string  `json:"default_language,omitempty"`       // Language for stemming: "fa" or "en"
	UseCrawlPhrasesOnly   *bool    `json:"use_crawl_phrases_only,omitempty"` // true = only match crawl-extracted words; false = also match manual phrases
}

// Value implements driver.Valuer for GORM JSON storage
func (s JobSettings) Value() (driver.Value, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner for GORM JSON storage
func (s *JobSettings) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case string:
		bytes = []byte(v)
	case []byte:
		bytes = v
	default:
		return fmt.Errorf("cannot scan %T into JobSettings", value)
	}
	return json.Unmarshal(bytes, s)
}

// DiscoveryJob represents a subdomain discovery job for a domain
type DiscoveryJob struct {
	ID              string     `gorm:"type:varchar(36);primaryKey" json:"id"`
	Domain          string     `gorm:"type:varchar(255);index;not null" json:"domain"`
	Status          JobStatus  `gorm:"type:varchar(20);index;default:'pending'" json:"status"`
	SubdomainsFound int        `gorm:"default:0" json:"subdomains_found"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	ErrorMessage    string     `gorm:"type:text" json:"error_message,omitempty"`
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
	ID             string       `gorm:"type:varchar(36);primaryKey" json:"id"`
	DiscoveryJobID string       `gorm:"type:varchar(36);index" json:"discovery_job_id,omitempty"`
	TargetURL      string       `gorm:"type:varchar(512);not null" json:"target_url"`
	Domain         string       `gorm:"type:varchar(255);index;not null" json:"domain"`
	Status         JobStatus    `gorm:"type:varchar(20);index;default:'pending'" json:"status"`
	TotalURLs      int          `gorm:"default:0" json:"total_urls"`
	CrawledURLs    int          `gorm:"default:0" json:"crawled_urls"`
	FoundMatches   int          `gorm:"default:0" json:"found_matches"`
	MaxDepth       int          `gorm:"default:10" json:"max_depth"`
	Settings       *JobSettings `gorm:"type:json" json:"settings"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	StartedAt      *time.Time   `json:"started_at"`
	CompletedAt    *time.Time   `json:"completed_at"`
	ErrorMessage   string       `gorm:"type:text" json:"error_message,omitempty"`
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
	ID             uint         `gorm:"primaryKey" json:"id"`
	DiscoveryJobID string       `gorm:"type:varchar(36);index;not null" json:"discovery_job_id"`
	Domain         string       `gorm:"type:varchar(255);index;not null" json:"domain"`
	Subdomain      string       `gorm:"type:varchar(255);not null" json:"subdomain"`
	FullURL        string       `gorm:"type:varchar(512)" json:"full_url"`
	IPAddress      string       `gorm:"type:varchar(45)" json:"ip_address,omitempty"`
	IsActive       bool         `gorm:"default:true" json:"is_active"`
	CrawlJobID     *string      `gorm:"type:varchar(36);index" json:"crawl_job_id,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	DiscoveryJob   DiscoveryJob `gorm:"foreignKey:DiscoveryJobID;constraint:OnDelete:CASCADE" json:"-"`
	CrawlJob       *CrawlJob    `gorm:"foreignKey:CrawlJobID;constraint:OnDelete:SET NULL" json:"-"`
}

// CrawledPage represents a crawled web page
type CrawledPage struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	CrawlJobID    string    `gorm:"type:varchar(36);uniqueIndex:idx_job_url_hash;not null" json:"crawl_job_id"`
	URL           string    `gorm:"type:varchar(2048);index:idx_url,length:255;not null" json:"url"`
	URLHash       string    `gorm:"type:varchar(64);uniqueIndex:idx_job_url_hash;not null" json:"url_hash"`
	DocHash       string    `gorm:"type:varchar(64)" json:"doc_hash"`
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
	TextContent   *string   `gorm:"type:longtext" json:"text_content,omitempty"`
	IsArchived    bool      `gorm:"default:false;index" json:"is_archived"`
	CrawlJob      CrawlJob  `gorm:"foreignKey:CrawlJobID;constraint:OnDelete:CASCADE" json:"-"`
}

// FrontierURL represents a URL in the crawl frontier queue
type FrontierURL struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	CrawlJobID    string    `gorm:"type:varchar(36);index:idx_job_status;uniqueIndex:idx_job_url_hash;not null" json:"crawl_job_id"`
	URL           string    `gorm:"type:varchar(2048);not null" json:"url"`
	URLHash       string    `gorm:"type:varchar(64);uniqueIndex:idx_job_url_hash;not null" json:"url_hash"`
	NormalizedURL string    `gorm:"type:varchar(2048)" json:"normalized_url"`
	Depth         int       `json:"depth"`
	Priority      int       `gorm:"index;default:0" json:"priority"`
	Status        string    `gorm:"type:varchar(20);index:idx_job_status;default:'pending'" json:"status"` // pending, processing, failed
	ParentURL     string    `gorm:"type:varchar(2048)" json:"parent_url,omitempty"`
	AnchorText    string    `gorm:"type:text" json:"anchor_text,omitempty"`
	RetryCount    int       `gorm:"default:0" json:"retry_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	CrawlJob      CrawlJob  `gorm:"foreignKey:CrawlJobID;constraint:OnDelete:CASCADE" json:"-"`
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
	ID             uint          `gorm:"primaryKey" json:"id"`
	CrawlJobID     string        `gorm:"type:varchar(36);index;not null" json:"crawl_job_id"`
	PageID         uint          `gorm:"index" json:"page_id"`
	SearchPhraseID *uint         `gorm:"index" json:"search_phrase_id"`
	URL            string        `gorm:"type:varchar(2048);not null" json:"url"`
	Phrase         string        `gorm:"type:varchar(255);index:idx_phrase;not null" json:"phrase"`
	MatchType      MatchType     `gorm:"type:varchar(20);index;default:'content'" json:"match_type"`
	Context        string        `gorm:"type:text" json:"context,omitempty"`
	Occurrences    int           `gorm:"default:1" json:"occurrences"`
	FoundAt        time.Time     `json:"found_at"`
	IsArchived     bool          `gorm:"default:false;index" json:"is_archived"`
	CrawlJob       CrawlJob      `gorm:"foreignKey:CrawlJobID;constraint:OnDelete:CASCADE" json:"-"`
	Page           CrawledPage   `gorm:"foreignKey:PageID;constraint:OnDelete:CASCADE" json:"-"`
	SearchPhrase   *SearchPhrase `gorm:"foreignKey:SearchPhraseID;constraint:OnDelete:SET NULL" json:"-"`
}

// SearchPhrase represents a phrase to search for during crawling
type SearchPhrase struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Phrase     string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"phrase"`
	IsActive   bool      `gorm:"default:true" json:"is_active"`
	CrawlJobID *string   `gorm:"type:varchar(36);index" json:"crawl_job_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	CrawlJob   *CrawlJob `gorm:"foreignKey:CrawlJobID;constraint:OnDelete:SET NULL" json:"-"`
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

// PhraseWithStats represents a search phrase with its match count for the phrases listing page
type PhraseWithStats struct {
	ID         uint    `json:"id"`
	Phrase     string  `json:"phrase"`
	IsActive   bool    `json:"is_active"`
	CreatedAt  string  `json:"created_at"`
	MatchCount int64   `json:"match_count"`
	URLCount   int64   `json:"url_count"`
	CrawlJobID *string `json:"crawl_job_id,omitempty"`
}

// PageEmbedding stores the vector embedding for a crawled page
type PageEmbedding struct {
	ID         uint        `gorm:"primaryKey" json:"id"`
	CrawlJobID string      `gorm:"type:varchar(36);index;not null" json:"crawl_job_id"`
	PageID     uint        `gorm:"uniqueIndex;not null" json:"page_id"`
	URL        string      `gorm:"type:varchar(2048);not null" json:"url"`
	Title      string      `gorm:"type:varchar(512)" json:"title"`
	Embedding  []byte      `gorm:"type:mediumblob" json:"-"`          // float32 array serialized as bytes
	TextHash   string      `gorm:"type:varchar(64)" json:"text_hash"` // to avoid re-embedding identical content
	CreatedAt  time.Time   `json:"created_at"`
	CrawlJob   CrawlJob    `gorm:"foreignKey:CrawlJobID;constraint:OnDelete:CASCADE" json:"-"`
	Page       CrawledPage `gorm:"foreignKey:PageID;constraint:OnDelete:CASCADE" json:"-"`
}

// SemanticSearchResult represents a result from vector similarity search
type SemanticSearchResult struct {
	PageID       uint    `json:"page_id"`
	URL          string  `json:"url"`
	Title        string  `json:"title"`
	Score        float64 `json:"score"`         // cosine similarity 0-1
	ScorePercent float64 `json:"score_percent"` // Score * 100 for display
	CrawlJobID   string  `json:"crawl_job_id"`
	Domain       string  `json:"domain"`
	Snippet      string  `json:"snippet,omitempty"`
}

// SearchResult represents a grouped search result for the search page
// MatchedPhrase describes one phrase that matched on a page.
type MatchedPhrase struct {
	Phrase      string `json:"phrase"`
	MatchType   string `json:"match_type"`
	Context     string `json:"context"`
	Occurrences int    `json:"occurrences"`
}

type SearchResult struct {
	URL            string          `json:"url"`
	Domain         string          `json:"domain"`
	CrawlJobID     string          `json:"crawl_job_id"`
	FoundAt        string          `json:"found_at"`
	Score          float64         `json:"score"`           // TF-IDF relevance score
	ScorePercent   float64         `json:"score_percent"`   // Score normalised to 0-100 for display
	MatchedPhrases []MatchedPhrase `json:"matched_phrases"` // All phrases that matched on this page
	Phrase         string          `json:"phrase"`          // Primary (best) matched phrase – for backwards compat
	MatchType      string          `json:"match_type"`      // Primary match type
	Context        string          `json:"context"`         // Primary context
	Occurrences    int             `json:"occurrences"`     // Total occurrences across all phrases
}
