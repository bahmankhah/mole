package models

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
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
	JobStatusQueued    JobStatus = "queued"
	JobStatusRunning   JobStatus = "running"
	JobStatusPaused    JobStatus = "paused"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

// SettingsEditable reports whether crawler settings may be changed for this status.
func (s JobStatus) SettingsEditable() bool {
	return s == JobStatusPending || s == JobStatusPaused
}

// IsTerminal reports whether the job has finished for good.
func (s JobStatus) IsTerminal() bool {
	return s == JobStatusCompleted || s == JobStatusFailed || s == JobStatusCancelled
}

// IsActive reports whether the job is queued or running.
func (s JobStatus) IsActive() bool {
	return s == JobStatusQueued || s == JobStatusRunning
}

// MatchType represents where a phrase was found
type MatchType string

const (
	MatchTypeContent MatchType = "content" // Found in page body content
	MatchTypeURL     MatchType = "url"     // Found in the page URL
	MatchTypeAnchor  MatchType = "anchor"  // Found in anchor text pointing to the page
)

// HTTPHeader is a single extra request header applied to crawl fetches.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

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
	AfterCrawlPlugin      *string  `json:"after_crawl_plugin,omitempty"`     // Plugin id from plugins/; empty disables after-crawl
	AfterJobPlugin        *string  `json:"after_job_plugin,omitempty"`       // Plugin id from plugins/; empty disables after-job
	EnableWordExtraction  *bool    `json:"enable_word_extraction,omitempty"` // Extract words and build inverted index
	EnableStemming        *bool    `json:"enable_stemming,omitempty"`        // Stem/lemmatise words during indexing and search
	EnableLemmatization   *bool    `json:"enable_lemmatization,omitempty"`   // Use lemmatization vs pure stemming
	DefaultLanguage       *string  `json:"default_language,omitempty"`       // Language for stemming: "fa" or "en"
	UseCrawlPhrasesOnly   *bool    `json:"use_crawl_phrases_only,omitempty"` // true = only match crawl-extracted words; false = also match manual phrases
	// HTTP request overrides. Method/body apply to seed URLs; extra headers apply to every fetch.
	RequestMethod   *string           `json:"request_method,omitempty"`    // "GET" (default) or "POST"
	RequestHeaders  []HTTPHeader      `json:"request_headers,omitempty"`   // extra headers (repeater)
	RequestBody     *string           `json:"request_body,omitempty"`      // POST payload; may contain {{VAR}} placeholders
	RequestBodyVars map[string]string `json:"request_body_vars,omitempty"` // var_name → value expression (e.g. "1,hamster,5-10")

	// Network
	ProxyPool      *string  `json:"proxy_pool,omitempty"`      // "" / "auto" = routing rules, "direct", or a pool name
	Proxies        []string `json:"proxies,omitempty"`         // job-specific proxy URLs; win over pools
	StickyProxy    *bool    `json:"sticky_proxy,omitempty"`    // keep one exit IP per host for the whole job
	TLSFingerprint *string  `json:"tls_fingerprint,omitempty"` // chrome, firefox, safari, edge, ios, go
	HostRateLimit  *float64 `json:"host_rate_limit,omitempty"` // max requests/sec per host for this job

	// Browser
	HeadlessFallback   *bool           `json:"headless_fallback,omitempty"`    // retry blocked/challenge/SPA-shell pages with the browser
	HeadlessRenderWait *int            `json:"headless_render_wait,omitempty"` // extra seconds after network idle
	BrowserActions     []BrowserAction `json:"browser_actions,omitempty"`      // run on every page rendered by the browser
	Screenshot         *bool           `json:"screenshot,omitempty"`
	FullPageScreenshot *bool           `json:"full_page_screenshot,omitempty"`
	BlockResources     []string        `json:"block_resources,omitempty"` // image, font, media, stylesheet

	// Content
	Formats          []string       `json:"formats,omitempty"`           // markdown, html, text, links, metadata; detectors: product, article, business, job_posting, event, faq
	OnlyMainContent  *bool          `json:"only_main_content,omitempty"` // strip nav/footer/ads before converting
	ExtractFields    []ExtractField `json:"extract_fields,omitempty"`    // structured extraction schema (CSS, XPath or JSON-LD)
	ExtractURLMatch  *string        `json:"extract_url_match,omitempty"` // regex; extraction runs only on matching URLs
	IncludeDocuments *bool          `json:"include_documents,omitempty"` // follow links to PDF/DOCX files (parsed into Markdown)
	LLMPrompt        *string        `json:"llm_prompt,omitempty"`        // LLM extraction instructions
	LLMSchema        *string        `json:"llm_schema,omitempty"`        // JSON schema the LLM must return
	Phrases          []string       `json:"phrases,omitempty"`           // job-specific phrases (replace the global list)

	// Monitoring
	ChangeDetection  *bool   `json:"change_detection,omitempty"`    // diff each page against the previous run
	CompareWithJobID *string `json:"compare_with_job_id,omitempty"` // baseline job (default: previous run of the schedule)
}

