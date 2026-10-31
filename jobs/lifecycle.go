package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/crawler"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"github.com/resolver/crawler/storage"
	"gorm.io/gorm"
)

// Errors callers map to HTTP status codes.
var (
	ErrNotFound     = errors.New("not found")
	ErrInvalidState = errors.New("invalid state")
	ErrInvalidInput = errors.New("invalid input")
)

func invalidInput(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

func invalidState(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrInvalidState, fmt.Sprintf(format, args...))
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w", ErrNotFound)
	}
	return err
}

// JobSpec is everything needed to create a crawl job. It is the contract used
// by the /v1 API, the CLI and schedules.
type JobSpec struct {
	URL          string                `json:"url"`
	SeedURLs     []string              `json:"seed_urls,omitempty"`
	TemplateVars map[string]string     `json:"template_vars,omitempty"` // expand {{VAR}} placeholders in url
	MaxDepth     *int                  `json:"max_depth,omitempty"`
	OwnerRef     string                `json:"owner_ref,omitempty"`
	ExternalID   string                `json:"external_id,omitempty"`
	Priority     int                   `json:"priority,omitempty"`
	Region       string                `json:"region,omitempty"` // "", a region tag, or "auto" (from proxy routes)
	Settings     *models.JobSettings   `json:"settings,omitempty"`
	Limits       *models.JobLimits     `json:"limits,omitempty"`
	Webhook      *models.WebhookConfig `json:"webhook,omitempty"`
	Start        *bool                 `json:"start,omitempty"` // queue immediately (default true)

	Kind       string `json:"-"` // crawl (default) or batch; set by the batch endpoint and schedule templates
	ScheduleID string `json:"-"`
}

// MaxBatchURLs caps the URLs of one batch job.
const MaxBatchURLs = 50000

// normalizeKind validates a job kind; "" means crawl.
func normalizeKind(kind string) (string, error) {
	switch strings.TrimSpace(kind) {
	case "", models.JobKindCrawl:
		return models.JobKindCrawl, nil
	case models.JobKindBatch:
		return models.JobKindBatch, nil
	}
	return "", invalidInput("kind must be %q or %q", models.JobKindCrawl, models.JobKindBatch)
}

// batchURL turns one URL of a batch into an absolute http(s) URL. Unlike
// ParseTarget it keeps the path of scheme-less input ("shop.ir/p/1").
func batchURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", invalidInput("batch URLs must not be empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", invalidInput("invalid URL %q", raw)
	}
	return u.String(), nil
}

// SpecFromTemplate turns a schedule template into a spec.
func SpecFromTemplate(t models.JobTemplate) JobSpec {
	depth := t.MaxDepth
	spec := JobSpec{
		Kind:         t.Kind,
		URL:          t.TargetURL,
		SeedURLs:     t.SeedURLs,
		TemplateVars: t.TemplateVars,
		Priority:     t.Priority,
		Region:       t.Region,
		Settings:     t.Settings,
		Limits:       t.Limits,
		Webhook:      t.Webhook,
	}
	if depth > 0 {
		spec.MaxDepth = &depth
	}
	return spec
}

// SplitTargets splits a start-URL field into individual URLs. Whitespace
// (one URL per line is what the form asks for) is the only separator, so a
// URL carrying commas in its query string survives intact.
func SplitTargets(raw string) []string {
	var out []string
	for _, f := range strings.Fields(raw) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ParseTarget turns a URL or bare domain into the job's target URL and the
// base domain links are kept within.
func ParseTarget(target string) (fullURL, domain string, err error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", "", invalidInput("url is required")
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		parsed, perr := url.Parse(target)
		if perr != nil || parsed.Host == "" {
			return "", "", invalidInput("invalid URL %q", target)
		}
		// Store the base domain (e.g. "bayut.com" not "www.bayut.com") so link
		// filtering works across subdomains.
		domain = modules.NewURLCleaner().ExtractBaseDomain(parsed.Scheme + "://" + parsed.Host)
		if domain == "" {
			domain = parsed.Host
		}
		return target, domain, nil
	}
	if strings.Contains(target, "://") {
		return "", "", invalidInput("only http and https URLs can be crawled")
	}
	host := modules.CleanDomain(target)
	if host == "" {
		return "", "", invalidInput("invalid domain %q", target)
	}
	// Links are filtered by base domain, so store the base domain here too.
	domain = modules.NewURLCleaner().ExtractBaseDomain("https://" + host)
	if domain == "" {
		domain = host
	}
	return "https://" + host, domain, nil
}

