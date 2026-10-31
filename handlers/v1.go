package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/crawler"
	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
)

// The /v1 API is the contract external control planes (e.g. a Laravel app)
// build on. Successful responses are {"data": ...}; list responses add
// "next_cursor"; errors are {"error": {"code": ..., "message": ...}}.
// The same handlers are mounted at /api/v1 for the built-in UI.

const ctxAllowPlugins = "mole.allowPlugins"

// AllowPlugins marks requests that may select Python plugins.
func AllowPlugins(allow bool) gin.HandlerFunc {
	return func(c *gin.Context) { c.Set(ctxAllowPlugins, allow); c.Next() }
}

func abortV1(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": msg}})
}

func v1Error(c *gin.Context, err error) {
	status := statusFor(err)
	code := map[int]string{
		http.StatusNotFound:            "not_found",
		http.StatusConflict:            "conflict",
		http.StatusBadRequest:          "invalid_request",
		http.StatusUnprocessableEntity: "idempotency_conflict",
		http.StatusForbidden:           "forbidden",
	}[status]
	if code == "" {
		code = "internal_error"
	}
	abortV1(c, status, code, err.Error())
}

func ok(c *gin.Context, status int, data interface{}) {
	c.JSON(status, gin.H{"data": data})
}

func bindJSON(c *gin.Context, dst interface{}) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 4<<20))
	if err != nil {
		abortV1(c, http.StatusBadRequest, "invalid_request", "cannot read body")
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		abortV1(c, http.StatusBadRequest, "invalid_json", err.Error())
		return nil, false
	}
	var generic interface{}
	if json.Unmarshal(body, &generic) == nil {
		if err := checkSettingsObjects(generic); err != nil {
			abortV1(c, http.StatusBadRequest, "invalid_json", err.Error())
			return nil, false
		}
	}
	return body, true
}

// checkSettingsObjects strictly validates every "settings" object in a request
// (JobSettings has a lenient custom unmarshaller for stored data).
func checkSettingsObjects(v interface{}) error {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, sub := range t {
			if k == "settings" {
				if obj, ok := sub.(map[string]interface{}); ok {
					raw, _ := json.Marshal(obj)
					if err := models.CheckSettingsJSON(raw); err != nil {
						return err
					}
					continue
				}
			}
			if err := checkSettingsObjects(sub); err != nil {
				return err
			}
		}
	case []interface{}:
		for _, sub := range t {
			if err := checkSettingsObjects(sub); err != nil {
				return err
			}
		}
	}
	return nil
}

func queryInt(c *gin.Context, key string, def int) int {
	if v, err := strconv.Atoi(c.Query(key)); err == nil {
		return v
	}
	return def
}

func queryUint(c *gin.Context, key string) uint {
	v, _ := strconv.ParseUint(c.Query(key), 10, 64)
	return uint(v)
}

func queryBool(c *gin.Context, key string) bool {
	v := strings.ToLower(c.Query(key))
	return v == "1" || v == "true" || v == "yes"
}

// loadJob loads the :id job, enforcing X-Owner-Ref when the caller sends it.
func (h *Handler) loadJob(c *gin.Context) (*models.CrawlJob, bool) {
	job, err := h.jobManager.GetJob(c.Param("id"))
	if err != nil || (c.GetHeader("X-Owner-Ref") != "" && c.GetHeader("X-Owner-Ref") != job.OwnerRef) {
		abortV1(c, http.StatusNotFound, "not_found", "crawl not found")
		return nil, false
	}
	return job, true
}

