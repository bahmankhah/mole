package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/resolver/crawler/models"
	"gorm.io/gorm/clause"
)

// ErrIdempotencyConflict means a key was reused with a different request body.
var ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")

// OwnerDeletion reports what DeleteOwner removed.
type OwnerDeletion struct {
	OwnerRef  string `json:"owner_ref"`
	Jobs      int    `json:"jobs"`
	Schedules int64  `json:"schedules"`
}

// DeleteOwner removes every job, schedule and artifact of an owner (account deletion).
func (m *Manager) DeleteOwner(ownerRef string) (*OwnerDeletion, error) {
	if ownerRef == "" {
		return nil, invalidInput("owner_ref is required")
	}
	res := &OwnerDeletion{OwnerRef: ownerRef}
	sch := m.db.Where("owner_ref = ?", ownerRef).Delete(&models.Schedule{})
	if sch.Error != nil {
		return nil, sch.Error
	}
	res.Schedules = sch.RowsAffected

	var jobs []models.CrawlJob
	if err := m.db.Select("id", "status").Where("owner_ref = ?", ownerRef).Find(&jobs).Error; err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Status == models.JobStatusRunning {
			_ = m.CancelJob(j.ID)
		}
	}
	for _, j := range jobs {
		if j.Status == models.JobStatusRunning && !m.waitNotRunning(j.ID, 30*time.Second) {
			log.Printf("[JobManager] Job %s still stopping; deleting anyway", j.ID)
		}
		if err := m.deleteJobData(j.ID); err != nil {
			return res, fmt.Errorf("delete job %s: %w", j.ID, err)
		}
		res.Jobs++
	}
	m.db.Where("owner_ref = ?", ownerRef).Delete(&models.WebhookDelivery{})
	m.db.Where("owner_ref = ?", ownerRef).Delete(&models.IdempotencyKey{})
	m.db.Where("owner_ref = ?", ownerRef).Delete(&models.OwnerLimit{})
	m.db.Where("owner_ref = ?", ownerRef).Delete(&models.ScrapeCacheEntry{})
	return res, nil
}

// OwnerLimits is the effective concurrency limit of an owner.
type OwnerLimits struct {
	OwnerRef   string     `json:"owner_ref"`
	MaxRunning int        `json:"max_running"` // 0 = unlimited
	Source     string     `json:"source"`      // "owner" (set for this owner) or "default" (worker.max_running_per_owner)
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// MaxOwnerRunning caps max_running so a typo cannot disable fairness.
const MaxOwnerRunning = 1000

// GetOwnerLimits returns an owner's limits: its override or the engine default.
func (m *Manager) GetOwnerLimits(ownerRef string) (*OwnerLimits, error) {
	if ownerRef == "" {
		return nil, invalidInput("owner_ref is required")
	}
	var rows []models.OwnerLimit
	if err := m.db.Where("owner_ref = ?", ownerRef).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &OwnerLimits{OwnerRef: ownerRef, MaxRunning: m.config.Worker.MaxRunningPerOwner, Source: "default"}, nil
	}
	return &OwnerLimits{OwnerRef: ownerRef, MaxRunning: rows[0].MaxRunning, Source: "owner", UpdatedAt: &rows[0].UpdatedAt}, nil
}

// SetOwnerLimits stores an owner's concurrency limit (e.g. from its plan).
func (m *Manager) SetOwnerLimits(ownerRef string, maxRunning int) (*OwnerLimits, error) {
	if ownerRef == "" || len(ownerRef) > 191 {
		return nil, invalidInput("owner_ref must be 1 to 191 characters")
	}
	if maxRunning < 0 || maxRunning > MaxOwnerRunning {
		return nil, invalidInput("max_running must be between 0 (unlimited) and %d", MaxOwnerRunning)
	}
	row := models.OwnerLimit{OwnerRef: ownerRef, MaxRunning: maxRunning, UpdatedAt: time.Now()}
	if err := m.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	m.nudge()
	return m.GetOwnerLimits(ownerRef)
}