// ExpandTemplateURL expands {{VAR}} placeholders into seed URLs.
func ExpandTemplateURL(target string, vars map[string]string) (models.StringSlice, error) {
	if !modules.HasTemplateVars(target) {
		return nil, nil
	}
	if len(vars) == 0 {
		return nil, invalidInput("template_vars are required when the URL contains {{VAR}}")
	}
	expanded := make(map[string][]string, len(vars))
	for name, expr := range vars {
		vals, err := modules.ExpandValueExpr(expr)
		if err != nil {
			return nil, invalidInput("error expanding variable {{%s}}: %v", name, err)
		}
		if len(vals) == 0 {
			return nil, invalidInput("variable {{%s}} has no values", name)
		}
		expanded[name] = vals
	}
	urls, err := modules.ExpandTemplateURL(target, expanded, 50000)
	if err != nil {
		return nil, invalidInput("%v", err)
	}
	return urls, nil
}

// ValidateSettings checks job settings and limits. When allowPlugins is false
// (requests from the public API), selecting a plugin is rejected because
// plugins execute arbitrary code on the worker.
func (m *Manager) ValidateSettings(s *models.JobSettings, limits *models.JobLimits, allowPlugins bool) error {
	if s != nil {
		s.SanitizeRequest()
		if !allowPlugins && ((s.AfterCrawlPlugin != nil && *s.AfterCrawlPlugin != "") || (s.AfterJobPlugin != nil && *s.AfterJobPlugin != "")) {
			return invalidInput("plugins are disabled for API requests (set plugins.allow_in_api)")
		}
	}
	if limits != nil {
		if limits.MaxPages < 0 || limits.MaxRequests < 0 || limits.MaxBytes < 0 || limits.MaxBrowserSeconds < 0 ||
			limits.MaxLLMTokens < 0 || limits.MaxDurationSec < 0 {
			return invalidInput("limits must not be negative")
		}
	}
	if _, err := crawler.BuildRunSettings(m.config, s, limits, m.engine.Proxies()); err != nil {
		return invalidInput("%v", err)
	}
	if s != nil && s.LLMPrompt != nil && *s.LLMPrompt != "" && m.engine.LLM() == nil {
		return invalidInput("LLM extraction is not configured on this engine (llm.base_url / llm.model)")
	}
	if err := m.validateGroupingSettings(s); err != nil {
		return err
	}
	return nil
}

// validateGroupingSettings checks the grouping half of a job's settings:
// the fields it compares must be known product fields or one of its own
// extract fields, and grouping must be enabled on the engine.
func (m *Manager) validateGroupingSettings(s *models.JobSettings) error {
	if s == nil {
		return nil
	}
	grouping := s.GroupProducts != nil && *s.GroupProducts
	if grouping && !m.config.Grouping.Enabled {
		return invalidInput("product grouping is disabled on this engine (grouping.enabled)")
	}
	if s.MatchThreshold != nil && (*s.MatchThreshold < 0 || *s.MatchThreshold > 1) {
		return invalidInput("match_threshold must be between 0 and 1")
	}
	if s.MatchMode != nil {
		switch *s.MatchMode {
		case "", models.MatchModeAuto, models.MatchModeExactOnly, models.MatchModeManual:
		default:
			return invalidInput("match_mode must be %q, %q or %q", models.MatchModeAuto, models.MatchModeExactOnly, models.MatchModeManual)
		}
	}
	own := map[string]bool{}
	for _, f := range s.ExtractFields {
		own[f.Name] = true
	}
	for _, f := range s.CompareFields {
		if content.FieldKindOf(f) == "" && !own[f] {
			return invalidInput("compare_fields: %q is not a detected product field, and no extract field is named that", f)
		}
	}
	return nil
}