// RegisterV1 mounts the versioned API on group.
func (h *Handler) RegisterV1(g *gin.RouterGroup) {
	g.GET("/info", h.V1Info)
	g.GET("/workers", h.V1Workers)

	g.POST("/crawls", h.V1CreateCrawl)
	g.GET("/crawls", h.V1ListCrawls)
	g.GET("/crawls/:id", h.V1GetCrawl)
	g.PATCH("/crawls/:id", h.V1UpdateCrawl)
	g.DELETE("/crawls/:id", h.V1DeleteCrawl)
	g.POST("/crawls/:id/start", h.v1Action(func(id string) error { return h.jobManager.StartJob(id) }))
	g.POST("/crawls/:id/pause", h.v1Action(func(id string) error { return h.jobManager.PauseJob(id) }))
	g.POST("/crawls/:id/resume", h.v1Action(func(id string) error { return h.jobManager.ResumeJob(id) }))
	g.POST("/crawls/:id/cancel", h.v1Action(func(id string) error { return h.jobManager.CancelJob(id) }))
	g.POST("/crawls/:id/duplicate", h.V1DuplicateCrawl)
	g.POST("/crawls/:id/after-job", h.V1RunAfterJob)
	g.GET("/crawls/:id/pages", h.V1ListPages)
	g.GET("/crawls/:id/tags", h.V1TagCounts)
	g.PUT("/crawls/:id/tag-rules", h.V1SetTagRules)
	g.GET("/crawls/:id/pages/:pageId", h.V1GetPage)
	g.GET("/crawls/:id/pages/:pageId/screenshot", h.V1Screenshot)
	g.GET("/crawls/:id/changes", h.V1ListChanges)
	g.GET("/crawls/:id/export", h.V1Export)
	g.GET("/crawls/:id/webhooks", h.V1Deliveries)
	g.POST("/webhooks/:deliveryId/redeliver", h.V1Redeliver)

	g.POST("/batch/scrape", h.V1CreateBatch)
	g.POST("/scrape", h.V1Scrape)
	g.POST("/map", h.V1Map)
	g.POST("/search", h.V1Search)

	g.POST("/schedules", h.V1CreateSchedule)
	g.GET("/schedules", h.V1ListSchedules)
	g.GET("/schedules/:id", h.V1GetSchedule)
	g.PUT("/schedules/:id", h.V1UpdateSchedule)
	g.DELETE("/schedules/:id", h.V1DeleteSchedule)
	g.POST("/schedules/:id/run", h.V1RunSchedule)
	g.GET("/cron/preview", h.V1CronPreview)
	g.POST("/schedules/:id/enable", h.v1ScheduleEnabled(true))
	g.POST("/schedules/:id/disable", h.v1ScheduleEnabled(false))

	// Grouping within a crawl or a schedule — which is how several sites
	// are compared: give the crawl several start URLs — and the actions
	// that work in either scope.
	g.GET("/crawls/:id/groups", h.V1CrawlGrouping)
	g.POST("/crawls/:id/regroup", h.V1RegroupCrawl)
	g.GET("/schedules/:id/groups", h.V1ScheduleGrouping)
	g.POST("/schedules/:id/regroup", h.V1RegroupSchedule)
	g.GET("/grouping/fields", h.V1GroupingFields)
	g.POST("/grouping/product-groups", h.V1CreateProductGroup)
	g.POST("/grouping/assign", h.V1AssignUnmatched)
	g.POST("/grouping/members/:memberId/pin", h.v1MemberAction(h.jobManager.PinGroupMember, gin.H{"pinned": true}))
	g.POST("/grouping/members/:memberId/unpin", h.v1MemberAction(h.jobManager.UnpinGroupMember, gin.H{"pinned": false}))
	g.POST("/grouping/members/:memberId/exclude", h.v1MemberAction(h.jobManager.ExcludeGroupMember, gin.H{"excluded": true}))
	g.POST("/grouping/members/:memberId/unexclude", h.v1MemberAction(h.jobManager.UnexcludeGroupMember, gin.H{"excluded": false}))
	g.POST("/grouping/members/:memberId/move", h.V1MoveGroupMember)

	g.GET("/owners/:ref/usage", h.V1OwnerUsage)
	g.GET("/owners/:ref/limits", h.V1GetOwnerLimits)
	g.PUT("/owners/:ref/limits", h.V1SetOwnerLimits)
	g.DELETE("/owners/:ref/limits", h.V1DeleteOwnerLimits)
	g.DELETE("/owners/:ref", h.V1DeleteOwner)
}

// V1Health is an unauthenticated liveness/readiness probe.
func (h *Handler) V1Health(c *gin.Context) {
	sqlDB, err := h.jobManager.DB().DB()
	if err == nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		err = sqlDB.PingContext(ctx)
		cancel()
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "database": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "worker_id": h.jobManager.WorkerID(), "version": jobs.Version})
}

// V1Info describes the engine's capabilities.
func (h *Handler) V1Info(c *gin.Context) { ok(c, http.StatusOK, h.catalog()) }

