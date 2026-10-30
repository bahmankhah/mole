package jobs

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/netx"
)

// Webhook event types.
const (
	EventJobQueued    = "job.queued"
	EventJobStarted   = "job.started"
	EventJobProgress  = "job.progress"
	EventJobPaused    = "job.paused"
	EventJobCompleted = "job.completed"
	EventJobFailed    = "job.failed"
	EventJobCancelled = "job.cancelled"
	EventJobChanges   = "job.changes"
	EventPageCrawled  = "page.crawled"
	EventPageChanged  = "page.changed"
)

// AllEvents lists every webhook event.
var AllEvents = []string{EventJobQueued, EventJobStarted, EventJobProgress, EventJobPaused, EventJobCompleted,
	EventJobFailed, EventJobCancelled, EventJobChanges, EventPageCrawled, EventPageChanged}

// defaultEvents are sent when a webhook does not list events: lifecycle
// changes only, not the high-volume progress and per-page events.
var defaultEvents = map[string]bool{
	EventJobStarted: true, EventJobPaused: true, EventJobCompleted: true,
	EventJobFailed: true, EventJobCancelled: true, EventJobChanges: true,
}

func validEvent(ev string) bool {
	if ev == "*" {
		return true
	}
	for _, e := range AllEvents {
		if e == ev {
			return true
		}
	}
	return false
}

func wantsEvent(w *models.WebhookConfig, event string) bool {
	if w == nil || w.URL == "" {
		return false
	}
	if len(w.Events) == 0 {
		return defaultEvents[event]
	}
	for _, e := range w.Events {
		if e == "*" || e == event {
			return true
		}
	}
	return false
}

// JobSummary is the job representation embedded in webhook payloads and API responses.
type JobSummary struct {
	ID           string           `json:"id"`
	Kind         string           `json:"kind"`
	ExternalID   string           `json:"external_id,omitempty"`
	OwnerRef     string           `json:"owner_ref,omitempty"`
	ScheduleID   *string          `json:"schedule_id,omitempty"`
	Status       models.JobStatus `json:"status"`
	StopReason   string           `json:"stop_reason,omitempty"`
	TargetURL    string           `json:"target_url"`
	CrawledURLs  int              `json:"crawled_urls"`
	TotalURLs    int              `json:"total_urls"`
	FoundMatches int              `json:"found_matches"`
	Usage        models.JobUsage  `json:"usage"`
	Error        string           `json:"error,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	StartedAt    *time.Time       `json:"started_at,omitempty"`
	CompletedAt  *time.Time       `json:"completed_at,omitempty"`
}

// Summarize builds the summary of a job.
func Summarize(j *models.CrawlJob) JobSummary {
	return JobSummary{
		ID: j.ID, Kind: j.Kind, ExternalID: j.ExternalID, OwnerRef: j.OwnerRef, ScheduleID: j.ScheduleID,
		Status: j.Status, StopReason: j.StopReason, TargetURL: j.TargetURL,
		CrawledURLs: j.CrawledURLs, TotalURLs: j.TotalURLs, FoundMatches: j.FoundMatches,
		Usage: j.Usage, Error: j.ErrorMessage, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, CompletedAt: j.CompletedAt,
	}
}

type webhookPayload struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	CreatedAt time.Time              `json:"created_at"`
	Job       JobSummary             `json:"job"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

func (m *Manager) onEngineEvent(job *models.CrawlJob, event string, data map[string]interface{}) {
	m.emit(job, event, data)
}

// emit writes an event to the webhook outbox if the job subscribes to it.
func (m *Manager) emit(job *models.CrawlJob, event string, data map[string]interface{}) {
	if job == nil || !wantsEvent(job.Webhook, event) {
		return
	}
	eventID := uuid.New().String()
	payload, err := json.Marshal(webhookPayload{ID: eventID, Type: event, CreatedAt: time.Now().UTC(), Job: Summarize(job), Data: data})
	if err != nil {
		log.Printf("[Webhooks] Failed to encode %s for job %s: %v", event, job.ID, err)
		return
	}
	now := time.Now()
	d := models.WebhookDelivery{
		EventID:       eventID,
		CrawlJobID:    job.ID,
		OwnerRef:      job.OwnerRef,
		Event:         event,
		URL:           job.Webhook.URL,
		Secret:        job.Webhook.Secret,
		Payload:       string(payload),
		Status:        models.DeliveryPending,
		NextAttemptAt: &now,
	}
	if err := m.db.Create(&d).Error; err != nil {
		log.Printf("[Webhooks] Failed to queue %s for job %s: %v", event, job.ID, err)
		return
	}
	select {
	case m.deliverWake <- struct{}{}:
	default:
	}
}

func newWebhookClient(cfg *config.Config) *http.Client {
	guard, err := netx.NewGuard(cfg.Webhooks.AllowPrivate, cfg.Security.AllowedCIDRs)
	if err != nil {
		guard, _ = netx.NewGuard(false, nil)
	}
	timeout := cfg.Webhooks.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: netx.NewTransport(netx.TransportOptions{Fingerprint: netx.FingerprintGo, Guard: guard}),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // receivers must answer at the configured URL
		},
	}
}

