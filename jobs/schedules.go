package jobs

import (
	"fmt"
	"strings"
	"time"

	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"github.com/robfig/cron/v3"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// NextRun returns the next time expr fires after from, evaluated in timezone.
// expr is a 5-field cron expression or a descriptor like "@daily" / "@every 6h".
func NextRun(expr, timezone string, from time.Time) (time.Time, error) {
	sched, err := cronParser.Parse(strings.TrimSpace(expr))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression %q: %v", expr, err)
	}
	loc := time.UTC
	if timezone != "" {
		if loc, err = time.LoadLocation(timezone); err != nil {
			return time.Time{}, fmt.Errorf("invalid timezone %q", timezone)
		}
	}
	next := sched.Next(from.In(loc))
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression %q never fires", expr)
	}
	return next, nil
}

// ScheduleInput creates or updates a schedule.
type ScheduleInput struct {
	Name            string             `json:"name"`
	OwnerRef        string             `json:"owner_ref,omitempty"`
	ExternalID      string             `json:"external_id,omitempty"`
	Cron            string             `json:"cron"`
	Timezone        string             `json:"timezone,omitempty"`
	Enabled         *bool              `json:"enabled,omitempty"`
	ChangeDetection *bool              `json:"change_detection,omitempty"`
	SkipIfRunning   *bool              `json:"skip_if_running,omitempty"`
	Template        models.JobTemplate `json:"template"`
	// SourceJobID names the crawl a new schedule was made from: a webhook
	// secret sent masked ("********") is then copied from that crawl.
	SourceJobID string `json:"source_job_id,omitempty"`
}

func (m *Manager) validateSchedule(in *ScheduleInput, allowPlugins bool) (time.Time, error) {
	if strings.TrimSpace(in.Cron) == "" {
		return time.Time{}, invalidInput("cron is required")
	}
	if in.Timezone == "" {
		in.Timezone = "Asia/Tehran"
	}
	next, err := NextRun(in.Cron, in.Timezone, time.Now())
	if err != nil {
		return time.Time{}, invalidInput("%v", err)
	}
	kind, err := normalizeKind(in.Template.Kind)
	if err != nil {
		return time.Time{}, err
	}
	in.Template.Kind = kind
	target := in.Template.TargetURL
	if kind == models.JobKindBatch {
		if target, err = batchURL(target); err != nil {
			return time.Time{}, err
		}
	}
	if _, _, err := ParseTarget(target); err != nil {
		return time.Time{}, err
	}
	if _, err := ExpandTemplateURL(in.Template.TargetURL, in.Template.TemplateVars); err != nil {
		return time.Time{}, err
	}
	if err := m.ValidateSettings(in.Template.Settings, in.Template.Limits, allowPlugins); err != nil {
		return time.Time{}, err
	}
	if err := validateWebhook(in.Template.Webhook); err != nil {
		return time.Time{}, err
	}
	if in.Name == "" {
		in.Name = in.Template.TargetURL
	}
	return next, nil
}

// CreateSchedule validates and stores a schedule.
func (m *Manager) CreateSchedule(in ScheduleInput, allowPlugins bool) (*models.Schedule, error) {
	if w := in.Template.Webhook; w != nil && w.Secret == SecretMask {
		w.Secret = ""
		if in.SourceJobID != "" {
			if src, err := m.GetJob(in.SourceJobID); err == nil && src.Webhook != nil {
				w.Secret = src.Webhook.Secret
			}
		}
	}
	next, err := m.validateSchedule(&in, allowPlugins)
	if err != nil {
		return nil, err
	}
	s := &models.Schedule{
		Name:            in.Name,
		OwnerRef:        in.OwnerRef,
		ExternalID:      in.ExternalID,
		Cron:            strings.TrimSpace(in.Cron),
		Timezone:        in.Timezone,
		Enabled:         in.Enabled == nil || *in.Enabled,
		ChangeDetection: in.ChangeDetection == nil || *in.ChangeDetection,
		SkipIfRunning:   in.SkipIfRunning == nil || *in.SkipIfRunning,
		Template:        in.Template,
		NextRunAt:       &next,
	}
	if err := m.db.Create(s).Error; err != nil {
		return nil, err
	}
	// GORM skips zero values that have column defaults; write the booleans explicitly.
	m.db.Model(s).Updates(map[string]interface{}{"enabled": s.Enabled, "change_detection": s.ChangeDetection, "skip_if_running": s.SkipIfRunning})
	m.nudge()
	return s, nil
}