// V1Workers lists live workers and this process's runs.
func (h *Handler) V1Workers(c *gin.Context) {
	ok(c, http.StatusOK, gin.H{"workers": h.jobManager.Workers(), "local": h.jobManager.GetEngineStats()})
}

// crawlView is the full job representation returned by the API.
type crawlView struct {
	jobs.JobSummary
	Domain     string                `json:"domain"`
	SeedCount  int                   `json:"seed_count"`
	MaxDepth   int                   `json:"max_depth"`
	Priority   int                   `json:"priority"`
	Region     string                `json:"region,omitempty"`
	QueuedAt   *time.Time            `json:"queued_at,omitempty"`
	WorkerID   string                `json:"worker_id,omitempty"`
	Settings   *models.JobSettings   `json:"settings"`
	Limits     *models.JobLimits     `json:"limits,omitempty"`
	Webhook    *models.WebhookConfig `json:"webhook,omitempty"`
	Live       *crawler.RunStats     `json:"live,omitempty"`
	Plugins    *pluginView           `json:"plugins,omitempty"`
	Changes    map[string]int64      `json:"changes,omitempty"`
	AfterJob   string                `json:"after_job_state,omitempty"`
	ControlReq string                `json:"pending_control,omitempty"`
}

func (h *Handler) view(j *models.CrawlJob, detailed bool) crawlView {
	v := crawlView{
		JobSummary: jobs.Summarize(j), Domain: j.Domain, SeedCount: len(j.SeedURLs), MaxDepth: j.MaxDepth,
		Priority: j.Priority, Region: j.Region, QueuedAt: j.QueuedAt, WorkerID: j.WorkerID,
		Settings: j.Settings, Limits: j.Limits, AfterJob: j.AfterJobState, ControlReq: j.Control,
	}
	if j.Webhook != nil {
		w := *j.Webhook
		if w.Secret != "" {
			w.Secret = secretMask
		}
		v.Webhook = &w
	}
	if detailed {
		if pv := h.pluginStatus(j); pv.AfterCrawl != "" || pv.AfterJob != "" {
			v.Plugins = &pv
		}
		if st, ok := h.jobManager.Engine().RunStatsFor(j.ID); ok {
			v.Live = &st
		}
		if j.ScheduleID != nil || (j.Settings != nil && j.Settings.ChangeDetection != nil && *j.Settings.ChangeDetection) {
			v.Changes = h.jobManager.ChangeCounts(j.ID)
		}
	}
	return v
}

// pluginView reports which plugins a job uses and how their runs went.
type pluginView struct {
	AfterCrawl         string     `json:"after_crawl,omitempty"`
	AfterCrawlRuns     int64      `json:"after_crawl_runs"`
	AfterCrawlFailures int64      `json:"after_crawl_failures"`
	AfterCrawlError    string     `json:"after_crawl_error,omitempty"`
	AfterJob           string     `json:"after_job,omitempty"`
	AfterJobState      string     `json:"after_job_state,omitempty"`
	AfterJobDueAt      *time.Time `json:"after_job_due_at,omitempty"`
	AfterJobFinishedAt *time.Time `json:"after_job_finished_at,omitempty"`
	AfterJobOutput     string     `json:"after_job_output,omitempty"`
}

func (h *Handler) pluginStatus(j *models.CrawlJob) pluginView {
	return pluginView{
		AfterCrawl:         h.jobManager.EffectiveAfterCrawlPlugin(j),
		AfterCrawlRuns:     j.AfterCrawlRuns,
		AfterCrawlFailures: j.AfterCrawlFailures,
		AfterCrawlError:    j.AfterCrawlError,
		AfterJob:           h.jobManager.EffectiveAfterJobPlugin(j),
		AfterJobState:      j.AfterJobState,
		AfterJobDueAt:      j.AfterJobDueAt,
		AfterJobFinishedAt: j.AfterJobFinishedAt,
		AfterJobOutput:     j.AfterJobOutput,
	}
}

// V1CreateCrawl creates (and by default queues) a crawl. Send an
// Idempotency-Key header to make retries safe.
func (h *Handler) V1CreateCrawl(c *gin.Context) {
	var spec jobs.JobSpec
	body, okb := bindJSON(c, &spec)
	if !okb {
		return
	}
	h.createJob(c, spec, "crawl", body)
}

