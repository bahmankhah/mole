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
)

// AllFormats lists every supported page output format.
var AllFormats = []string{FormatMarkdown, FormatHTML, FormatText, FormatLinks, FormatMetadata}

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
	ID            uint        `gorm:"primaryKey" json:"id"`
	PageID        uint        `gorm:"uniqueIndex;not null" json:"page_id"`
	CrawlJobID    string      `gorm:"type:varchar(36);index;not null" json:"crawl_job_id"`
	URLHash       string      `gorm:"type:varchar(64);index" json:"url_hash"`
	Markdown      *string     `gorm:"type:longtext" json:"markdown,omitempty"`
	HTML          *string     `gorm:"type:longtext" json:"html,omitempty"`
	Text          *string     `gorm:"type:longtext" json:"text,omitempty"`
	Links         StringSlice `gorm:"type:json" json:"links,omitempty"`
	Metadata      JSONMap     `gorm:"type:json" json:"metadata,omitempty"`
	Extracted     JSONRaw     `gorm:"type:json" json:"extracted,omitempty"`
	LLMExtracted  JSONRaw     `gorm:"type:json" json:"llm_extracted,omitempty"`
	ExtractError  string      `gorm:"type:text" json:"extract_error,omitempty"`
	ActionResults JSONRaw     `gorm:"type:json" json:"action_results,omitempty"`
	ScreenshotKey string      `gorm:"type:varchar(255);default:''" json:"screenshot_key,omitempty"`
	ContentHash   string      `gorm:"type:varchar(64)" json:"content_hash,omitempty"` // hash of the main text, for change detection
	CreatedAt     time.Time   `json:"created_at"`
	Page          CrawledPage `gorm:"foreignKey:PageID;constraint:OnDelete:CASCADE" json:"-"`
	CrawlJob      CrawlJob    `gorm:"foreignKey:CrawlJobID;constraint:OnDelete:CASCADE" json:"-"`
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
	DetectedAt time.Time `json:"detected_at"`
	CrawlJob   CrawlJob  `gorm:"foreignKey:CrawlJobID;constraint:OnDelete:CASCADE" json:"-"`
}

// JobTemplate is the part of a crawl request a schedule replays on every run.
type JobTemplate struct {
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
	StartedAt   time.Time `json:"started_at"`
	LastSeenAt  time.Time `gorm:"index" json:"last_seen_at"`
}