// UpdateSchedule replaces a schedule's definition.
func (m *Manager) UpdateSchedule(id string, in ScheduleInput, allowPlugins bool) (*models.Schedule, error) {
	s, err := m.GetSchedule(id)
	if err != nil {
		return nil, err
	}
	if w := in.Template.Webhook; w != nil && w.Secret == SecretMask {
		w.Secret = ""
		if s.Template.Webhook != nil {
			w.Secret = s.Template.Webhook.Secret
		}
	}
	next, err := m.validateSchedule(&in, allowPlugins)
	if err != nil {
		return nil, err
	}
	s.Name, s.Cron, s.Timezone, s.Template = in.Name, strings.TrimSpace(in.Cron), in.Timezone, in.Template
	if in.ExternalID != "" {
		s.ExternalID = in.ExternalID
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	if in.ChangeDetection != nil {
		s.ChangeDetection = *in.ChangeDetection
	}
	if in.SkipIfRunning != nil {
		s.SkipIfRunning = *in.SkipIfRunning
	}
	s.NextRunAt = &next
	err = m.db.Model(&models.Schedule{}).Where("id = ?", id).Updates(map[string]interface{}{
		"name": s.Name, "cron": s.Cron, "timezone": s.Timezone, "template": s.Template, "external_id": s.ExternalID,
		"enabled": s.Enabled, "change_detection": s.ChangeDetection, "skip_if_running": s.SkipIfRunning, "next_run_at": next,
	}).Error
	return s, err
}

// SetScheduleEnabled pauses or resumes a schedule.
func (m *Manager) SetScheduleEnabled(id string, enabled bool) error {
	s, err := m.GetSchedule(id)
	if err != nil {
		return err
	}
	updates := map[string]interface{}{"enabled": enabled}
	if enabled {
		next, err := NextRun(s.Cron, s.Timezone, time.Now())
		if err != nil {
			return invalidInput("%v", err)
		}
		updates["next_run_at"] = next
	}
	return m.db.Model(&models.Schedule{}).Where("id = ?", id).Updates(updates).Error
}

// GetSchedule loads a schedule.
func (m *Manager) GetSchedule(id string) (*models.Schedule, error) {
	var s models.Schedule
	if err := m.db.First(&s, "id = ?", id).Error; err != nil {
		return nil, notFound(err)
	}
	return &s, nil
}

// ListSchedules lists schedules, optionally for one owner.
func (m *Manager) ListSchedules(ownerRef string, limit, offset int) ([]models.Schedule, int64, error) {
	q := m.db.Model(&models.Schedule{})
	if ownerRef != "" {
		q = q.Where("owner_ref = ?", ownerRef)
	}
	var total int64
	q.Count(&total)
	var out []models.Schedule
	err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&out).Error
	return out, total, err
}