// batchRequest is the body of POST /v1/batch/scrape.
type batchRequest struct {
	URLs       []string              `json:"urls"`
	OwnerRef   string                `json:"owner_ref,omitempty"`
	ExternalID string                `json:"external_id,omitempty"`
	Priority   int                   `json:"priority,omitempty"`
	Region     string                `json:"region,omitempty"`
	Settings   *models.JobSettings   `json:"settings,omitempty"`
	Limits     *models.JobLimits     `json:"limits,omitempty"`
	Webhook    *models.WebhookConfig `json:"webhook,omitempty"`
	Start      *bool                 `json:"start,omitempty"`
}

// maxBatchRequestURLs caps the URLs of one batch request.
const maxBatchRequestURLs = 10000

// V1CreateBatch creates a batch job that fetches exactly the listed URLs
// (no link following). Results are read like a crawl's: /v1/crawls/{id}/pages.
func (h *Handler) V1CreateBatch(c *gin.Context) {
	var req batchRequest
	body, okb := bindJSON(c, &req)
	if !okb {
		return
	}
	if len(req.URLs) == 0 || len(req.URLs) > maxBatchRequestURLs {
		abortV1(c, http.StatusBadRequest, "invalid_request", fmt.Sprintf("urls must list 1 to %d URLs", maxBatchRequestURLs))
		return
	}
	h.createJob(c, jobs.JobSpec{
		Kind: models.JobKindBatch, URL: req.URLs[0], SeedURLs: req.URLs[1:],
		OwnerRef: req.OwnerRef, ExternalID: req.ExternalID, Priority: req.Priority, Region: req.Region,
		Settings: req.Settings, Limits: req.Limits, Webhook: req.Webhook, Start: req.Start,
	}, "batch", body)
}

// createJob creates a job, replaying the job created earlier for the same
// Idempotency-Key and request body.
func (h *Handler) createJob(c *gin.Context, spec jobs.JobSpec, scope string, body []byte) {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len(key) > 191 {
		abortV1(c, http.StatusBadRequest, "invalid_request", "Idempotency-Key is too long")
		return
	}
	if key != "" {
		existing, err := h.jobManager.ReserveIdempotency(spec.OwnerRef, key, scope, jobs.HashRequest(body))
		if err != nil {
			v1Error(c, err)
			return
		}
		if existing != "" {
			job, err := h.jobManager.GetJob(existing)
			if err != nil {
				v1Error(c, fmt.Errorf("%w: crawl created for this key no longer exists", jobs.ErrNotFound))
				return
			}
			c.Header("Idempotent-Replayed", "true")
			ok(c, http.StatusOK, h.view(job, false))
			return
		}
	}
	job, err := h.jobManager.CreateFromSpec(spec, c.GetBool(ctxAllowPlugins))
	if err != nil {
		if key != "" {
			h.jobManager.ReleaseIdempotency(spec.OwnerRef, key)
		}
		v1Error(c, err)
		return
	}
	if key != "" {
		h.jobManager.CompleteIdempotency(spec.OwnerRef, key, job.ID)
	}
	c.Header("Location", "/v1/crawls/"+job.ID)
	ok(c, http.StatusCreated, h.view(job, false))
}

// V1ListCrawls lists crawls with filters owner_ref, external_id, schedule_id,
// status (comma-separated), cursor and limit.
func (h *Handler) V1ListCrawls(c *gin.Context) {
	owner := c.Query("owner_ref")
	if hdr := c.GetHeader("X-Owner-Ref"); hdr != "" {
		owner = hdr
	}
	list, next, err := h.jobManager.ListJobs(jobs.JobFilter{
		OwnerRef: owner, ExternalID: c.Query("external_id"), ScheduleID: c.Query("schedule_id"),
		Status: c.Query("status"), Cursor: c.Query("cursor"), Limit: queryInt(c, "limit", 50),
	})
	if err != nil {
		v1Error(c, err)
		return
	}
	out := make([]crawlView, len(list))
	for i := range list {
		out[i] = h.view(&list[i], false)
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "next_cursor": next})
}

// V1GetCrawl returns one crawl with live progress.
func (h *Handler) V1GetCrawl(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	ok(c, http.StatusOK, h.view(job, true))
}