// BrowserAction is a step performed in the headless browser after the page renders.
type BrowserAction struct {
	Type     string `json:"type"`               // wait, wait_for_selector, click, scroll, type, press, evaluate
	Selector string `json:"selector,omitempty"` // CSS selector
	Value    string `json:"value,omitempty"`    // text to type, key to press, or scroll target ("bottom", "top", pixels)
	Ms       int    `json:"ms,omitempty"`       // wait duration or per-action timeout
	Count    int    `json:"count,omitempty"`    // repeat count for scroll
	Script   string `json:"script,omitempty"`   // JS for evaluate
}

// ExtractField describes one field of a structured extraction schema.
type ExtractField struct {
	Name     string         `json:"name"`
	Selector string         `json:"selector"`           // CSS selector, or "xpath:<expr>"
	Type     string         `json:"type,omitempty"`     // text (default), html, attr, number, price, date, url, exists, list
	Attr     string         `json:"attr,omitempty"`     // attribute for type attr/url
	Multiple bool           `json:"multiple,omitempty"` // return all matches as an array
	Regex    string         `json:"regex,omitempty"`    // optional: first capture group (or whole match) of the text
	Fields   []ExtractField `json:"fields,omitempty"`   // for type list: fields relative to each matched element
	// FallbackSelectors are tried in order while the field is still empty,
	// e.g. a CSS selector after "jsonld:Product.offers.price".
	FallbackSelectors []string `json:"fallback_selectors,omitempty"`
}

// JobLimits caps what a job may consume. Zero means unlimited.
type JobLimits struct {
	MaxPages          int   `json:"max_pages,omitempty"`
	MaxRequests       int   `json:"max_requests,omitempty"`
	MaxBytes          int64 `json:"max_bytes,omitempty"`
	MaxBrowserSeconds int   `json:"max_browser_seconds,omitempty"`
	MaxLLMTokens      int   `json:"max_llm_tokens,omitempty"`
	MaxDurationSec    int   `json:"max_duration_seconds,omitempty"`
}

// IsZero reports whether no limit is set.
func (l JobLimits) IsZero() bool { return l == JobLimits{} }

// Value implements driver.Valuer for GORM JSON storage.
func (l JobLimits) Value() (driver.Value, error) { return jsonValue(l) }

// Scan implements sql.Scanner for GORM JSON storage.
func (l *JobLimits) Scan(value interface{}) error { return jsonScan(value, l) }

// WebhookConfig tells the engine where to send job events.
type WebhookConfig struct {
	URL    string   `json:"url"`
	Secret string   `json:"secret,omitempty"` // HMAC-SHA256 signing secret
	Events []string `json:"events,omitempty"` // empty = all events except page.crawled
}

// Value implements driver.Valuer for GORM JSON storage.
func (w WebhookConfig) Value() (driver.Value, error) { return jsonValue(w) }

// Scan implements sql.Scanner for GORM JSON storage.
func (w *WebhookConfig) Scan(value interface{}) error { return jsonScan(value, w) }

