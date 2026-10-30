package models

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Output formats a job can produce for each page.
const (
	FormatMarkdown = "markdown"
	FormatHTML     = "html"
	FormatText     = "text"
	FormatLinks    = "links"
	FormatMetadata = "metadata"

	// Detectors: structured data found without selectors (see content.Detect).
	FormatProduct    = "product"
	FormatArticle    = "article"
	FormatBusiness   = "business"
	FormatJobPosting = "job_posting"
	FormatEvent      = "event"
	FormatFAQ        = "faq"
)

// AllFormats lists every supported page output format.
var AllFormats = []string{FormatMarkdown, FormatHTML, FormatText, FormatLinks, FormatMetadata,
	FormatProduct, FormatArticle, FormatBusiness, FormatJobPosting, FormatEvent, FormatFAQ}

// DetectedData holds the detectors' results for a page as JSON, one column
// per detector format (the column is named like the format).
type DetectedData struct {
	Product    JSONRaw `gorm:"column:product;type:json" json:"product,omitempty"`
	Article    JSONRaw `gorm:"column:article;type:json" json:"article,omitempty"`
	Business   JSONRaw `gorm:"column:business;type:json" json:"business,omitempty"`
	JobPosting JSONRaw `gorm:"column:job_posting;type:json" json:"job_posting,omitempty"`
	Event      JSONRaw `gorm:"column:event;type:json" json:"event,omitempty"`
	FAQ        JSONRaw `gorm:"column:faq;type:json" json:"faq,omitempty"`
}

// DetectedField is one detector's result.
type DetectedField struct {
	Format string
	Value  JSONRaw
}

// DetectedColumns are the database columns of DetectedData.
var DetectedColumns = []string{FormatProduct, FormatArticle, FormatBusiness, FormatJobPosting, FormatEvent, FormatFAQ}

// Fields lists every detector's result in DetectedColumns order (empty when
// nothing was detected).
func (d DetectedData) Fields() []DetectedField {
	return []DetectedField{
		{FormatProduct, d.Product}, {FormatArticle, d.Article}, {FormatBusiness, d.Business},
		{FormatJobPosting, d.JobPosting}, {FormatEvent, d.Event}, {FormatFAQ, d.FAQ},
	}
}

// Set stores the result of the detector format.
func (d *DetectedData) Set(format string, v JSONRaw) {
	switch format {
	case FormatProduct:
		d.Product = v
	case FormatArticle:
		d.Article = v
	case FormatBusiness:
		d.Business = v
	case FormatJobPosting:
		d.JobPosting = v
	case FormatEvent:
		d.Event = v
	case FormatFAQ:
		d.FAQ = v
	}
}

// Stop reasons recorded when a job ends because of a limit.
const (
	StopReasonMaxPages    = "limit:max_pages"
	StopReasonMaxRequests = "limit:max_requests"
	StopReasonMaxBytes    = "limit:max_bytes"
	StopReasonMaxBrowser  = "limit:max_browser_seconds"
	StopReasonMaxLLM      = "limit:max_llm_tokens"
	StopReasonMaxDuration = "limit:max_duration"
	StopReasonFrontier    = "frontier_exhausted"
	StopReasonUser        = "user"
)

// Control requests a worker applies to a running job.
const (
	ControlPause  = "pause"
	ControlCancel = "cancel"
)

// JSONMap is a JSON object column.
type JSONMap map[string]interface{}

// Value implements driver.Valuer.
func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	return jsonValue(m)
}

// Scan implements sql.Scanner.
func (m *JSONMap) Scan(value interface{}) error { return jsonScan(value, m) }

// JSONRaw is an arbitrary JSON column (object, array or scalar).
type JSONRaw json.RawMessage

// Value implements driver.Valuer.
func (r JSONRaw) Value() (driver.Value, error) {
	if len(r) == 0 {
		return nil, nil
	}
	return string(r), nil
}