// V1UpdateCrawl edits a crawl that is not running.
func (h *Handler) V1UpdateCrawl(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	var req struct {
		jobs.JobOptions
		Settings *models.JobSettings `json:"settings,omitempty"`
	}
	if _, okb := bindJSON(c, &req); !okb {
		return
	}
	if req.Settings != nil {
		if err := h.jobManager.ValidateSettings(req.Settings, job.Limits, c.GetBool(ctxAllowPlugins)); err != nil {
			v1Error(c, err)
			return
		}
		if err := h.jobManager.UpdateJobSettings(job.ID, req.Settings); err != nil {
			v1Error(c, err)
			return
		}
	}
	if err := h.jobManager.UpdateJobOptions(job.ID, req.JobOptions); err != nil {
		v1Error(c, err)
		return
	}
	job, _ = h.jobManager.GetJob(job.ID)
	ok(c, http.StatusOK, h.view(job, true))
}

func (h *Handler) v1Action(fn func(id string) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		job, okj := h.loadJob(c)
		if !okj {
			return
		}
		if err := fn(job.ID); err != nil {
			v1Error(c, err)
			return
		}
		job, _ = h.jobManager.GetJob(job.ID)
		ok(c, http.StatusAccepted, h.view(job, false))
	}
}

// V1DeleteCrawl deletes a crawl and all its data.
func (h *Handler) V1DeleteCrawl(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	if err := h.jobManager.DeleteJob(job.ID); err != nil {
		v1Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// V1DuplicateCrawl copies a crawl's configuration into a new pending crawl.
func (h *Handler) V1DuplicateCrawl(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	dup, err := h.jobManager.DuplicateJob(job.ID)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusCreated, h.view(dup, false))
}

// V1RunAfterJob runs the job's after-job plugin now (again). Plugins execute
// code on the worker, so this needs plugins.allow_in_api for /v1 clients.
func (h *Handler) V1RunAfterJob(c *gin.Context) {
	if !c.GetBool(ctxAllowPlugins) {
		abortV1(c, http.StatusForbidden, "forbidden", "plugins are disabled for API requests (set plugins.allow_in_api)")
		return
	}
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	var req struct {
		Plugin string `json:"plugin"`
	}
	if c.Request.ContentLength > 0 {
		if _, okb := bindJSON(c, &req); !okb {
			return
		}
	}
	if err := h.jobManager.RunAfterJobNow(job.ID, strings.TrimSpace(req.Plugin)); err != nil {
		v1Error(c, err)
		return
	}
	job, _ = h.jobManager.GetJob(job.ID)
	ok(c, http.StatusAccepted, h.view(job, false))
}

// V1ListPages lists pages with an id cursor; content=1 includes derived
// content, body=1 includes stored bodies, successful=1 / failed=1 filter by
// outcome, q=words keeps pages whose title or URL contains every word,
// tag=… keeps pages with that tag, skipped=include|only adds or shows only
// pages skipped by keep_tags, and order=desc lists newest first.
func (h *Handler) V1ListPages(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	pages, next, err := h.jobManager.ListPages(jobs.PageQuery{
		JobID: job.ID, AfterID: queryUint(c, "cursor"), Limit: queryInt(c, "limit", 50),
		WithContent: queryBool(c, "content"), WithBody: queryBool(c, "body"), OnlySuccessful: queryBool(c, "successful"),
		OnlyFailed: queryBool(c, "failed"), NewestFirst: c.Query("order") == "desc",
		Search: c.Query("q"), Tag: c.Query("tag"), Skipped: c.Query("skipped"),
	})
	if err != nil {
		v1Error(c, err)
		return
	}
	cursor := ""
	if next > 0 {
		cursor = strconv.FormatUint(uint64(next), 10)
	}
	c.JSON(http.StatusOK, gin.H{"data": pages, "next_cursor": cursor})
}

// V1TagCounts counts a crawl's pages per tag (kept and skipped).
func (h *Handler) V1TagCounts(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	counts, err := h.jobManager.TagCounts(job.ID)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, counts)
}

// V1SetTagRules replaces a crawl's tag rules and re-tags its pages.
func (h *Handler) V1SetTagRules(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	var req struct {
		TagRules []models.TagRule `json:"tag_rules"`
	}
	if _, okb := bindJSON(c, &req); !okb {
		return
	}
	changed, err := h.jobManager.SetTagRules(job.ID, req.TagRules)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, gin.H{"tag_rules": req.TagRules, "retagged_pages": changed})
}

