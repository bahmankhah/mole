package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/resolver/crawler/crawler"
	"github.com/resolver/crawler/database"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"github.com/resolver/crawler/plugins"
)

// Version is reported in worker heartbeats; set by the cmd package.
var Version = "dev"

// schedulerLoop claims queued jobs, fires schedules and after-job plugins.
func (m *Manager) schedulerLoop(ctx context.Context) {
	defer m.loops.Done()
	interval := m.config.Worker.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastHeartbeat, lastCleanup time.Time
	for {
		m.reclaimExpiredLeases()
		if m.applyWorkerState() {
			m.claimJobs()
		}
		m.fireSchedules()
		m.runDueAfterJobPlugins(ctx)
		if time.Since(lastHeartbeat) >= 15*time.Second {
			lastHeartbeat = time.Now()
			m.heartbeat()
		}
		if time.Since(lastCleanup) >= time.Hour {
			lastCleanup = time.Now()
			m.cleanup()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.wake:
		}
	}
}

// reclaimExpiredLeases requeues jobs whose worker stopped renewing its lease
// (crashed or lost its database connection).
func (m *Manager) reclaimExpiredLeases() {
	now := time.Now()
	var stale []models.CrawlJob
	if err := m.db.Select("id", "worker_id").
		Where("status = ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", models.JobStatusRunning, now).
		Limit(100).Find(&stale).Error; err != nil {
		return
	}
	for _, j := range stale {
		if m.engine.IsRunning(j.ID) {
			continue // ours and alive; the next flush renews the lease
		}
		res := m.db.Model(&models.CrawlJob{}).
			Where("id = ? AND status = ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", j.ID, models.JobStatusRunning, now).
			Updates(map[string]interface{}{"status": models.JobStatusQueued, "worker_id": "", "lease_expires_at": nil})
		if res.RowsAffected > 0 {
			log.Printf("[Scheduler] Requeued job %s: lease of worker %q expired", j.ID, j.WorkerID)
		}
	}
}

// claimJobs starts queued jobs while this worker has free slots. Jobs pinned
// to another region, and jobs that need a browser when this worker has none,
// are left to other workers.
func (m *Manager) claimJobs() {
	free := m.config.Worker.MaxRunningJobs - m.engine.RunningCount()
	if free <= 0 {
		return
	}
	q := m.db.Select("id", "owner_ref", "settings").Where("status = ?", models.JobStatusQueued)
	if region := m.config.Worker.Region; region != "" {
		q = q.Where("region IN ?", []string{"", region})
	} else {
		q = q.Where("region = ''")
	}
	// Look past the first few jobs, which may all belong to owners at their
	// limit or need a browser this worker lacks.
	var candidates []models.CrawlJob
	if err := q.Order("priority DESC").Order("queued_at ASC").Limit(max(free*4, 50)).Find(&candidates).Error; err != nil {
		log.Printf("[Scheduler] Failed to list queued jobs: %v", err)
		return
	}
	hasBrowser := m.browser.Load()
	limits := m.ownerLimitsFor(candidates)
	ownerFull := map[string]bool{}
	for _, c := range candidates {
		if free <= 0 {
			return
		}
		if !hasBrowser && m.engine.NeedsBrowser(c.Settings) {
			continue
		}
		limit := m.config.Worker.MaxRunningPerOwner
		if l, ok := limits[c.OwnerRef]; ok {
			limit = l
		}
		if c.OwnerRef == "" || limit <= 0 {
			if m.claimAndStart(c.ID) {
				free--
			}
			continue
		}
		if ownerFull[c.OwnerRef] {
			continue
		}
		claimed, full := m.claimForOwner(c.ID, c.OwnerRef, limit)
		if full {
			ownerFull[c.OwnerRef] = true
		}
		if claimed && m.startClaimed(c.ID) {
			free--
		}
	}
}