func validateWebhook(w *models.WebhookConfig) error {
	if w == nil || strings.TrimSpace(w.URL) == "" {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(w.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalidInput("webhook.url must be an http(s) URL")
	}
	for _, ev := range w.Events {
		if !validEvent(ev) {
			return invalidInput("unknown webhook event %q", ev)
		}
	}
	return nil
}

// CreateFromSpec validates a spec and creates the job (queued unless Start is false).
func (m *Manager) CreateFromSpec(spec JobSpec, allowPlugins bool) (*models.CrawlJob, error) {
	kind, err := normalizeKind(spec.Kind)
	if err != nil {
		return nil, err
	}
	if kind == models.JobKindBatch {
		if spec.URL, err = batchURL(spec.URL); err != nil {
			return nil, err
		}
	}
	fullURL, domain, err := ParseTarget(spec.URL)
	if err != nil {
		return nil, err
	}
	seeds, err := ExpandTemplateURL(fullURL, spec.TemplateVars)
	if err != nil {
		return nil, err
	}
	if kind == models.JobKindBatch && len(seeds) == 0 {
		// A batch fetches its URL and every seed URL.
		seeds = models.StringSlice{fullURL}
	}
	for _, s := range spec.SeedURLs {
		if s = strings.TrimSpace(s); s != "" {
			if kind == models.JobKindBatch {
				if s, err = batchURL(s); err != nil {
					return nil, err
				}
			} else if _, _, err := ParseTarget(s); err != nil {
				return nil, err
			}
			seeds = append(seeds, s)
		}
	}
	if kind == models.JobKindBatch {
		seeds = uniqueStrings(seeds)
		if len(seeds) > MaxBatchURLs {
			return nil, invalidInput("a batch may contain at most %d URLs", MaxBatchURLs)
		}
	}
	if err := m.ValidateSettings(spec.Settings, spec.Limits, allowPlugins); err != nil {
		return nil, err
	}
	if err := validateWebhook(spec.Webhook); err != nil {
		return nil, err
	}
	if len(spec.OwnerRef) > 191 || len(spec.ExternalID) > 191 {
		return nil, invalidInput("owner_ref and external_id must be at most 191 characters")
	}
	depth := m.config.Crawler.MaxDepth
	if spec.MaxDepth == nil && spec.Settings != nil && spec.Settings.MaxDepth != nil {
		spec.MaxDepth = spec.Settings.MaxDepth // the setting wins at run time; record it on the job too
	}
	if spec.MaxDepth != nil {
		if *spec.MaxDepth < 0 {
			return nil, invalidInput("max_depth must be >= 0")
		}
		depth = *spec.MaxDepth
	}
	if kind == models.JobKindBatch {
		depth = 0
	}
	region := strings.TrimSpace(spec.Region)
	if region == "auto" {
		region = m.engine.Proxies().RegionFor(hostname(fullURL))
	}

	job := &models.CrawlJob{
		Kind:       kind,
		TargetURL:  fullURL,
		Domain:     domain,
		SeedURLs:   seeds,
		Status:     models.JobStatusPending,
		MaxDepth:   depth,
		Settings:   nilIfEmpty(spec.Settings),
		OwnerRef:   spec.OwnerRef,
		ExternalID: spec.ExternalID,
		Priority:   spec.Priority,
		Region:     region,
	}
	if spec.Limits != nil && !spec.Limits.IsZero() {
		job.Limits = spec.Limits
	}
	if spec.Webhook != nil && strings.TrimSpace(spec.Webhook.URL) != "" {
		job.Webhook = spec.Webhook
	}
	if spec.ScheduleID != "" {
		id := spec.ScheduleID
		job.ScheduleID = &id
	}
	if spec.Start == nil || *spec.Start {
		now := time.Now()
		job.Status = models.JobStatusQueued
		job.QueuedAt = &now
	}
	if err := m.createJob(job); err != nil {
		return nil, err
	}
	log.Printf("[JobManager] Created %s job %s for %s (status=%s, owner=%q)", job.Kind, job.ID, fullURL, job.Status, job.OwnerRef)
	if job.Status == models.JobStatusQueued {
		m.emit(job, EventJobQueued, nil)
		m.nudge()
	}
	return job, nil
}

// createJob inserts a job. GORM replaces zero values that have a column
// default (max_depth: 10) with the default, so a max_depth of 0 is written
// explicitly afterwards.
func (m *Manager) createJob(job *models.CrawlJob) error {
	depth := job.MaxDepth
	if err := m.db.Create(job).Error; err != nil {
		return err
	}
	if depth == 0 && job.MaxDepth != 0 {
		if err := m.db.Model(job).Update("max_depth", 0).Error; err != nil {
			return err
		}
		job.MaxDepth = 0
	}
	return nil
}

func uniqueStrings(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func nilIfEmpty(s *models.JobSettings) *models.JobSettings {
	if s == nil || s.IsEmpty() {
		return nil
	}
	return s
}

func hostname(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// CreateJob creates a pending crawl job for a URL or domain.
func (m *Manager) CreateJob(targetURL string, maxDepth int) (*models.CrawlJob, error) {
	return m.CreateJobWithSettings(targetURL, maxDepth, nil)
}

// CreateJobWithSettings creates a pending job from the web UI.
func (m *Manager) CreateJobWithSettings(targetURL string, maxDepth int, settings *models.JobSettings) (*models.CrawlJob, error) {
	start := false
	return m.CreateFromSpec(JobSpec{URL: targetURL, MaxDepth: &maxDepth, Settings: settings, Start: &start}, true)
}

// UpdateJobSeedURLs saves the expanded seed URLs for a job.
func (m *Manager) UpdateJobSeedURLs(jobID string, seeds models.StringSlice) error {
	return m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Update("seed_urls", seeds).Error
}

// StartJob queues a pending or paused job; a worker picks it up when it has capacity.
func (m *Manager) StartJob(jobID string) error {
	job, err := m.GetJob(jobID)
	if err != nil {
		return notFound(err)
	}
	switch job.Status {
	case models.JobStatusQueued, models.JobStatusRunning:
		return nil
	case models.JobStatusPending, models.JobStatusPaused:
	default:
		return invalidState("job is %s; duplicate it to crawl again", job.Status)
	}
	now := time.Now()
	res := m.db.Model(&models.CrawlJob{}).
		Where("id = ? AND status = ?", jobID, job.Status).
		Updates(map[string]interface{}{"status": models.JobStatusQueued, "queued_at": now, "control": "", "stop_reason": ""})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invalidState("job changed state; reload and try again")
	}
	job.Status = models.JobStatusQueued
	m.emit(job, EventJobQueued, nil)
	m.nudge()
	return nil
}

// ResumeJob resumes a paused job. With an empty id the most recently paused job is used.
func (m *Manager) ResumeJob(jobID string) error {
	if jobID == "" {
		var job models.CrawlJob
		if err := m.db.Where("status = ?", models.JobStatusPaused).Order("updated_at DESC").First(&job).Error; err != nil {
			return invalidState("no paused job to resume")
		}
		jobID = job.ID
	}
	job, err := m.GetJob(jobID)
	if err != nil {
		return notFound(err)
	}
	if job.Status != models.JobStatusPaused {
		return invalidState("job is not paused")
	}
	return m.StartJob(jobID)
}

// PauseJob pauses a running or queued job. A running job stops after its
// in-flight pages finish; its frontier is kept for resuming.
func (m *Manager) PauseJob(jobID string) error {
	return m.control(jobID, models.ControlPause)
}

// CancelJob stops a job for good.
func (m *Manager) CancelJob(jobID string) error {
	return m.control(jobID, models.ControlCancel)
}

// StopJobByID is CancelJob (kept for the web UI's "Stop" button).
func (m *Manager) StopJobByID(jobID string) error { return m.CancelJob(jobID) }

func (m *Manager) control(jobID, action string) error {
	job, err := m.GetJob(jobID)
	if err != nil {
		return notFound(err)
	}
	now := time.Now()
	switch job.Status {
	case models.JobStatusRunning:
		if err := m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Update("control", action).Error; err != nil {
			return err
		}
		// Apply at once when the run is local; other workers see the
		// request on their next lease renewal (within ~5s).
		m.engine.Signal(jobID, action)
		return nil
	case models.JobStatusQueued, models.JobStatusPending, models.JobStatusPaused:
		updates := map[string]interface{}{"control": ""}
		if action == models.ControlPause {
			if job.Status == models.JobStatusPending || job.Status == models.JobStatusPaused {
				return invalidState("job is not running")
			}
			updates["status"] = models.JobStatusPaused
		} else {
			updates["status"] = models.JobStatusCancelled
			updates["completed_at"] = now
			updates["stop_reason"] = models.StopReasonUser
			updates["error_message"] = "Job stopped manually"
		}
		res := m.db.Model(&models.CrawlJob{}).Where("id = ? AND status = ?", jobID, job.Status).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// It was claimed by a worker in the meantime; ask the worker instead.
			return m.control(jobID, action)
		}
		job.Status = updates["status"].(models.JobStatus)
		event := EventJobPaused
		if action == models.ControlCancel {
			event = EventJobCancelled
		}
		m.emit(job, event, nil)
		return nil
	default:
		return invalidState("job already %s", job.Status)
	}
}

// waitNotRunning waits until a job leaves the running state.
func (m *Manager) waitNotRunning(jobID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var job models.CrawlJob
		if err := m.db.Select("status").First(&job, "id = ?", jobID).Error; err != nil {
			return true
		}
		if job.Status != models.JobStatusRunning {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// DeleteJob deletes a job, its data and its stored artifacts. A running job is
// cancelled first.
func (m *Manager) DeleteJob(jobID string) error {
	job, err := m.GetJob(jobID)
	if err != nil {
		return notFound(err)
	}
	if job.Status == models.JobStatusRunning {
		_ = m.CancelJob(jobID)
		if !m.waitNotRunning(jobID, 30*time.Second) {
			return invalidState("job is still stopping; try again in a few seconds")
		}
	}
	return m.deleteJobData(jobID)
}

func (m *Manager) deleteJobData(jobID string) error {
	err := m.db.Transaction(func(tx *gorm.DB) error {
		for _, model := range []interface{}{&models.PageChange{}, &models.PageContent{}, &models.PageEmbedding{},
			&models.PhraseMatch{}, &models.FrontierURL{}, &models.CrawledPage{}} {
			if err := tx.Where("crawl_job_id = ?", jobID).Delete(model).Error; err != nil {
				return err
			}
		}
		tx.Model(&models.Subdomain{}).Where("crawl_job_id = ?", jobID).Update("crawl_job_id", nil)
		tx.Where("crawl_job_id = ? AND status IN ?", jobID, []string{models.DeliveryPending, models.DeliveryFailed}).Delete(&models.WebhookDelivery{})
		return tx.Delete(&models.CrawlJob{}, "id = ?", jobID).Error
	})
	if err != nil {
		return err
	}
	if store := m.engine.Store(); store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := store.DeletePrefix(ctx, storage.JobPrefix(jobID)); err != nil {
			log.Printf("[JobManager] Failed to delete stored artifacts of job %s: %v", jobID, err)
		}
	}
	if s := m.engine.GetSemanticSearcher(); s != nil {
		if p := s.IndexPathForCrawlJob(jobID); p != "" {
			_ = os.Remove(p)
		}
	}
	return nil
}

// DuplicateJob creates a new pending job with the same target and configuration.
func (m *Manager) DuplicateJob(jobID string) (*models.CrawlJob, error) {
	src, err := m.GetJob(jobID)
	if err != nil {
		return nil, notFound(err)
	}
	var settings *models.JobSettings
	if src.Settings != nil {
		settings = &models.JobSettings{}
		if err := deepCopy(src.Settings, settings); err != nil {
			return nil, err
		}
	}
	newJob := &models.CrawlJob{
		Kind:       src.Kind,
		TargetURL:  src.TargetURL,
		Domain:     src.Domain,
		SeedURLs:   append(models.StringSlice(nil), src.SeedURLs...),
		Status:     models.JobStatusPending,
		MaxDepth:   src.MaxDepth,
		Settings:   settings,
		OwnerRef:   src.OwnerRef,
		Priority:   src.Priority,
		Region:     src.Region,
		Limits:     src.Limits,
		Webhook:    src.Webhook,
		ScheduleID: src.ScheduleID,
	}
	if err := m.createJob(newJob); err != nil {
		return nil, err
	}
	log.Printf("[JobManager] Duplicated job %s → %s", src.ID, newJob.ID)
	return newJob, nil
}

func deepCopy(src, dst interface{}) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// UpdateJobSettings replaces the settings of a pending or paused job (nil resets to defaults).
func (m *Manager) UpdateJobSettings(jobID string, settings *models.JobSettings) error {
	job, err := m.GetJob(jobID)
	if err != nil {
		return notFound(err)
	}
	if !job.Status.SettingsEditable() {
		return invalidState("can only update settings for pending or paused jobs")
	}
	if settings == nil || settings.IsEmpty() {
		if err := m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Update("settings", nil).Error; err != nil {
			return err
		}
		if job.Settings != nil && len(job.Settings.TagRules) > 0 {
			_, err = m.retag(jobID, nil)
		}
		return err
	}
	if err := m.ValidateSettings(settings, job.Limits, true); err != nil {
		return err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	// Write the JSON column directly; Save() does not reliably persist the custom Valuer.
	if err := m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Update("settings", string(raw)).Error; err != nil {
		return err
	}
	// A paused job's pages follow its new tag rules.
	var old []models.TagRule
	if job.Settings != nil {
		old = job.Settings.TagRules
	}
	if !reflect.DeepEqual(old, settings.TagRules) {
		_, err = m.retag(jobID, settings.TagRules)
	}
	return err
}

// SecretMask is shown instead of stored webhook secrets; sending it back in
// an update keeps the stored secret.
const SecretMask = "********"

// JobOptions are the non-settings fields of a job editable before it runs.
type JobOptions struct {
	Priority *int                  `json:"priority,omitempty"`
	Region   *string               `json:"region,omitempty"`
	Limits   *models.JobLimits     `json:"limits,omitempty"`
	Webhook  *models.WebhookConfig `json:"webhook,omitempty"`
}

// UpdateJobOptions changes priority, region, limits and webhook of a job that is not running.
func (m *Manager) UpdateJobOptions(jobID string, o JobOptions) error {
	job, err := m.GetJob(jobID)
	if err != nil {
		return notFound(err)
	}
	if job.Status == models.JobStatusRunning {
		return invalidState("pause the job before changing its options")
	}
	updates := map[string]interface{}{}
	if o.Priority != nil {
		updates["priority"] = *o.Priority
	}
	if o.Region != nil {
		r := strings.TrimSpace(*o.Region)
		if r == "auto" {
			r = m.engine.Proxies().RegionFor(hostname(job.TargetURL))
		}
		updates["region"] = r
	}
	if o.Limits != nil {
		if err := m.ValidateSettings(job.Settings, o.Limits, true); err != nil {
			return err
		}
		if o.Limits.IsZero() {
			updates["limits"] = nil
		} else {
			b, _ := json.Marshal(o.Limits)
			updates["limits"] = string(b)
		}
	}
	if o.Webhook != nil {
		if err := validateWebhook(o.Webhook); err != nil {
			return err
		}
		if o.Webhook.Secret == SecretMask {
			o.Webhook.Secret = ""
			if job.Webhook != nil {
				o.Webhook.Secret = job.Webhook.Secret
			}
		}
		if strings.TrimSpace(o.Webhook.URL) == "" {
			updates["webhook"] = nil
		} else {
			b, _ := json.Marshal(o.Webhook)
			updates["webhook"] = string(b)
		}
	}
	if len(updates) == 0 {
		return nil
	}
	return m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Updates(updates).Error
}

// UpdateJobTarget updates the target URL (and seed URLs) of a pending job.
// Frontier rows are deleted so the next start re-seeds with the new URL(s).
func (m *Manager) UpdateJobTarget(jobID, targetURL string, seedURLs models.StringSlice) error {
	job, err := m.GetJob(jobID)
	if err != nil {
		return notFound(err)
	}
	if job.Status != models.JobStatusPending {
		return invalidState("can only edit the target URL of pending jobs")
	}
	fullURL, domain, err := ParseTarget(targetURL)
	if err != nil {
		return err
	}
	if err := m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Updates(map[string]interface{}{
		"target_url": fullURL,
		"domain":     domain,
		"seed_urls":  seedURLs,
	}).Error; err != nil {
		return err
	}
	if err := m.db.Where("crawl_job_id = ?", jobID).Delete(&models.FrontierURL{}).Error; err != nil {
		log.Printf("[JobManager] Failed to clear frontier for job %s: %v", jobID, err)
	}
	return nil
}

// GetDefaultJobSettings returns the global crawler configuration as JobSettings.
func (m *Manager) GetDefaultJobSettings() *models.JobSettings {
	cfg := m.config.Crawler
	maxConcurrent := cfg.MaxConcurrentRequests
	timeout := int(cfg.RequestTimeout.Seconds())
	delayMs := int(cfg.PolitenessDelay.Milliseconds())
	renderWait := cfg.HeadlessRenderWait
	if renderWait <= 0 {
		renderWait = 5
	}
	fingerprint := m.config.Network.TLSFingerprint
	hostRate := m.config.Network.HostRateLimit
	pool := m.config.Network.DefaultProxyPool
	f := false
	return &models.JobSettings{
		MaxConcurrentRequests: &maxConcurrent,
		RequestTimeoutSec:     &timeout,
		PolitenessDelayMs:     &delayMs,
		MaxDepth:              &cfg.MaxDepth,
		MaxPages:              &cfg.MaxPages,
		UserAgent:             &cfg.UserAgent,
		MaxRetries:            &cfg.MaxRetries,
		RespectRobotsTxt:      &cfg.RespectRobotsTxt,
		SkipContentDuplicates: &cfg.SkipContentDuplicates,
		UseHeadlessBrowser:    &cfg.UseHeadlessBrowser,
		HeadlessWaitSelector:  &cfg.HeadlessWaitSelector,
		HeadlessRenderWait:    &renderWait,
		EnableSemanticSearch:  &cfg.EnableSemanticSearch,
		SaveTextContent:       &cfg.SaveTextContent,
		AfterCrawlPlugin:      &cfg.AfterCrawlPlugin,
		AfterJobPlugin:        &cfg.AfterJobPlugin,
		EnableWordExtraction:  &cfg.EnableWordExtraction,
		EnableStemming:        &cfg.EnableStemming,
		EnableLemmatization:   &cfg.EnableLemmatization,
		DefaultLanguage:       &cfg.DefaultLanguage,
		UseCrawlPhrasesOnly:   &cfg.UseCrawlPhrasesOnly,
		SkipExtensions:        cfg.SkipExtensions,
		TLSFingerprint:        &fingerprint,
		HostRateLimit:         &hostRate,
		ProxyPool:             &pool,
		HeadlessFallback:      &f,
		StickyProxy:           &f,
		OnlyMainContent:       &f,
		Screenshot:            &f,
		FullPageScreenshot:    &f,
		ChangeDetection:       &f,
	}
}