// Sign returns the X-Mole-Signature header value for a payload:
// "t=<unix>,v1=<hex HMAC-SHA256 of "<unix>.<payload>">".
func Sign(secret string, ts int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func (m *Manager) deliveryLoop(ctx context.Context) {
	defer m.loops.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		m.deliverDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.deliverWake:
		}
	}
}

func (m *Manager) deliverDue(ctx context.Context) {
	now := time.Now()
	// Deliveries stuck in "sending" (worker died mid-request) become pending again.
	m.db.Model(&models.WebhookDelivery{}).
		Where("status = ? AND next_attempt_at < ?", models.DeliverySending, now).
		Update("status", models.DeliveryPending)

	var due []models.WebhookDelivery
	if err := m.db.Where("status = ? AND next_attempt_at <= ?", models.DeliveryPending, now).
		Order("id ASC").Limit(25).Find(&due).Error; err != nil {
		return
	}
	for _, d := range due {
		if ctx.Err() != nil {
			return
		}
		claimUntil := time.Now().Add(5 * time.Minute)
		res := m.db.Model(&models.WebhookDelivery{}).Where("id = ? AND status = ?", d.ID, models.DeliveryPending).
			Updates(map[string]interface{}{"status": models.DeliverySending, "next_attempt_at": claimUntil})
		if res.RowsAffected == 0 {
			continue
		}
		m.deliver(ctx, &d)
	}
}

func (m *Manager) deliver(ctx context.Context, d *models.WebhookDelivery) {
	attempts := d.Attempts + 1
	code, err := m.send(ctx, d)
	now := time.Now()
	updates := map[string]interface{}{"attempts": attempts, "response_code": code}
	if err == nil {
		updates["status"] = models.DeliveryDelivered
		updates["delivered_at"] = now
		updates["last_error"] = ""
		updates["next_attempt_at"] = nil
	} else {
		updates["last_error"] = err.Error()
		maxAttempts := m.config.Webhooks.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 8
		}
		if attempts >= maxAttempts {
			updates["status"] = models.DeliveryFailed
			updates["next_attempt_at"] = nil
			log.Printf("[Webhooks] Giving up on %s %s after %d attempts: %v", d.Event, d.EventID, attempts, err)
		} else {
			// Exponential backoff: 10s, 20s, 40s ... capped at 1h.
			backoff := time.Duration(10<<uint(attempts-1)) * time.Second
			if backoff > time.Hour {
				backoff = time.Hour
			}
			updates["status"] = models.DeliveryPending
			updates["next_attempt_at"] = now.Add(backoff)
		}
	}
	m.db.Model(&models.WebhookDelivery{}).Where("id = ?", d.ID).Updates(updates)
}

func (m *Manager) send(ctx context.Context, d *models.WebhookDelivery) (int, error) {
	payload := []byte(d.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mole-Webhooks/1.0")
	req.Header.Set("X-Mole-Event", d.Event)
	req.Header.Set("X-Mole-Delivery", d.EventID)
	if d.Secret != "" {
		req.Header.Set("X-Mole-Signature", Sign(d.Secret, time.Now().Unix(), payload))
	}
	resp, err := m.webhookClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("receiver answered HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// GetDeliveries lists webhook deliveries of a job, newest first.
func (m *Manager) GetDeliveries(jobID string, limit int) ([]models.WebhookDelivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []models.WebhookDelivery
	err := m.db.Where("crawl_job_id = ?", jobID).Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}

// RedeliverWebhook queues a failed or delivered event again.
func (m *Manager) RedeliverWebhook(id uint) error {
	now := time.Now()
	res := m.db.Model(&models.WebhookDelivery{}).Where("id = ? AND status <> ?", id, models.DeliverySending).
		Updates(map[string]interface{}{"status": models.DeliveryPending, "next_attempt_at": now, "attempts": 0})
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: delivery %s", ErrNotFound, strconv.FormatUint(uint64(id), 10))
	}
	select {
	case m.deliverWake <- struct{}{}:
	default:
	}
	return nil
}