// Scan implements sql.Scanner.
func (r *JSONRaw) Scan(value interface{}) error {
	switch v := value.(type) {
	case nil:
		*r = nil
	case string:
		*r = JSONRaw(v)
	case []byte:
		*r = append(JSONRaw(nil), v...)
	}
	return nil
}

// MarshalJSON emits the raw JSON (or null).
func (r JSONRaw) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return []byte(r), nil
}

// UnmarshalJSON stores the raw JSON.
func (r *JSONRaw) UnmarshalJSON(b []byte) error {
	*r = append((*r)[0:0], b...)
	return nil
}

// PageContent holds derived content for a crawled page: Markdown, metadata,
// structured extraction results and screenshots. It is kept apart from
// crawled_pages so page listings stay light.
type PageContent struct {
	ID            uint              `gorm:"primaryKey" json:"id"`
	PageID        uint              `gorm:"uniqueIndex;not null" json:"page_id"`
	CrawlJobID    string            `gorm:"type:varchar(36);index;not null" json:"crawl_job_id"`
	URLHash       string            `gorm:"type:varchar(64);index" json:"url_hash"`
	Markdown      *string           `gorm:"type:longtext" json:"markdown,omitempty"`
	HTML          *string           `gorm:"type:longtext" json:"html,omitempty"`
	Text          *string           `gorm:"type:longtext" json:"text,omitempty"`
	Links         StringSlice       `gorm:"type:json" json:"links,omitempty"`
	Metadata      JSONMap           `gorm:"type:json" json:"metadata,omitempty"`
	Extracted     JSONRaw           `gorm:"type:json" json:"extracted,omitempty"`
	DetectedData  `gorm:"embedded"` // product, article, business, job_posting, event, faq
	LLMExtracted  JSONRaw           `gorm:"type:json" json:"llm_extracted,omitempty"`
	ExtractError  string            `gorm:"type:text" json:"extract_error,omitempty"`
	ActionResults JSONRaw           `gorm:"type:json" json:"action_results,omitempty"`
	ScreenshotKey string            `gorm:"type:varchar(255);default:''" json:"screenshot_key,omitempty"`
	ContentHash   string            `gorm:"type:varchar(64)" json:"content_hash,omitempty"` // hash of the main text, for change detection
	CreatedAt     time.Time         `json:"created_at"`
	Page          CrawledPage       `gorm:"foreignKey:PageID;constraint:OnDelete:CASCADE" json:"-"`
	CrawlJob      CrawlJob          `gorm:"foreignKey:CrawlJobID;constraint:OnDelete:CASCADE" json:"-"`
}

// Change types recorded by change detection.
const (
	ChangeNew     = "new"
	ChangeChanged = "changed"
	ChangeRemoved = "removed"
)

// PageChange records how a page differs from the same URL in a baseline job.
type PageChange struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	CrawlJobID string    `gorm:"type:varchar(36);index;not null" json:"crawl_job_id"`
	BaseJobID  string    `gorm:"type:varchar(36);index" json:"base_job_id"`
	PageID     *uint     `gorm:"index" json:"page_id,omitempty"`
	URL        string    `gorm:"type:varchar(2048);not null" json:"url"`
	URLHash    string    `gorm:"type:varchar(64);index" json:"url_hash"`
	ChangeType string    `gorm:"type:varchar(16);index" json:"change_type"`
	Added      int       `json:"added"`
	Removed    int       `json:"removed"`
	Diff       *string   `gorm:"type:mediumtext" json:"diff,omitempty"`
	Fields     JSONRaw   `gorm:"type:json" json:"fields,omitempty"` // []FieldChange: extracted and detected fields whose value changed
	DetectedAt time.Time `json:"detected_at"`
	CrawlJob   CrawlJob  `gorm:"foreignKey:CrawlJobID;constraint:OnDelete:CASCADE" json:"-"`
}

// FieldChange is an extracted field whose value differs from the baseline.
type FieldChange struct {
	Name string          `json:"name"`
	Old  json.RawMessage `json:"old"`
	New  json.RawMessage `json:"new"`
}