// JobUsage is what a job consumed; control planes bill from it.
type JobUsage struct {
	Requests      int64 `gorm:"column:usage_requests;default:0" json:"requests"`
	Pages         int64 `gorm:"column:usage_pages;default:0" json:"pages"`
	Bytes         int64 `gorm:"column:usage_bytes;default:0" json:"bytes"`
	BrowserMs     int64 `gorm:"column:usage_browser_ms;default:0" json:"browser_ms"`
	LLMTokens     int64 `gorm:"column:usage_llm_tokens;default:0" json:"llm_tokens"`
	Errors        int64 `gorm:"column:usage_errors;default:0" json:"errors"`
	Screenshots   int64 `gorm:"column:usage_screenshots;default:0" json:"screenshots"`
	ProxyRequests int64 `gorm:"column:usage_proxy_requests;default:0" json:"proxy_requests"`
	DocumentPages int64 `gorm:"column:usage_document_pages;default:0" json:"document_pages"` // PDF/DOCX pages converted
}

func jsonValue(v interface{}) (driver.Value, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func jsonScan(value interface{}, dst interface{}) error {
	if value == nil {
		return nil
	}
	var b []byte
	switch v := value.(type) {
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		return fmt.Errorf("cannot scan %T into %T", value, dst)
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, dst)
}

// IsEmpty reports whether no per-job overrides were set.
func (s JobSettings) IsEmpty() bool {
	return s.MaxConcurrentRequests == nil && s.RequestTimeoutSec == nil &&
		s.PolitenessDelayMs == nil && s.MaxDepth == nil &&
		s.MaxPages == nil &&
		s.UserAgent == nil && s.MaxRetries == nil &&
		s.RespectRobotsTxt == nil &&
		s.SkipContentDuplicates == nil &&
		s.UseHeadlessBrowser == nil && s.HeadlessWaitSelector == nil &&
		s.EnableSemanticSearch == nil && s.AfterCrawlPlugin == nil && s.AfterJobPlugin == nil &&
		s.SaveTextContent == nil && s.EnableWordExtraction == nil &&
		s.EnableStemming == nil && s.EnableLemmatization == nil &&
		s.DefaultLanguage == nil && s.UseCrawlPhrasesOnly == nil &&
		s.RequestMethod == nil && s.RequestBody == nil &&
		len(s.RequestHeaders) == 0 && len(s.RequestBodyVars) == 0 &&
		len(s.SkipExtensions) == 0 && len(s.URLIncludePatterns) == 0 &&
		len(s.URLExcludePatterns) == 0 && len(s.ExtraTrackingParams) == 0 &&
		s.ProxyPool == nil && len(s.Proxies) == 0 && s.StickyProxy == nil &&
		s.TLSFingerprint == nil && s.HostRateLimit == nil &&
		s.HeadlessFallback == nil && s.HeadlessRenderWait == nil &&
		len(s.BrowserActions) == 0 && s.Screenshot == nil && s.FullPageScreenshot == nil &&
		len(s.BlockResources) == 0 && len(s.Formats) == 0 && s.OnlyMainContent == nil &&
		len(s.ExtractFields) == 0 && s.ExtractURLMatch == nil && s.IncludeDocuments == nil &&
		s.LLMPrompt == nil && s.LLMSchema == nil && len(s.Phrases) == 0 &&
		s.ChangeDetection == nil && s.CompareWithJobID == nil
}

// SanitizeRequest normalizes HTTP request fields (method, headers, body vars).
func (s *JobSettings) SanitizeRequest() {
	if s == nil {
		return
	}
	if s.RequestMethod != nil {
		m := strings.ToUpper(strings.TrimSpace(*s.RequestMethod))
		if m == "POST" {
			s.RequestMethod = &m
		} else if m == "" || m == "GET" {
			s.RequestMethod = nil
			s.RequestBody = nil
			s.RequestBodyVars = nil
		} else {
			s.RequestMethod = nil
			s.RequestBody = nil
			s.RequestBodyVars = nil
		}
	}
	if len(s.RequestHeaders) > 0 {
		cleaned := make([]HTTPHeader, 0, len(s.RequestHeaders))
		for _, h := range s.RequestHeaders {
			name := strings.TrimSpace(h.Name)
			if name == "" {
				continue
			}
			cleaned = append(cleaned, HTTPHeader{Name: name, Value: h.Value})
		}
		if len(cleaned) == 0 {
			s.RequestHeaders = nil
		} else {
			s.RequestHeaders = cleaned
		}
	}
	if s.RequestBody != nil && strings.TrimSpace(*s.RequestBody) == "" {
		s.RequestBody = nil
	}
	if len(s.RequestBodyVars) > 0 {
		cleaned := make(map[string]string, len(s.RequestBodyVars))
		for k, v := range s.RequestBodyVars {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k == "" || v == "" {
				continue
			}
			cleaned[k] = v
		}
		if len(cleaned) == 0 {
			s.RequestBodyVars = nil
		} else {
			s.RequestBodyVars = cleaned
		}
	}
	s.AfterCrawlPlugin = sanitizePluginID(s.AfterCrawlPlugin)
	s.AfterJobPlugin = sanitizePluginID(s.AfterJobPlugin)
}

const legacyEnabledPluginID = "divar"

func sanitizePluginID(id *string) *string {
	if id == nil {
		return nil
	}
	v := strings.TrimSpace(*id)
	if v == "" {
		empty := ""
		return &empty
	}
	if !isSafePluginID(v) {
		empty := ""
		return &empty
	}
	return &v
}

func isSafePluginID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		if i > 0 && (r == '_' || r == '-') {
			continue
		}
		return false
	}
	return true
}