// ownerLimitsFor loads the per-owner overrides of worker.max_running_per_owner
// for the owners of jobs.
func (m *Manager) ownerLimitsFor(jobs []models.CrawlJob) map[string]int {
	owners := make([]string, 0, len(jobs))
	seen := map[string]bool{}
	for _, j := range jobs {
		if j.OwnerRef != "" && !seen[j.OwnerRef] {
			seen[j.OwnerRef] = true
			owners = append(owners, j.OwnerRef)
		}
	}
	out := map[string]int{}
	if len(owners) == 0 {
		return out
	}
	var rows []models.OwnerLimit
	if err := m.db.Where("owner_ref IN ?", owners).Find(&rows).Error; err != nil {
		log.Printf("[Scheduler] Failed to load owner limits: %v", err)
		return out
	}
	for _, r := range rows {
		out[r.OwnerRef] = r.MaxRunning
	}
	return out
}

// claimForOwner claims jobID unless its owner already runs limit jobs on all
// workers together. Counting and claiming happen under a per-owner database
// lock, so two workers cannot both take the owner's last slot.
func (m *Manager) claimForOwner(jobID, owner string, limit int) (claimed, full bool) {
	sum := sha256.Sum256([]byte(owner))
	lock := "mole:owner:" + hex.EncodeToString(sum[:16])
	_, err := database.WithLock(m.db, lock, 5*time.Second, func() error {
		var n int64
		if err := m.db.Model(&models.CrawlJob{}).Where("owner_ref = ? AND status = ?", owner, models.JobStatusRunning).Count(&n).Error; err != nil {
			return err
		}
		if n >= int64(limit) {
			full = true
			return nil
		}
		claimed = m.claim(jobID)
		return nil
	})
	if err != nil {
		log.Printf("[Scheduler] Failed to claim job %s for owner %s: %v", jobID, owner, err)
	}
	return claimed, full
}

func (m *Manager) claimAndStart(jobID string) bool {
	return m.claim(jobID) && m.startClaimed(jobID)
}

// claim marks a queued job as running on this worker. It reports false when
// another worker won it.
func (m *Manager) claim(jobID string) bool {
	lease := time.Now().Add(m.config.Worker.LeaseTTL)
	res := m.db.Model(&models.CrawlJob{}).
		Where("id = ? AND status = ?", jobID, models.JobStatusQueued).
		Updates(map[string]interface{}{"status": models.JobStatusRunning, "worker_id": m.workerID,
			"lease_expires_at": lease, "control": "", "stop_reason": ""})
	return res.Error == nil && res.RowsAffected > 0
}

// startClaimed starts a job this worker has claimed; a job that cannot start fails.
func (m *Manager) startClaimed(jobID string) bool {
	job, err := m.GetJob(jobID)
	if err != nil {
		return false
	}
	if err := m.engine.StartRun(job); err != nil {
		log.Printf("[Scheduler] Job %s failed to start: %v", jobID, err)
		now := time.Now()
		m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Updates(map[string]interface{}{
			"status": models.JobStatusFailed, "error_message": err.Error(), "completed_at": now,
			"worker_id": "", "lease_expires_at": nil,
		})
		job.Status = models.JobStatusFailed
		job.ErrorMessage = err.Error()
		m.emit(job, EventJobFailed, map[string]interface{}{"error": err.Error()})
		return false
	}
	return true
}

// onRunEnded is called by the engine after a run wrote its final state.
func (m *Manager) onRunEnded(job *models.CrawlJob, out crawler.Outcome) {
	data := map[string]interface{}{"stop_reason": out.StopReason}
	if out.Err != nil {
		data["error"] = out.Err.Error()
	}
	switch out.Status {
	case models.JobStatusCompleted:
		m.emit(job, EventJobCompleted, data)
	case models.JobStatusFailed:
		m.emit(job, EventJobFailed, data)
	case models.JobStatusCancelled:
		m.emit(job, EventJobCancelled, data)
	case models.JobStatusPaused:
		m.emit(job, EventJobPaused, data)
	}
	if out.Changes != nil && (out.Changes.New+out.Changes.Changed+out.Changes.Removed) > 0 {
		m.emit(job, EventJobChanges, map[string]interface{}{"changes": out.Changes})
	}
	m.nudge()
}