// DeleteOwnerLimits removes an owner's override; the engine default applies again.
func (m *Manager) DeleteOwnerLimits(ownerRef string) error {
	if ownerRef == "" {
		return invalidInput("owner_ref is required")
	}
	if err := m.db.Where("owner_ref = ?", ownerRef).Delete(&models.OwnerLimit{}).Error; err != nil {
		return err
	}
	m.nudge()
	return nil
}

// OwnerUsage sums usage of an owner's jobs created in [from, to).
type OwnerUsage struct {
	OwnerRef string          `json:"owner_ref"`
	From     time.Time       `json:"from"`
	To       time.Time       `json:"to"`
	Jobs     int64           `json:"jobs"`
	Usage    models.JobUsage `json:"usage"`
}

// GetOwnerUsage aggregates usage for billing.
func (m *Manager) GetOwnerUsage(ownerRef string, from, to time.Time) (*OwnerUsage, error) {
	out := &OwnerUsage{OwnerRef: ownerRef, From: from, To: to}
	row := struct {
		Jobs                                                                              int64
		Requests, Pages, Bytes, BrowserMs, LLMTokens, Errors, Shots, Proxy, DocumentPages int64
	}{}
	err := m.db.Model(&models.CrawlJob{}).
		Select(`COUNT(*) AS jobs, COALESCE(SUM(usage_requests),0) AS requests, COALESCE(SUM(usage_pages),0) AS pages,
			COALESCE(SUM(usage_bytes),0) AS bytes, COALESCE(SUM(usage_browser_ms),0) AS browser_ms,
			COALESCE(SUM(usage_llm_tokens),0) AS llm_tokens, COALESCE(SUM(usage_errors),0) AS errors,
			COALESCE(SUM(usage_screenshots),0) AS shots, COALESCE(SUM(usage_proxy_requests),0) AS proxy,
			COALESCE(SUM(usage_document_pages),0) AS document_pages`).
		Where("owner_ref = ? AND created_at >= ? AND created_at < ?", ownerRef, from, to).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	out.Jobs = row.Jobs
	out.Usage = models.JobUsage{Requests: row.Requests, Pages: row.Pages, Bytes: row.Bytes, BrowserMs: row.BrowserMs,
		LLMTokens: row.LLMTokens, Errors: row.Errors, Screenshots: row.Shots, ProxyRequests: row.Proxy,
		DocumentPages: row.DocumentPages}
	return out, nil
}

// HashRequest fingerprints a request body for idempotency checks.
func HashRequest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// ErrIdempotencyInProgress means another request with the same key is still running.
var ErrIdempotencyInProgress = errors.New("a request with this idempotency key is still in progress")

// ReserveIdempotency claims key before the resource is created. It returns
// the id of the resource created earlier for the same key and request, or ""
// when the caller now owns the key and must call CompleteIdempotency (or
// ReleaseIdempotency on failure).
func (m *Manager) ReserveIdempotency(ownerRef, key, scope, requestHash string) (string, error) {
	rec := models.IdempotencyKey{OwnerRef: ownerRef, Key: key, Scope: scope, RequestHash: requestHash, CreatedAt: time.Now()}
	if err := m.db.Create(&rec).Error; err == nil {
		return "", nil
	}
	var existing models.IdempotencyKey
	if err := m.db.Where("owner_ref = ? AND `key` = ?", ownerRef, key).First(&existing).Error; err != nil {
		return "", err
	}
	if existing.Scope != scope || existing.RequestHash != requestHash {
		return "", ErrIdempotencyConflict
	}
	if existing.ResourceID == "" {
		return "", ErrIdempotencyInProgress
	}
	return existing.ResourceID, nil
}

// CompleteIdempotency records the resource created for a reserved key.
func (m *Manager) CompleteIdempotency(ownerRef, key, resourceID string) {
	m.db.Model(&models.IdempotencyKey{}).Where("owner_ref = ? AND `key` = ?", ownerRef, key).Update("resource_id", resourceID)
}

// ReleaseIdempotency frees a reserved key after the request failed.
func (m *Manager) ReleaseIdempotency(ownerRef, key string) {
	m.db.Where("owner_ref = ? AND `key` = ? AND resource_id = ''", ownerRef, key).Delete(&models.IdempotencyKey{})
}