// DeleteSchedule deletes a schedule; jobs it created are kept.
func (m *Manager) DeleteSchedule(id string) error {
	res := m.db.Delete(&models.Schedule{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w", ErrNotFound)
	}
	return nil
}

// RunSchedule creates (and queues) a job from a schedule now.
func (m *Manager) RunSchedule(s *models.Schedule) (*models.CrawlJob, error) {
	if s.SkipIfRunning {
		var active int64
		m.db.Model(&models.CrawlJob{}).Where("schedule_id = ? AND status IN ?", s.ID,
			[]string{string(models.JobStatusQueued), string(models.JobStatusRunning)}).Count(&active)
		if active > 0 {
			return nil, invalidState("previous run is still active")
		}
	}
	spec := SpecFromTemplate(s.Template)
	spec.OwnerRef = s.OwnerRef
	spec.ScheduleID = s.ID
	if s.ChangeDetection {
		if spec.Settings == nil {
			spec.Settings = &models.JobSettings{}
		}
		t := true
		spec.Settings.ChangeDetection = &t
	}
	job, err := m.CreateFromSpec(spec, true)
	if err != nil {
		return nil, err
	}
	m.db.Model(&models.Schedule{}).Where("id = ?", s.ID).Updates(map[string]interface{}{
		"last_job_id": job.ID, "run_count": s.RunCount + 1,
	})
	return job, nil
}

// ScheduleInputFromJob returns a schedule that repeats job: the same URLs,
// kind, settings, limits, priority, region and webhook (secret masked; send
// SourceJobID to keep it). The link depth the job ran with becomes an
// explicit setting, and job-only settings (a fixed baseline job, change
// detection) are left to the schedule, which compares each run with the
// previous one.
func ScheduleInputFromJob(job *models.CrawlJob) (ScheduleInput, error) {
	var settings *models.JobSettings
	if job.Settings != nil {
		settings = &models.JobSettings{}
		if err := deepCopy(job.Settings, settings); err != nil {
			return ScheduleInput{}, err
		}
	}
	if job.Kind != models.JobKindBatch && (settings == nil || settings.MaxDepth == nil) {
		if settings == nil {
			settings = &models.JobSettings{}
		}
		depth := job.MaxDepth
		settings.MaxDepth = &depth
	}
	changes := true
	if settings != nil {
		if settings.ChangeDetection != nil {
			changes = *settings.ChangeDetection
		}
		settings.ChangeDetection, settings.CompareWithJobID = nil, nil
	}
	// A {{VAR}} target was expanded into the seeds when the job was created.
	target, seeds := job.TargetURL, []string(job.SeedURLs)
	if modules.HasTemplateVars(target) && len(seeds) > 0 {
		target, seeds = seeds[0], seeds[1:]
	}
	if job.Kind == models.JobKindBatch {
		seeds = removeString(seeds, target)
	}
	t := models.JobTemplate{
		Kind:      job.Kind,
		TargetURL: target,
		SeedURLs:  seeds,
		Priority:  job.Priority,
		Region:    job.Region,
		Settings:  settings,
		Limits:    job.Limits,
	}
	if job.Webhook != nil {
		w := *job.Webhook
		if w.Secret != "" {
			w.Secret = SecretMask
		}
		t.Webhook = &w
	}
	enabled, skip := true, true
	return ScheduleInput{
		Name:            job.Domain,
		OwnerRef:        job.OwnerRef,
		Cron:            "0 3 * * *",
		Timezone:        "Asia/Tehran",
		Enabled:         &enabled,
		ChangeDetection: &changes,
		SkipIfRunning:   &skip,
		Template:        t,
		SourceJobID:     job.ID,
	}, nil
}

func removeString(list []string, s string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// MaskSchedule returns a copy of s with its webhook secret masked.
func MaskSchedule(s *models.Schedule) *models.Schedule {
	c := *s
	if w := s.Template.Webhook; w != nil && w.Secret != "" {
		masked := *w
		masked.Secret = SecretMask
		c.Template.Webhook = &masked
	}
	return &c
}

// GetScheduleJobs lists jobs created by a schedule, newest first.
func (m *Manager) GetScheduleJobs(id string, limit int) ([]models.CrawlJob, error) {
	var out []models.CrawlJob
	err := m.db.Where("schedule_id = ?", id).Order("created_at DESC").Limit(limit).Find(&out).Error
	return out, err
}