// fireSchedules creates jobs for schedules that are due.
func (m *Manager) fireSchedules() {
	now := time.Now()
	var due []models.Schedule
	if err := m.db.Where("enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", true, now).
		Order("next_run_at ASC").Limit(20).Find(&due).Error; err != nil {
		return
	}
	for _, s := range due {
		next, err := NextRun(s.Cron, s.Timezone, now)
		if err != nil {
			log.Printf("[Scheduler] Disabling schedule %s: %v", s.ID, err)
			m.db.Model(&models.Schedule{}).Where("id = ?", s.ID).Update("enabled", false)
			continue
		}
		// Claim this firing; with several workers only one advances next_run_at.
		res := m.db.Model(&models.Schedule{}).
			Where("id = ? AND next_run_at = ?", s.ID, s.NextRunAt).
			Updates(map[string]interface{}{"next_run_at": next, "last_run_at": now})
		if res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		if _, err := m.RunSchedule(&s); err != nil {
			log.Printf("[Scheduler] Schedule %s (%s) did not run: %v", s.ID, s.Name, err)
		}
	}
}

// runDueAfterJobPlugins starts after-job plugins whose delay elapsed. A run
// goes to the worker that ran the crawl (plugins keep per-page files on that
// machine); other workers take it only when that worker is no longer alive.
func (m *Manager) runDueAfterJobPlugins(ctx context.Context) {
	// A run that outlived the 30-minute limit by far was interrupted (crash or restart).
	m.db.Model(&models.CrawlJob{}).
		Where("after_job_state = ? AND after_job_due_at < ?", "running", time.Now().Add(-45*time.Minute)).
		Updates(map[string]interface{}{"after_job_state": "failed", "after_job_output": "[mole] interrupted: the worker running this plugin stopped"})
	var due []models.CrawlJob
	if err := m.db.Select("id", "after_job_plugin", "after_job_worker").
		Where("after_job_state = ? AND after_job_due_at <= ?", "scheduled", time.Now()).
		Limit(20).Find(&due).Error; err != nil || len(due) == 0 {
		return
	}
	alive := map[string]bool{m.workerID: true}
	for _, w := range m.Workers() {
		alive[w.ID] = true
	}
	started := 0
	for _, j := range due {
		if started >= 5 {
			return
		}
		if j.AfterJobWorker != "" && j.AfterJobWorker != m.workerID && alive[j.AfterJobWorker] {
			continue // its own worker is up and will run it
		}
		res := m.db.Model(&models.CrawlJob{}).Where("id = ? AND after_job_state = ?", j.ID, "scheduled").
			Update("after_job_state", "running")
		if res.RowsAffected == 0 {
			continue
		}
		started++
		m.loops.Add(1)
		go func(jobID, pluginID string) {
			defer m.loops.Done()
			m.finishAfterJob(jobID, pluginID, m.runAfterJobPlugin(ctx, jobID, pluginID))
		}(j.ID, j.AfterJobPlugin)
	}
}

// afterJobResult is the outcome of one after-job plugin run.
type afterJobResult struct {
	output string
	err    error
}

func (m *Manager) finishAfterJob(jobID, pluginID string, res afterJobResult) {
	state := "done"
	if res.err != nil {
		state = "failed"
		log.Printf("[AfterJob] Plugin %s for job %s failed: %v", pluginID, jobID, res.err)
		res.output = strings.TrimRight(res.output, "\n") + "\n[mole] " + res.err.Error()
	}
	now := time.Now()
	m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Updates(map[string]interface{}{
		"after_job_state": state, "after_job_output": tail(res.output, 8000), "after_job_finished_at": now,
	})
}