func pageIDParam(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("pageId"), 10, 64)
	if err != nil || id == 0 {
		abortV1(c, http.StatusBadRequest, "invalid_request", "invalid page id")
		return 0, false
	}
	return uint(id), true
}

// V1GetPage returns one page with its body and derived content.
func (h *Handler) V1GetPage(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	pid, okp := pageIDParam(c)
	if !okp {
		return
	}
	p, err := h.jobManager.GetPageResult(job.ID, pid)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, p)
}

// V1Screenshot serves a page screenshot.
func (h *Handler) V1Screenshot(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	pid, okp := pageIDParam(c)
	if !okp {
		return
	}
	png, err := h.jobManager.GetScreenshot(job.ID, pid)
	if err != nil {
		v1Error(c, err)
		return
	}
	c.Header("Cache-Control", "private, max-age=3600")
	c.Data(http.StatusOK, "image/png", png)
}

// V1ListChanges lists change-detection results (type=new|changed|removed, diff=1).
func (h *Handler) V1ListChanges(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	list, next, err := h.jobManager.ListChanges(job.ID, c.Query("type"), queryUint(c, "cursor"), queryInt(c, "limit", 50), queryBool(c, "diff"))
	if err != nil {
		v1Error(c, err)
		return
	}
	cursor := ""
	if next > 0 {
		cursor = strconv.FormatUint(uint64(next), 10)
	}
	c.JSON(http.StatusOK, gin.H{"data": list, "next_cursor": cursor, "counts": h.jobManager.ChangeCounts(job.ID)})
}