// UnmarshalJSON accepts current plugin-id fields and the older after_*_script booleans.
func (s *JobSettings) UnmarshalJSON(data []byte) error {
	type Alias JobSettings
	aux := &struct {
		*Alias
		AfterCrawlScript *bool `json:"after_crawl_script"`
		AfterJobScript   *bool `json:"after_job_script"`
	}{Alias: (*Alias)(s)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if s.AfterCrawlPlugin == nil && aux.AfterCrawlScript != nil {
		id := ""
		if *aux.AfterCrawlScript {
			id = legacyEnabledPluginID
		}
		s.AfterCrawlPlugin = &id
	}
	if s.AfterJobPlugin == nil && aux.AfterJobScript != nil {
		id := ""
		if *aux.AfterJobScript {
			id = legacyEnabledPluginID
		}
		s.AfterJobPlugin = &id
	}
	return nil
}

// CheckSettingsJSON reports unknown fields in a JSON settings object, so API
// clients learn about typos instead of having options silently ignored.
func CheckSettingsJSON(raw []byte) error {
	type Alias JobSettings
	aux := struct {
		*Alias
		AfterCrawlScript *bool `json:"after_crawl_script"`
		AfterJobScript   *bool `json:"after_job_script"`
	}{Alias: &Alias{}}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&aux); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	return nil
}

// StringSlice is a JSON-serialised []string for GORM columns.
type StringSlice []string