// tail keeps the last n bytes of s, starting at a line boundary when possible.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return "…\n" + s
}

// runAfterJobPlugin runs a plugin's after-job script with --job-id. Output is
// appended to after_job.log in the plugin folder and returned for the UI.
func (m *Manager) runAfterJobPlugin(ctx context.Context, jobID, pluginID string) afterJobResult {
	p, err := plugins.Get(pluginID)
	if err != nil {
		return afterJobResult{err: err}
	}
	if !p.HasAfterJob {
		return afterJobResult{err: fmt.Errorf("plugin %q has no after-job script", pluginID)}
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd, err := p.Command(runCtx, modules.FindPython(m.config.Crawler.PythonPath), plugins.HookAfterJob, jobID, "--job-id", jobID)
	if err != nil {
		return afterJobResult{err: err}
	}
	var buf bytes.Buffer
	var w io.Writer = &buf
	if logFile, err := os.OpenFile(filepath.Join(p.Dir, "after_job.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		defer logFile.Close()
		fmt.Fprintf(logFile, "\n=== %s job %s (worker %s)\n", time.Now().Format(time.RFC3339), jobID, m.workerID)
		w = io.MultiWriter(&buf, logFile)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	log.Printf("[AfterJob] Running plugin %s for job %s", pluginID, jobID)
	if err := cmd.Run(); err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after 30m")
		}
		return afterJobResult{output: buf.String(), err: err}
	}
	return afterJobResult{output: buf.String()}
}

// RunAfterJobNow schedules a job's after-job plugin to run at once (again).
// pluginID "" uses the plugin recorded for the job, then the job's settings,
// then the engine default.
func (m *Manager) RunAfterJobNow(jobID, pluginID string) error {
	job, err := m.GetJob(jobID)
	if err != nil {
		return notFound(err)
	}
	if !job.Status.IsTerminal() && job.Status != models.JobStatusPaused {
		return invalidState("the job is %s; after-job plugins run on finished jobs", job.Status)
	}
	if job.AfterJobState == "running" || job.AfterJobState == "scheduled" {
		return invalidState("the after-job plugin is already %s", job.AfterJobState)
	}
	if pluginID == "" {
		pluginID = m.EffectiveAfterJobPlugin(job)
	}
	if pluginID == "" {
		return invalidInput("no after-job plugin is configured for this job")
	}
	p, err := plugins.Get(pluginID)
	if err != nil {
		return invalidInput("%v", err)
	}
	if !p.HasAfterJob {
		return invalidInput("plugin %q has no after-job script", pluginID)
	}
	now := time.Now()
	worker := job.AfterJobWorker
	if worker == "" {
		worker = job.WorkerID
	}
	if err := m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Updates(map[string]interface{}{
		"after_job_plugin": pluginID, "after_job_due_at": now, "after_job_state": "scheduled", "after_job_worker": worker,
	}).Error; err != nil {
		return err
	}
	m.nudge()
	return nil
}

// RunAfterJobInline runs a job's after-job plugin in this process and waits
// (used by the CLI).
func (m *Manager) RunAfterJobInline(ctx context.Context, jobID, pluginID string) (string, error) {
	job, err := m.GetJob(jobID)
	if err != nil {
		return "", notFound(err)
	}
	if pluginID == "" {
		pluginID = m.EffectiveAfterJobPlugin(job)
	}
	if pluginID == "" {
		return "", invalidInput("no after-job plugin is configured for this job; pass --plugin")
	}
	res := m.db.Model(&models.CrawlJob{}).Where("id = ? AND after_job_state <> ?", jobID, "running").
		Updates(map[string]interface{}{"after_job_state": "running", "after_job_plugin": pluginID, "after_job_due_at": time.Now()})
	if res.RowsAffected == 0 {
		return "", invalidState("the after-job plugin is already running for this job")
	}
	out := m.runAfterJobPlugin(ctx, jobID, pluginID)
	m.finishAfterJob(jobID, pluginID, out)
	return out.output, out.err
}

// EffectiveAfterCrawlPlugin is the after-crawl plugin a job uses.
func (m *Manager) EffectiveAfterCrawlPlugin(job *models.CrawlJob) string {
	if job.Settings != nil && job.Settings.AfterCrawlPlugin != nil {
		return strings.TrimSpace(*job.Settings.AfterCrawlPlugin)
	}
	return m.config.Crawler.AfterCrawlPlugin
}

// EffectiveAfterJobPlugin is the after-job plugin of a job: the one already
// recorded for it, else its settings, else the engine default.
func (m *Manager) EffectiveAfterJobPlugin(job *models.CrawlJob) string {
	if job.AfterJobPlugin != "" {
		return job.AfterJobPlugin
	}
	if job.Settings != nil && job.Settings.AfterJobPlugin != nil {
		return strings.TrimSpace(*job.Settings.AfterJobPlugin)
	}
	return m.config.Crawler.AfterJobPlugin
}

func (m *Manager) heartbeat() {
	host, _ := os.Hostname()
	hb := models.WorkerHeartbeat{
		ID:          m.workerID,
		Hostname:    host,
		Region:      m.config.Worker.Region,
		Version:     Version,
		MaxJobs:     m.config.Worker.MaxRunningJobs,
		RunningJobs: m.engine.RunningCount(),
		Browser:     m.browser.Load(),
		StartedAt:   m.startedAt,
		LastSeenAt:  time.Now(),
	}
	if err := m.db.Save(&hb).Error; err != nil {
		log.Printf("[Scheduler] Heartbeat failed: %v", err)
	}
}

func (m *Manager) cleanup() {
	m.db.Where("created_at < ?", time.Now().Add(-24*time.Hour)).Delete(&models.IdempotencyKey{})
	m.db.Where("last_seen_at < ?", time.Now().Add(-24*time.Hour)).Delete(&models.WorkerHeartbeat{})
	m.db.Where("status = ? AND delivered_at < ?", models.DeliveryDelivered, time.Now().Add(-30*24*time.Hour)).Delete(&models.WebhookDelivery{})
	m.cleanupScrapeCache()
}

// Workers lists workers seen in the last two minutes with their operator-set
// state and whether they run a different version than this process.
func (m *Manager) Workers() []models.WorkerHeartbeat {
	var out []models.WorkerHeartbeat
	m.db.Where("last_seen_at > ?", time.Now().Add(-2*time.Minute)).Order("id").Find(&out)
	if len(out) == 0 {
		return out
	}
	var controls []models.WorkerControl
	m.db.Find(&controls)
	states := make(map[string]string, len(controls))
	for _, c := range controls {
		states[c.WorkerID] = c.State
	}
	for i := range out {
		out[i].State = models.WorkerActive
		if s := states[out[i].ID]; s != "" {
			out[i].State = s
		}
		out[i].VersionMismatch = out[i].Version != Version
	}
	return out
}

// RunNow claims a queued job for this process and starts it immediately,
// without running the scheduler (used by "mole crawl").
func (m *Manager) RunNow(jobID string) error {
	if !m.claimAndStart(jobID) {
		job, err := m.GetJob(jobID)
		if err != nil {
			return notFound(err)
		}
		if job.Status == models.JobStatusFailed {
			return fmt.Errorf("job failed to start: %s", job.ErrorMessage)
		}
		return invalidState("job is %s, not queued", job.Status)
	}
	return nil
}

// StartDeliveries runs only the webhook delivery loop (for CLI crawls).
func (m *Manager) StartDeliveries() {
	if m.workerOn.Swap(true) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.stopLoops = cancel
	m.loops.Add(1)
	go m.deliveryLoop(ctx)
}