// FieldChanges decodes Fields (for templates).
func (c PageChange) FieldChanges() []FieldChange {
	var out []FieldChange
	if len(c.Fields) > 0 {
		_ = json.Unmarshal(c.Fields, &out)
	}
	return out
}

// JobTemplate is the part of a crawl request a schedule replays on every run.
type JobTemplate struct {
	Kind         string            `json:"kind,omitempty"` // crawl (default) or batch
	TargetURL    string            `json:"target_url"`
	SeedURLs     []string          `json:"seed_urls,omitempty"`
	TemplateVars map[string]string `json:"template_vars,omitempty"`
	MaxDepth     int               `json:"max_depth,omitempty"`
	Priority     int               `json:"priority,omitempty"`
	Region       string            `json:"region,omitempty"`
	Settings     *JobSettings      `json:"settings,omitempty"`
	Limits       *JobLimits        `json:"limits,omitempty"`
	Webhook      *WebhookConfig    `json:"webhook,omitempty"`
}

// Value implements driver.Valuer.
func (t JobTemplate) Value() (driver.Value, error) { return jsonValue(t) }

// Scan implements sql.Scanner.
func (t *JobTemplate) Scan(value interface{}) error { return jsonScan(value, t) }

// Schedule runs a crawl on a cron schedule.
type Schedule struct {
	ID              string      `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name            string      `gorm:"type:varchar(255)" json:"name"`
	OwnerRef        string      `gorm:"type:varchar(191);index;default:''" json:"owner_ref,omitempty"`
	ExternalID      string      `gorm:"type:varchar(191);index;default:''" json:"external_id,omitempty"`
	Cron            string      `gorm:"type:varchar(128);not null" json:"cron"`
	Timezone        string      `gorm:"type:varchar(64);default:'UTC'" json:"timezone"`
	Enabled         bool        `gorm:"default:true;index" json:"enabled"`
	ChangeDetection bool        `gorm:"default:true" json:"change_detection"`
	SkipIfRunning   bool        `gorm:"default:true" json:"skip_if_running"`
	Template        JobTemplate `gorm:"type:json" json:"template"`
	NextRunAt       *time.Time  `gorm:"index" json:"next_run_at,omitempty"`
	LastRunAt       *time.Time  `json:"last_run_at,omitempty"`
	LastJobID       string      `gorm:"type:varchar(36);default:''" json:"last_job_id,omitempty"`
	RunCount        int         `gorm:"default:0" json:"run_count"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

// BeforeCreate generates a UUID for new schedules.
func (s *Schedule) BeforeCreate(tx *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.New().String()
	}
	return nil
}

