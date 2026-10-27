package handlers

import (
	"errors"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/netx"
	"github.com/resolver/crawler/plugins"
)

// secretMask replaces stored secrets in responses; sending it back keeps the secret.
const secretMask = jobs.SecretMask

// statusFor maps manager errors to HTTP status codes.
func statusFor(err error) int {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, jobs.ErrInvalidState), errors.Is(err, jobs.ErrIdempotencyInProgress):
		return http.StatusConflict
	case errors.Is(err, jobs.ErrIdempotencyConflict):
		return http.StatusUnprocessableEntity
	case errors.Is(err, jobs.ErrInvalidInput), errors.Is(err, netx.ErrBlockedAddress):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// jobIDParam reads the job id from the path, query string or JSON body field "job_id".
func jobIDParam(c *gin.Context) string {
	if id := c.Param("id"); id != "" {
		return id
	}
	if id := c.Query("job_id"); id != "" {
		return id
	}
	var body struct {
		JobID string `json:"job_id"`
	}
	_ = c.ShouldBindJSON(&body)
	return body.JobID
}

// Catalog describes what this engine supports; the UI builds its forms from it
// and API clients can discover capabilities.
type Catalog struct {
	Version         string              `json:"version"`
	WorkerID        string              `json:"worker_id"`
	Region          string              `json:"region"`
	ProxyPools      []string            `json:"proxy_pools"`
	ProxyRoutes     []netx.ProxyRoute   `json:"proxy_routes"`
	Regions         []string            `json:"regions"`
	Fingerprints    []string            `json:"fingerprints"`
	Formats         []string            `json:"formats"`
	FieldTypes      []string            `json:"field_types"`
	BrowserActions  []string            `json:"browser_actions"`
	BlockResources  []string            `json:"block_resources"`
	WebhookEvents   []string            `json:"webhook_events"`
	LLMEnabled      bool                `json:"llm_enabled"`
	LLMModel        string              `json:"llm_model,omitempty"`
	StorageDriver   string              `json:"storage_driver"`
	BrowserSlots    int                 `json:"browser_slots"`
	AllowPrivate    bool                `json:"allow_private_networks"`
	PluginsInAPI    bool                `json:"plugins_in_api"`
	Plugins         []plugins.Plugin    `json:"plugins"`
	DefaultSettings *models.JobSettings `json:"default_settings"`
}

func (h *Handler) catalog() Catalog {
	eng := h.jobManager.Engine()
	cfg := h.jobManager.Config()
	regions := map[string]bool{}
	for _, r := range eng.Proxies().Routes() {
		if r.Region != "" {
			regions[r.Region] = true
		}
	}
	if cfg.Worker.Region != "" {
		regions[cfg.Worker.Region] = true
	}
	for _, w := range h.jobManager.Workers() {
		if w.Region != "" {
			regions[w.Region] = true
		}
	}
	regionList := make([]string, 0, len(regions))
	for r := range regions {
		regionList = append(regionList, r)
	}
	sort.Strings(regionList)
	pluginList, _ := plugins.List()
	if pluginList == nil {
		pluginList = []plugins.Plugin{}
	}
	storageDriver := ""
	if eng.Store() != nil {
		storageDriver = eng.Store().Driver()
	}
	return Catalog{
		Version:         jobs.Version,
		WorkerID:        h.jobManager.WorkerID(),
		Region:          cfg.Worker.Region,
		ProxyPools:      nonNil(eng.Proxies().PoolNames()),
		ProxyRoutes:     eng.Proxies().Routes(),
		Regions:         regionList,
		Fingerprints:    netx.Fingerprints,
		Formats:         models.AllFormats,
		FieldTypes:      content.FieldTypes,
		BrowserActions:  []string{"wait", "wait_for_selector", "click", "scroll", "type", "press", "evaluate"},
		BlockResources:  []string{"image", "font", "media", "stylesheet"},
		WebhookEvents:   jobs.AllEvents,
		LLMEnabled:      eng.LLM() != nil,
		LLMModel:        eng.LLM().Model(),
		StorageDriver:   storageDriver,
		BrowserSlots:    eng.BrowserPoolSize(),
		AllowPrivate:    eng.Guard().AllowsPrivate(),
		PluginsInAPI:    cfg.Plugins.AllowInAPI,
		Plugins:         pluginList,
		DefaultSettings: h.jobManager.GetDefaultJobSettings(),
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