func (ss StringSlice) Value() (driver.Value, error) {
	if ss == nil {
		return nil, nil
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (ss *StringSlice) Scan(value interface{}) error {
	if value == nil {
		*ss = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case string:
		bytes = []byte(v)
	case []byte:
		bytes = v
	default:
		return fmt.Errorf("cannot scan %T into StringSlice", value)
	}
	return json.Unmarshal(bytes, ss)
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

// PortScanJob is a TCP scan of well-known service ports for one host.
type PortScanJob struct {
	ID           string     `gorm:"type:varchar(36);primaryKey" json:"id"`
	Target       string     `gorm:"type:varchar(255);index;not null" json:"target"`
	ResolvedIP   string     `gorm:"type:varchar(45)" json:"resolved_ip"`
	Status       JobStatus  `gorm:"type:varchar(20);index;default:'pending'" json:"status"`
	PortsTotal   int        `gorm:"default:0" json:"ports_total"`
	PortsScanned int        `gorm:"default:0" json:"ports_scanned"`
	PortsOpen    int        `gorm:"default:0" json:"ports_open"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	StartedAt    *time.Time `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	ErrorMessage string     `gorm:"type:text" json:"error_message,omitempty"`
}

// BeforeCreate generates UUID for new port scan jobs
func (j *PortScanJob) BeforeCreate(tx *gorm.DB) error {
	if j.ID == "" {
		j.ID = uuid.New().String()
	}
	return nil
}

// DiscoveredPort is an open TCP port found by a port scan.
type DiscoveredPort struct {
	ID            uint        `gorm:"primaryKey" json:"id"`
	PortScanJobID string      `gorm:"type:varchar(36);uniqueIndex:idx_scan_port;not null" json:"port_scan_job_id"`
	Port          int         `gorm:"uniqueIndex:idx_scan_port;not null" json:"port"`
	Protocol      string      `gorm:"type:varchar(8);default:'tcp'" json:"protocol"`
	Service       string      `gorm:"type:varchar(64);not null" json:"service"`
	Detail        string      `gorm:"type:varchar(255)" json:"detail,omitempty"`
	Group         string      `gorm:"type:varchar(64)" json:"group,omitempty"`
	Banner        string      `gorm:"type:text" json:"banner,omitempty"`
	Product       string      `gorm:"type:varchar(128)" json:"product,omitempty"`
	Warning       string      `gorm:"type:text" json:"warning,omitempty"`
	Importance    int         `gorm:"index" json:"importance"`
	LatencyMS     int64       `json:"latency_ms"`
	CreatedAt     time.Time   `json:"created_at"`
	PortScanJob   PortScanJob `gorm:"foreignKey:PortScanJobID;constraint:OnDelete:CASCADE" json:"-"`
}

// Job kinds.
const (
	JobKindCrawl = "crawl" // follow links from the start URL(s)
	JobKindBatch = "batch" // fetch exactly the listed URLs (batch scrape)
)

// CrawlJob represents a crawling job for a specific target (domain or subdomain)
type CrawlJob struct {
	ID             string       `gorm:"type:varchar(36);primaryKey" json:"id"`
	Kind           string       `gorm:"type:varchar(16);default:'crawl'" json:"kind"`
	DiscoveryJobID string       `gorm:"type:varchar(36);index" json:"discovery_job_id,omitempty"`
	TargetURL      string       `gorm:"type:varchar(512);not null" json:"target_url"`
	Domain         string       `gorm:"type:varchar(255);index;not null" json:"domain"`
	SeedURLs       StringSlice  `gorm:"type:json" json:"seed_urls,omitempty"`
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

	// Ownership and scheduling. OwnerRef is an opaque id set by the control
	// plane (e.g. a Laravel team id); the engine never interprets it.
	OwnerRef   string  `gorm:"type:varchar(191);index;default:''" json:"owner_ref,omitempty"`
	ExternalID string  `gorm:"type:varchar(191);index;default:''" json:"external_id,omitempty"`
	ScheduleID *string `gorm:"type:varchar(36);index" json:"schedule_id,omitempty"`
	Priority   int     `gorm:"default:0;index" json:"priority"`
	Region     string  `gorm:"type:varchar(32);default:''" json:"region,omitempty"`

	Limits  *JobLimits     `gorm:"type:json" json:"limits,omitempty"`
	Webhook *WebhookConfig `gorm:"type:json" json:"webhook,omitempty"`
	Usage   JobUsage       `gorm:"embedded" json:"usage"`

	// Queue / lease state.
	QueuedAt       *time.Time `gorm:"index" json:"queued_at,omitempty"`
	WorkerID       string     `gorm:"type:varchar(128);default:''" json:"worker_id,omitempty"`
	LeaseExpiresAt *time.Time `gorm:"index" json:"lease_expires_at,omitempty"`
	Control        string     `gorm:"type:varchar(16);default:''" json:"control,omitempty"` // pending control request: pause, cancel
	StopReason     string     `gorm:"type:varchar(64);default:''" json:"stop_reason,omitempty"`

	// Durable after-job plugin run.
	AfterJobPlugin string     `gorm:"type:varchar(64);default:''" json:"after_job_plugin,omitempty"`
	AfterJobDueAt  *time.Time `gorm:"index" json:"after_job_due_at,omitempty"`
	AfterJobState  string     `gorm:"type:varchar(16);default:''" json:"after_job_state,omitempty"` // scheduled, running, done, failed
	// AfterJobWorker is the worker that ran the crawl. Plugins keep per-page
	// output on that machine, so it gets the after-job run first.
	AfterJobWorker     string     `gorm:"type:varchar(128);default:''" json:"after_job_worker,omitempty"`
	AfterJobOutput     string     `gorm:"type:text" json:"after_job_output,omitempty"` // tail of the last run's output
	AfterJobFinishedAt *time.Time `json:"after_job_finished_at,omitempty"`

	// After-crawl plugin statistics.
	AfterCrawlRuns     int64  `gorm:"default:0" json:"after_crawl_runs"`
	AfterCrawlFailures int64  `gorm:"default:0" json:"after_crawl_failures"`
	AfterCrawlError    string `gorm:"type:text" json:"after_crawl_error,omitempty"` // last failure
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
	BodyKey       string    `gorm:"type:varchar(255);default:''" json:"body_key,omitempty"`    // object-store key when bodies are offloaded
	FetchedWith   string    `gorm:"type:varchar(16);default:''" json:"fetched_with,omitempty"` // http, browser
	FinalURL      string    `gorm:"type:varchar(2048)" json:"final_url,omitempty"`
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
	RequestMethod string    `gorm:"type:varchar(10);default:''" json:"request_method,omitempty"` // GET (empty) or POST; seed requests only
	RequestBody   string    `gorm:"type:mediumtext" json:"request_body,omitempty"`               // POST body for this frontier entry
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
	Phrase      string  `json:"phrase"`
	MatchType   string  `json:"match_type"`
	Context     string  `json:"context"`
	Occurrences int     `json:"occurrences"`
	TF          float64 `json:"tf"`
	IDF         float64 `json:"idf"`
	TFIDF       float64 `json:"tfidf"`
	MatchBoost  float64 `json:"match_boost"`
	ExactBoost  float64 `json:"exact_boost"`
	PhraseScore float64 `json:"phrase_score"` // final score contribution from this phrase
}

// ScoreBreakdown explains how the total score was computed.
type ScoreBreakdown struct {
	TotalDocs      int64   `json:"total_docs"`       // N in IDF formula
	MatchedTerms   int     `json:"matched_terms"`    // K distinct query n-grams matched
	MultiTermBonus float64 `json:"multi_term_bonus"` // multiplier from K>1
	RawScore       float64 `json:"raw_score"`        // score before multi-term bonus
	FinalScore     float64 `json:"final_score"`      // score after multi-term bonus
}

type SearchResult struct {
	URL            string          `json:"url"`
	Domain         string          `json:"domain"`
	CrawlJobID     string          `json:"crawl_job_id"`
	FoundAt        string          `json:"found_at"`
	Score          float64         `json:"score"`           // TF-IDF relevance score
	ScorePercent   float64         `json:"score_percent"`   // Score normalised to 0-100 for display
	ScoreDetail    ScoreBreakdown  `json:"score_detail"`    // Detailed score breakdown
	MatchedPhrases []MatchedPhrase `json:"matched_phrases"` // All phrases that matched on this page
	Phrase         string          `json:"phrase"`          // Primary (best) matched phrase – for backwards compat
	MatchType      string          `json:"match_type"`      // Primary match type
	Context        string          `json:"context"`         // Primary context
	Occurrences    int             `json:"occurrences"`     // Total occurrences across all phrases
}
