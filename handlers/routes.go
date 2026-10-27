package handlers

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/config"
)

// TemplateFuncs are the helpers available in HTML templates.
var TemplateFuncs = template.FuncMap{
	"divf": func(a, b float64) float64 {
		if b == 0 {
			return 0
		}
		return a / b
	},
	"add":      func(a, b int) int { return a + b },
	"subtract": func(a, b int) int { return a - b },
	"truncate": func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return s[:n]
	},
	"pages": func(current, total int) []int {
		var p []int
		start := current - 2
		if start < 1 {
			start = 1
		}
		end := start + 4
		if end > total {
			end = total
		}
		if end-start < 4 && start > 1 {
			start = end - 4
			if start < 1 {
				start = 1
			}
		}
		for i := start; i <= end; i++ {
			p = append(p, i)
		}
		return p
	},
	"bytes": func(b int64) string { return formatBytes(b) },
	"ms": func(ms int64) string {
		return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
	},
	"pct": func(a, b int) int {
		if b <= 0 {
			return 0
		}
		p := a * 100 / b
		if p > 100 {
			return 100
		}
		return p
	},
	"deref": func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	},
	"ago": func(t *time.Time) string {
		if t == nil || t.IsZero() {
			return "—"
		}
		d := time.Since(*t)
		if d < 0 {
			return "in " + humanDuration(-d)
		}
		return humanDuration(d) + " ago"
	},
	"until": func(t *time.Time) string {
		if t == nil || t.IsZero() {
			return "—"
		}
		d := time.Until(*t)
		if d < 0 {
			return "due"
		}
		return "in " + humanDuration(d)
	},
	"hasPrefix": strings.HasPrefix,
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(time.Second).String()
	case d < time.Hour:
		return d.Round(time.Minute).String()
	case d < 48*time.Hour:
		return d.Round(time.Hour).String()
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Register mounts the web UI, its /api endpoints and the /v1 API.
func Register(router *gin.Engine, h *Handler, cfg *config.Config) {
	router.GET("/healthz", h.V1Health)
	router.GET("/v1/health", h.V1Health)

	v1 := router.Group("/v1", TokenAuth(cfg.API.Tokens), AllowPlugins(cfg.Plugins.AllowInAPI))
	h.RegisterV1(v1)
	if len(cfg.API.Tokens) == 0 {
		log.Printf("[Security] WARNING: api.tokens is empty; /v1 accepts unauthenticated requests. Set tokens before exposing the engine.")
	}

	if !cfg.UI.Enabled {
		router.NoRoute(func(c *gin.Context) {
			abortV1(c, http.StatusNotFound, "not_found", "no such endpoint")
		})
		return
	}
	ui := router.Group("/", BasicAuth(cfg.UI.Username, cfg.UI.Password))
	if cfg.UI.Username == "" {
		log.Printf("[Security] Web UI has no password (ui.username/ui.password). Do not expose it to the internet.")
	}

	ui.Static("/static", "./static")
	ui.StaticFile("/favicon.ico", "./static/favicon.ico")

	asHTML := func(fn gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Request.Header.Set("Accept", "text/html")
			fn(c)
		}
	}
	ui.GET("/", h.Index)
	ui.GET("/search", h.SearchPage)
	ui.GET("/phrases", h.PhrasesPage)
	ui.GET("/jobs", h.JobsPage)
	ui.GET("/discovery", h.DiscoveryJobsPage)
	ui.GET("/ports", h.PortScansPage)
	ui.GET("/scrape", h.ScrapePage)
	ui.GET("/schedules", h.SchedulesPage)
	ui.GET("/schedules/:id", h.SchedulePage)
	ui.GET("/system", h.SystemPage)
	ui.GET("/jobs/:id/pages/:pageId", asHTML(h.GetCrawledPage))
	ui.GET("/jobs/:id", asHTML(h.GetJob))
	ui.GET("/discovery/:id", asHTML(h.GetDiscoveryJob))
	ui.GET("/ports/:id", asHTML(h.GetPortScan))

	api := ui.Group("/api")
	{
		api.POST("/jobs", h.CreateJob)
		api.GET("/jobs", h.GetJobs)
		api.GET("/jobs/:id", h.GetJob)
		api.POST("/jobs/:id/start", h.StartJob)
		api.POST("/jobs/stop", h.StopJob)
		api.POST("/jobs/:id/stop", h.StopJob)
		api.POST("/jobs/pause", h.PauseJob)
		api.POST("/jobs/:id/pause", h.PauseJob)
		api.POST("/jobs/resume", h.ResumeJob)
		api.POST("/jobs/:id/resume", h.ResumeJob)
		api.POST("/jobs/:id/duplicate", h.DuplicateJob)
		api.DELETE("/jobs/:id", h.DeleteJob)

		api.GET("/discovery", h.GetDiscoveryJobs)
		api.GET("/discovery/:id", h.GetDiscoveryJob)
		api.POST("/discovery", h.CreateDiscoveryJob)
		api.DELETE("/discovery/:id", h.DeleteDiscoveryJob)

		api.GET("/ports", h.GetPortScans)
		api.GET("/ports/:id", h.GetPortScan)
		api.POST("/ports", h.CreatePortScan)
		api.POST("/ports/:id/stop", h.StopPortScan)
		api.DELETE("/ports/:id", h.DeletePortScan)

		api.POST("/jobs/:id/subdomains/discover", h.StartSubdomainDiscovery)
		api.GET("/jobs/:id/subdomains", h.GetSubdomains)
		api.POST("/subdomains/:id/crawl", h.StartCrawlForSubdomain)

		api.GET("/jobs/:id/pages", h.GetCrawledPages)
		api.GET("/jobs/:id/pages/:pageId", h.GetCrawledPage)
		api.GET("/jobs/:id/extracted-phrases", h.GetJobExtractedPhrases)
		api.GET("/matches", h.GetMatches)
		api.GET("/search", h.SearchAPI)
		api.POST("/semantic/rebuild", h.RebuildSemanticIndex)
		api.GET("/semantic/stats", h.GetSemanticSearchStats)

		api.GET("/phrases", h.GetPhrases)
		api.POST("/phrases", h.AddPhrase)
		api.PUT("/phrases/:id", h.UpdatePhrase)
		api.DELETE("/phrases", h.DeleteAllPhrases)
		api.DELETE("/phrases/:id", h.DeletePhrase)

		api.PUT("/jobs/:id/settings", h.UpdateJobSettings)
		api.PUT("/jobs/:id/target", h.UpdateJobTarget)
		api.GET("/settings/defaults", h.GetDefaultSettings)
		api.GET("/plugins", h.ListPlugins)
		api.GET("/stats", h.GetStats)

		// The UI uses the same versioned API as external clients, under its own auth.
		h.RegisterV1(api.Group("/v1", AllowPlugins(true)))
	}
}
