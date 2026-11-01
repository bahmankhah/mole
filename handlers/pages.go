package handlers

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/plugins"
)

// ScrapePage renders the scrape / map playground.
func (h *Handler) ScrapePage(c *gin.Context) {
	catalog, _ := json.Marshal(h.catalog())
	c.HTML(http.StatusOK, "scrape.html", gin.H{
		"nav":         "scrape",
		"catalogJSON": template.JS(catalog),
		"initialURL":  c.Query("url"),
	})
}

// SchedulesPage lists schedules.
func (h *Handler) SchedulesPage(c *gin.Context) {
	page, limit, offset := parsePage(c, 20)
	list, total, _ := h.jobManager.ListSchedules("", limit, offset)
	from, to := showingRange(total, offset, len(list))
	c.HTML(http.StatusOK, "schedules.html", gin.H{
		"nav":        "schedules",
		"schedules":  list,
		"total":      int(total),
		"page":       page,
		"totalPages": pageCount(total, limit),
		"pageBase":   "/schedules?",
		"from":       from,
		"to":         to,
	})
}

// SchedulePage shows one schedule with its runs and full settings. The id
// "new" opens the form for a new schedule, prefilled from a crawl with
// ?from_job=<id>.
func (h *Handler) SchedulePage(c *gin.Context) {
	data := gin.H{
		"nav":                 "schedules",
		"catalogJSON":         h.catalogJS(),
		"defaultSettingsJSON": h.defaultSettingsJS(),
		"pluginsJSON":         h.pluginsJS(),
		"settingsEditable":    true,
	}
	if c.Param("id") == "new" {
		in := jobs.ScheduleInput{Cron: "0 3 * * *", Timezone: "Asia/Tehran", Template: models.JobTemplate{Kind: models.JobKindCrawl}}
		if id := c.Query("from_job"); id != "" {
			job, err := h.jobManager.GetJob(id)
			if err != nil {
				c.Redirect(http.StatusFound, "/schedules/new")
				return
			}
			if in, err = jobs.ScheduleInputFromJob(job); err != nil {
				c.Redirect(http.StatusFound, "/jobs/"+id)
				return
			}
			data["sourceJob"] = job
		}
		b, _ := json.Marshal(in)
		data["isNew"] = true
		data["scheduleJSON"] = template.JS(b)
		c.HTML(http.StatusOK, "schedule.html", data)
		return
	}
	s, err := h.jobManager.GetSchedule(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/schedules")
		return
	}
	runs, _ := h.jobManager.GetScheduleJobs(s.ID, 50)
	type runRow struct {
		Job     interface{}
		Changes map[string]int64
	}
	rows := make([]runRow, len(runs))
	for i := range runs {
		rows[i] = runRow{Job: runs[i], Changes: h.jobManager.ChangeCounts(runs[i].ID)}
	}
	var upcoming []time.Time
	if s.Enabled {
		upcoming, _ = jobs.NextRuns(s.Cron, s.Timezone, time.Now(), 3)
	}
	b, _ := json.Marshal(jobs.MaskSchedule(s))
	data["schedule"] = s
	data["runs"] = rows
	data["upcoming"] = upcoming
	data["scheduleJSON"] = template.JS(b)
	// A schedule is the scope that accumulates history, so its own page is
	// where the comparison table and the tracking it enables belong.
	for k, v := range h.scheduleURLCard(s) {
		data[k] = v
	}
	if h.scheduleGroups(s) {
		gpage, gper := groupingPage(c)
		if d, err := h.jobManager.GetGroupingDashboard(models.ScopeSchedule, s.ID, c.Query("field"), gpage, gper); err == nil {
			d.BasePath, d.Anchor = "/schedules/"+s.ID, "compare"
			data["grouping"], data["showGrouping"] = d, true
		}
	}
	c.HTML(http.StatusOK, "schedule.html", data)
}

// catalogJS, defaultSettingsJS and pluginsJS are the JSON the settings form needs.
func (h *Handler) catalogJS() template.JS {
	b, _ := json.Marshal(h.catalog())
	return template.JS(b)
}

func (h *Handler) defaultSettingsJS() template.JS {
	b, _ := json.Marshal(h.jobManager.GetDefaultJobSettings())
	return template.JS(b)
}

func (h *Handler) pluginsJS() template.JS {
	list, err := plugins.List()
	if err != nil {
		log.Printf("[Handler] Failed to list plugins: %v", err)
	}
	if list == nil {
		list = []plugins.Plugin{}
	}
	b, _ := json.Marshal(list)
	return template.JS(b)
}

// SystemPage shows workers, live runs, the queue and engine capabilities.
func (h *Handler) SystemPage(c *gin.Context) {
	stats := h.jobManager.GetEngineStats()
	queued, _, _ := h.jobManager.ListJobs(jobs.JobFilter{Status: "queued", Limit: 50})
	catalog := h.catalog()
	workers := h.jobManager.Workers()
	browserWorkers := 0
	for _, w := range workers {
		if w.Browser && w.State == models.WorkerActive {
			browserWorkers++
		}
	}
	// Queued jobs that only a worker with a headless browser can run.
	needBrowser := 0
	for _, j := range queued {
		if h.jobManager.Engine().NeedsBrowser(j.Settings) {
			needBrowser++
		}
	}
	c.HTML(http.StatusOK, "system.html", gin.H{
		"nav":            "system",
		"stats":          stats,
		"runs":           h.jobManager.Engine().Runs(),
		"workers":        workers,
		"browserWorkers": browserWorkers,
		"needBrowser":    needBrowser,
		"queued":         queued,
		"catalog":        catalog,
		"apiOpen":        len(h.jobManager.Config().API.Tokens) == 0,
		"uiOpen":         h.jobManager.Config().UI.Username == "",
		"llmModel":       catalog.LLMModel,
		"config":         h.jobManager.Config(),
	})
}

// ListingPage renders one listing's tracking view: what a single shop has
// asked for one product over time.
//
// This is the other half of the comparison table, and deliberately a page
// of its own rather than a panel over it: a price history is something to
// link to and come back to. The table answers "who sells this and for how
// much"; this answers "what has this shop done with the price".
func (h *Handler) ListingPage(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/schedules")
		return
	}
	hist, err := h.jobManager.GetListingHistory(uint(id), c.Query("field"))
	if err != nil {
		c.Redirect(http.StatusFound, "/schedules")
		return
	}
	backPath, backLabel := "/schedules/"+hist.Listing.ScopeID, "the comparison"
	nav := "schedules"
	if hist.Listing.ScopeType == models.ScopeCrawl {
		backPath, nav = "/jobs/"+hist.Listing.ScopeID, "jobs"
	}
	c.HTML(http.StatusOK, "listing.html", gin.H{
		"nav": nav, "hist": hist, "backPath": backPath, "backLabel": backLabel,
	})
}

// scheduleGroups reports whether a schedule matches its products, and so
// has a comparison table worth showing.
func (h *Handler) scheduleGroups(s *models.Schedule) bool {
	if !h.jobManager.Config().Grouping.Enabled {
		return false
	}
	set := s.Template.Settings
	return set != nil && set.GroupProducts != nil && *set.GroupProducts
}