// V1Export streams results as json, jsonl or csv (format=...).
func (h *Handler) V1Export(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	format := c.DefaultQuery("format", jobs.ExportJSONL)
	ctype := map[string]string{
		jobs.ExportJSON:  "application/json; charset=utf-8",
		jobs.ExportJSONL: "application/x-ndjson; charset=utf-8",
		jobs.ExportCSV:   "text/csv; charset=utf-8",
	}[format]
	if ctype == "" {
		abortV1(c, http.StatusBadRequest, "invalid_request", "format must be json, jsonl or csv")
		return
	}
	c.Header("Content-Type", ctype)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="crawl-%s.%s"`, job.ID[:8], format))
	c.Status(http.StatusOK)
	filter := jobs.PageQuery{OnlySuccessful: queryBool(c, "successful"), Search: c.Query("q"), Tag: c.Query("tag")}
	if err := h.jobManager.ExportFiltered(job.ID, format, filter, c.Writer); err != nil {
		// Headers are already sent; report the problem in the stream.
		fmt.Fprintf(c.Writer, "\nexport error: %v\n", err)
	}
}

// V1Deliveries lists webhook deliveries of a crawl.
func (h *Handler) V1Deliveries(c *gin.Context) {
	job, okj := h.loadJob(c)
	if !okj {
		return
	}
	list, err := h.jobManager.GetDeliveries(job.ID, queryInt(c, "limit", 50))
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, list)
}

// V1Redeliver queues a webhook delivery again.
func (h *Handler) V1Redeliver(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("deliveryId"), 10, 64)
	if err != nil {
		abortV1(c, http.StatusBadRequest, "invalid_request", "invalid delivery id")
		return
	}
	if err := h.jobManager.RedeliverWebhook(uint(id)); err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusAccepted, gin.H{"id": id, "status": models.DeliveryPending})
}

func requestTimeout(c *gin.Context, def, max time.Duration) time.Duration {
	if s := queryInt(c, "timeout", 0); s > 0 {
		d := time.Duration(s) * time.Second
		if d > max {
			return max
		}
		return d
	}
	return def
}

// V1Scrape fetches one URL synchronously. With max_age it may answer from
// the scrape cache; X-Owner-Ref scopes cache entries of private requests.
func (h *Handler) V1Scrape(c *gin.Context) {
	var req jobs.ScrapeRequest
	if _, okb := bindJSON(c, &req); !okb {
		return
	}
	if err := h.jobManager.ValidateSettings(req.Settings, nil, false); err != nil {
		v1Error(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), requestTimeout(c, 2*time.Minute, 5*time.Minute))
	defer cancel()
	res, err := h.jobManager.Scrape(ctx, req, c.GetHeader("X-Owner-Ref"))
	if err != nil {
		if !errors.Is(err, jobs.ErrInvalidInput) {
			err = fmt.Errorf("%w: %v", jobs.ErrInvalidInput, err)
		}
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, res)
}

// V1Search runs a web search and optionally scrapes the results.
func (h *Handler) V1Search(c *gin.Context) {
	var req jobs.SearchRequest
	if _, okb := bindJSON(c, &req); !okb {
		return
	}
	if err := h.jobManager.ValidateSettings(req.Settings, nil, false); err != nil {
		v1Error(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), requestTimeout(c, 2*time.Minute, 5*time.Minute))
	defer cancel()
	res, err := h.jobManager.Search(ctx, req, c.GetHeader("X-Owner-Ref"))
	if err != nil {
		if errors.Is(err, jobs.ErrInvalidInput) {
			v1Error(c, err)
			return
		}
		abortV1(c, http.StatusBadGateway, "search_failed", err.Error())
		return
	}
	ok(c, http.StatusOK, res)
}

// V1Map lists a site's URLs from sitemaps and links.
func (h *Handler) V1Map(c *gin.Context) {
	var req crawler.MapRequest
	if _, okb := bindJSON(c, &req); !okb {
		return
	}
	if err := h.jobManager.ValidateSettings(req.Settings, nil, false); err != nil {
		v1Error(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), requestTimeout(c, 2*time.Minute, 10*time.Minute))
	defer cancel()
	res, err := h.jobManager.Engine().Map(ctx, req)
	if err != nil {
		v1Error(c, fmt.Errorf("%w: %v", jobs.ErrInvalidInput, err))
		return
	}
	ok(c, http.StatusOK, res)
}

func (h *Handler) loadSchedule(c *gin.Context) (*models.Schedule, bool) {
	s, err := h.jobManager.GetSchedule(c.Param("id"))
	if err != nil || (c.GetHeader("X-Owner-Ref") != "" && c.GetHeader("X-Owner-Ref") != s.OwnerRef) {
		abortV1(c, http.StatusNotFound, "not_found", "schedule not found")
		return nil, false
	}
	return s, true
}

// V1CreateSchedule creates a cron schedule.
func (h *Handler) V1CreateSchedule(c *gin.Context) {
	var in jobs.ScheduleInput
	if _, okb := bindJSON(c, &in); !okb {
		return
	}
	s, err := h.jobManager.CreateSchedule(in, c.GetBool(ctxAllowPlugins))
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusCreated, jobs.MaskSchedule(s))
}

// V1ListSchedules lists schedules (owner_ref filter, limit/offset).
func (h *Handler) V1ListSchedules(c *gin.Context) {
	owner := c.Query("owner_ref")
	if hdr := c.GetHeader("X-Owner-Ref"); hdr != "" {
		owner = hdr
	}
	list, total, err := h.jobManager.ListSchedules(owner, clampInt(queryInt(c, "limit", 50), 1, 200), queryInt(c, "offset", 0))
	if err != nil {
		v1Error(c, err)
		return
	}
	out := make([]*models.Schedule, len(list))
	for i := range list {
		out[i] = jobs.MaskSchedule(&list[i])
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": total})
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// V1GetSchedule returns a schedule and its recent runs.
func (h *Handler) V1GetSchedule(c *gin.Context) {
	s, oks := h.loadSchedule(c)
	if !oks {
		return
	}
	runs, _ := h.jobManager.GetScheduleJobs(s.ID, 20)
	views := make([]crawlView, len(runs))
	for i := range runs {
		views[i] = h.view(&runs[i], false)
	}
	ok(c, http.StatusOK, gin.H{"schedule": jobs.MaskSchedule(s), "recent_crawls": views})
}

// V1UpdateSchedule replaces a schedule.
func (h *Handler) V1UpdateSchedule(c *gin.Context) {
	s, oks := h.loadSchedule(c)
	if !oks {
		return
	}
	var in jobs.ScheduleInput
	if _, okb := bindJSON(c, &in); !okb {
		return
	}
	updated, err := h.jobManager.UpdateSchedule(s.ID, in, c.GetBool(ctxAllowPlugins))
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, jobs.MaskSchedule(updated))
}

// V1DeleteSchedule deletes a schedule (its crawls are kept).
func (h *Handler) V1DeleteSchedule(c *gin.Context) {
	s, oks := h.loadSchedule(c)
	if !oks {
		return
	}
	if err := h.jobManager.DeleteSchedule(s.ID); err != nil {
		v1Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// V1RunSchedule starts a run of a schedule now.
func (h *Handler) V1RunSchedule(c *gin.Context) {
	s, oks := h.loadSchedule(c)
	if !oks {
		return
	}
	job, err := h.jobManager.RunSchedule(s)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusCreated, h.view(job, false))
}

func (h *Handler) v1ScheduleEnabled(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		s, oks := h.loadSchedule(c)
		if !oks {
			return
		}
		if err := h.jobManager.SetScheduleEnabled(s.ID, enabled); err != nil {
			v1Error(c, err)
			return
		}
		s, _ = h.jobManager.GetSchedule(s.ID)
		ok(c, http.StatusOK, jobs.MaskSchedule(s))
	}
}

// V1OwnerUsage sums an owner's usage between from and to (RFC3339 or YYYY-MM-DD; default: this month).
func (h *Handler) V1OwnerUsage(c *gin.Context) {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	to := now.Add(time.Second)
	for key, dst := range map[string]*time.Time{"from": &from, "to": &to} {
		v := c.Query(key)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			t, err = time.ParseInLocation("2006-01-02", v, now.Location())
		}
		if err != nil {
			abortV1(c, http.StatusBadRequest, "invalid_request", key+" must be RFC3339 or YYYY-MM-DD")
			return
		}
		*dst = t
	}
	u, err := h.jobManager.GetOwnerUsage(c.Param("ref"), from, to)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, u)
}

// V1GetOwnerLimits returns an owner's concurrency limit.
func (h *Handler) V1GetOwnerLimits(c *gin.Context) {
	l, err := h.jobManager.GetOwnerLimits(c.Param("ref"))
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, l)
}

// V1SetOwnerLimits sets an owner's concurrency limit: {"max_running": n}.
func (h *Handler) V1SetOwnerLimits(c *gin.Context) {
	var req struct {
		MaxRunning *int `json:"max_running"`
	}
	if _, okb := bindJSON(c, &req); !okb {
		return
	}
	if req.MaxRunning == nil {
		abortV1(c, http.StatusBadRequest, "invalid_request", "max_running is required (0 = unlimited)")
		return
	}
	l, err := h.jobManager.SetOwnerLimits(c.Param("ref"), *req.MaxRunning)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, l)
}

// V1DeleteOwnerLimits removes an owner's limit; the engine default applies again.
func (h *Handler) V1DeleteOwnerLimits(c *gin.Context) {
	if err := h.jobManager.DeleteOwnerLimits(c.Param("ref")); err != nil {
		v1Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// V1DeleteOwner deletes all data of an owner.
func (h *Handler) V1DeleteOwner(c *gin.Context) {
	res, err := h.jobManager.DeleteOwner(c.Param("ref"))
	if err != nil {
		if res != nil {
			c.JSON(statusFor(err), gin.H{"data": res, "error": gin.H{"code": "partial_failure", "message": err.Error()}})
			return
		}
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, res)
}

// V1CronPreview checks a cron expression and lists when it fires next:
// GET /v1/cron/preview?cron=0 3 * * *&timezone=Asia/Tehran&count=5.
func (h *Handler) V1CronPreview(c *gin.Context) {
	expr, tz := strings.TrimSpace(c.Query("cron")), strings.TrimSpace(c.Query("timezone"))
	if expr == "" {
		abortV1(c, http.StatusBadRequest, "invalid_request", "cron is required")
		return
	}
	if tz == "" {
		tz = "Asia/Tehran"
	}
	n, _ := strconv.Atoi(c.Query("count"))
	if n < 1 || n > 20 {
		n = 5
	}
	next, err := jobs.NextRuns(expr, tz, time.Now(), n)
	if err != nil {
		abortV1(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ok(c, http.StatusOK, gin.H{"cron": expr, "timezone": tz, "description": jobs.DescribeCron(expr), "next": next})
}
