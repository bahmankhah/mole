package jobs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrJoinTokenInvalid is returned for an unknown, expired or revoked join token.
var ErrJoinTokenInvalid = errors.New("join token is invalid, expired or revoked")

// joinTokenPrefix marks Mole join tokens so they are easy to recognise in configs and logs.
const joinTokenPrefix = "mjt_"

// workerIDInUseWindow is how recently another host must have sent a heartbeat
// with our worker id for this process to refuse to start (three heartbeats).
const workerIDInUseWindow = 45 * time.Second

var regionPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{0,32}$`)

// HashJoinToken returns the stored form of a join token.
func HashJoinToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// JoinTokenSpec describes a new join token.
type JoinTokenSpec struct {
	Name   string
	Region string        // region given to nodes that join with it ("" = any)
	TTL    time.Duration // 0 = never expires
}

// CreateJoinToken stores a new join token and returns it with its secret,
// which is shown once and never stored.
func (m *Manager) CreateJoinToken(spec JoinTokenSpec) (*models.JoinToken, string, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Region = strings.TrimSpace(spec.Region)
	if len(spec.Name) > 255 {
		return nil, "", invalidInput("name is longer than 255 characters")
	}
	if !regionPattern.MatchString(spec.Region) {
		return nil, "", invalidInput("region may only contain letters, digits, '.', '_' and '-' (at most 32)")
	}
	if spec.TTL < 0 {
		return nil, "", invalidInput("ttl must not be negative")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, "", err
	}
	secret := joinTokenPrefix + hex.EncodeToString(buf)
	t := &models.JoinToken{
		ID:        uuid.New().String(),
		Name:      spec.Name,
		TokenHash: HashJoinToken(secret),
		Prefix:    secret[:len(joinTokenPrefix)+6],
		Region:    spec.Region,
	}
	if spec.TTL > 0 {
		exp := time.Now().Add(spec.TTL)
		t.ExpiresAt = &exp
	}
	if err := m.db.Create(t).Error; err != nil {
		return nil, "", err
	}
	return t, secret, nil
}

// ListJoinTokens returns every join token, newest first.
func (m *Manager) ListJoinTokens() ([]models.JoinToken, error) {
	var out []models.JoinToken
	err := m.db.Order("created_at DESC").Find(&out).Error
	return out, err
}

// RevokeJoinToken stops a join token from working. Nodes that already joined
// with it keep running, but can no longer sync their configuration, so they
// will not start again until they join with another token.
func (m *Manager) RevokeJoinToken(id string) error {
	now := time.Now()
	res := m.db.Model(&models.JoinToken{}).Where("id = ?", id).Update("revoked_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: no join token with id %s", ErrNotFound, id)
	}
	return nil
}

// CheckJoinToken checks a join token presented by a node. With record set it
// also counts the use (a node joining or syncing its configuration).
func (m *Manager) CheckJoinToken(secret string, record bool) (*models.JoinToken, error) {
	secret = strings.TrimSpace(secret)
	if !strings.HasPrefix(secret, joinTokenPrefix) {
		return nil, ErrJoinTokenInvalid
	}
	var t models.JoinToken
	if err := m.db.Where("token_hash = ?", HashJoinToken(secret)).Limit(1).Find(&t).Error; err != nil {
		return nil, err
	}
	if t.ID == "" {
		return nil, ErrJoinTokenInvalid
	}
	now := time.Now()
	if !t.Usable(now) {
		return nil, ErrJoinTokenInvalid
	}
	if !record {
		return &t, nil
	}
	m.db.Model(&models.JoinToken{}).Where("id = ?", t.ID).Updates(map[string]interface{}{
		"last_used_at": now,
		"use_count":    gorm.Expr("use_count + 1"),
	})
	return &t, nil
}

// SetWorkerState cordons, drains or reactivates a worker. The worker applies
// it within one poll interval and keeps it across restarts.
func (m *Manager) SetWorkerState(workerID, state string) error {
	switch state {
	case models.WorkerActive, models.WorkerCordoned, models.WorkerDraining:
	default:
		return invalidInput("state must be %s, %s or %s", models.WorkerActive, models.WorkerCordoned, models.WorkerDraining)
	}
	var known int64
	m.db.Model(&models.WorkerHeartbeat{}).Where("id = ?", workerID).Count(&known)
	if known == 0 {
		m.db.Model(&models.WorkerControl{}).Where("worker_id = ?", workerID).Count(&known)
	}
	if known == 0 {
		return fmt.Errorf("%w: no worker with id %s", ErrNotFound, workerID)
	}
	var err error
	if state == models.WorkerActive {
		err = m.db.Delete(&models.WorkerControl{}, "worker_id = ?", workerID).Error
	} else {
		err = m.db.Clauses(clause.OnConflict{UpdateAll: true}).
			Create(&models.WorkerControl{WorkerID: workerID, State: state, UpdatedAt: time.Now()}).Error
	}
	if err == nil && workerID == m.workerID {
		m.nudge()
	}
	return err
}

// workerState returns this worker's operator-set state. On a database error it
// keeps the last state it read.
func (m *Manager) workerState() string {
	var ctl models.WorkerControl
	err := m.db.Where("worker_id = ?", m.workerID).Limit(1).Find(&ctl).Error
	switch {
	case err != nil:
		if m.state == "" {
			return models.WorkerActive
		}
		return m.state
	case ctl.State == "":
		return models.WorkerActive
	}
	return ctl.State
}

// applyWorkerState reads this worker's state and acts on it: a draining worker
// hands its running jobs back to the queue. It reports whether the worker may
// claim new jobs.
func (m *Manager) applyWorkerState() bool {
	state := m.workerState()
	if state != m.state {
		if m.state != "" || state != models.WorkerActive {
			log.Printf("[JobManager] Worker %s is now %s", m.workerID, state)
		}
		m.state = state
	}
	if state == models.WorkerDraining {
		if n := m.engine.RequeueAll("worker_drain"); n > 0 {
			log.Printf("[JobManager] Draining: handing %d running job(s) back to the queue", n)
		}
	}
	return state == models.WorkerActive
}

// checkWorkerID refuses to start when another machine is using this worker id:
// two processes with one id would steal each other's jobs.
func (m *Manager) checkWorkerID() error {
	host, _ := os.Hostname()
	var hb models.WorkerHeartbeat
	if err := m.db.Where("id = ?", m.workerID).Limit(1).Find(&hb).Error; err != nil {
		return fmt.Errorf("check worker id: %w", err)
	}
	if hb.ID != "" && hb.Hostname != host && time.Since(hb.LastSeenAt) < workerIDInUseWindow {
		return fmt.Errorf("worker id %q is in use by host %q (last seen %s ago); give this worker another id with worker.id or MOLE_WORKER_ID",
			m.workerID, hb.Hostname, time.Since(hb.LastSeenAt).Round(time.Second))
	}
	return nil
}

// detectBrowser decides whether this worker can run jobs that need the
// headless browser. worker.browser overrides the check.
func (m *Manager) detectBrowser() bool {
	if b := m.config.Worker.Browser; b != nil {
		return *b
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg := m.config.Crawler
	python := modules.FindPython(cfg.PythonPath)
	script := modules.FindPluginScript("headless_fetch.py", cfg.HeadlessScriptPath)
	if err := modules.CheckBrowser(ctx, python, script); err != nil {
		log.Printf("[JobManager] No headless browser on this worker (%v); jobs that need one are left to other workers", err)
		return false
	}
	return true
}

// HasBrowser reports whether this worker runs jobs that need the headless browser.
func (m *Manager) HasBrowser() bool { return m.browser.Load() }