// Webhook delivery states.
const (
	DeliveryPending   = "pending"
	DeliverySending   = "sending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// WebhookDelivery is one event in the webhook outbox.
type WebhookDelivery struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	EventID       string     `gorm:"type:varchar(36);uniqueIndex" json:"event_id"`
	CrawlJobID    string     `gorm:"type:varchar(36);index;default:''" json:"crawl_job_id,omitempty"`
	OwnerRef      string     `gorm:"type:varchar(191);index;default:''" json:"owner_ref,omitempty"`
	Event         string     `gorm:"type:varchar(64);index" json:"event"`
	URL           string     `gorm:"type:varchar(2048);not null" json:"url"`
	Secret        string     `gorm:"type:varchar(255)" json:"-"`
	Payload       string     `gorm:"type:mediumtext" json:"payload"`
	Status        string     `gorm:"type:varchar(16);index" json:"status"`
	Attempts      int        `gorm:"default:0" json:"attempts"`
	NextAttemptAt *time.Time `gorm:"index" json:"next_attempt_at,omitempty"`
	LastError     string     `gorm:"type:text" json:"last_error,omitempty"`
	ResponseCode  int        `json:"response_code,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	DeliveredAt   *time.Time `json:"delivered_at,omitempty"`
}

// IdempotencyKey remembers the resource created for an Idempotency-Key header.
type IdempotencyKey struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	OwnerRef     string    `gorm:"type:varchar(191);uniqueIndex:idx_idem_owner_key;default:''" json:"owner_ref"`
	Key          string    `gorm:"type:varchar(191);uniqueIndex:idx_idem_owner_key;not null" json:"key"`
	Scope        string    `gorm:"type:varchar(32)" json:"scope"`
	RequestHash  string    `gorm:"type:varchar(64)" json:"request_hash"`
	ResourceID   string    `gorm:"type:varchar(64)" json:"resource_id"`
	ResponseCode int       `json:"response_code"`
	CreatedAt    time.Time `gorm:"index" json:"created_at"`
}

// WorkerHeartbeat tracks live engine processes.
type WorkerHeartbeat struct {
	ID          string    `gorm:"type:varchar(128);primaryKey" json:"id"`
	Hostname    string    `gorm:"type:varchar(255)" json:"hostname"`
	Region      string    `gorm:"type:varchar(32)" json:"region"`
	Version     string    `gorm:"type:varchar(64)" json:"version"`
	MaxJobs     int       `json:"max_jobs"`
	RunningJobs int       `json:"running_jobs"`
	Browser     bool      `json:"browser"` // a headless browser is available on this worker
	StartedAt   time.Time `json:"started_at"`
	LastSeenAt  time.Time `gorm:"index" json:"last_seen_at"`

	// Filled when listing workers, not stored.
	State           string `gorm:"-" json:"state"`            // active, cordoned or draining
	VersionMismatch bool   `gorm:"-" json:"version_mismatch"` // runs a different version than this process
}

// Worker states set by an operator.
const (
	WorkerActive   = "active"   // claims queued jobs
	WorkerCordoned = "cordoned" // finishes its running jobs but claims no new ones
	WorkerDraining = "draining" // hands its running jobs back to the queue and claims no new ones
)

// WorkerControl holds the operator-set state of a worker id. A missing row
// means active. It is kept apart from WorkerHeartbeat, which every worker
// overwrites on each heartbeat.
type WorkerControl struct {
	WorkerID  string    `gorm:"type:varchar(128);primaryKey" json:"worker_id"`
	State     string    `gorm:"type:varchar(16);not null" json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

// JoinToken lets a new machine join the cluster as a worker ("mole join").
// Only the SHA-256 of the token is stored.
type JoinToken struct {
	ID         string     `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name       string     `gorm:"type:varchar(255)" json:"name"`
	TokenHash  string     `gorm:"type:varchar(64);uniqueIndex;not null" json:"-"`
	Prefix     string     `gorm:"type:varchar(16)" json:"prefix"` // first characters, to recognise a token
	Region     string     `gorm:"type:varchar(32);default:''" json:"region,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	UseCount   int        `gorm:"default:0" json:"use_count"`
	CreatedAt  time.Time  `json:"created_at"`
}

// OwnerLimit overrides worker.max_running_per_owner for one owner.
type OwnerLimit struct {
	OwnerRef   string    `gorm:"type:varchar(191);primaryKey" json:"owner_ref"`
	MaxRunning int       `gorm:"not null" json:"max_running"` // 0 = unlimited
	UpdatedAt  time.Time `json:"updated_at"`
}

// ScrapeCacheEntry is a stored /v1/scrape result, reused by requests with max_age.
type ScrapeCacheEntry struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CacheKey  string    `gorm:"type:varchar(64);uniqueIndex;not null" json:"cache_key"`
	OwnerRef  string    `gorm:"type:varchar(191);index;default:''" json:"owner_ref,omitempty"` // empty for shared entries
	URL       string    `gorm:"type:varchar(2048);not null" json:"url"`
	Result    []byte    `gorm:"type:longblob;not null" json:"-"` // gzipped JSON of the scrape result
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

// Usable reports whether the token is neither revoked nor expired at now.
func (t *JoinToken) Usable(now time.Time) bool {
	return t.RevokedAt == nil && (t.ExpiresAt == nil || now.Before(*t.ExpiresAt))
}
