package handlers

import (
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/jobs"
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
	catalog, _ := json.Marshal(h.catalog())
	c.HTML(http.StatusOK, "schedules.html", gin.H{
		"nav":         "schedules",
		"schedules":   list,
		"total":       int(total),
		"page":        page,
		"totalPages":  pageCount(total, limit),
		"pageBase":    "/schedules?",
		"from":        from,
		"to":          to,
		"catalogJSON": template.JS(catalog),
	})
}

// SchedulePage shows one schedule with its runs.
func (h *Handler) SchedulePage(c *gin.Context) {
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
	scheduleJSON, _ := json.Marshal(jobs.MaskSchedule(s))
	catalog, _ := json.Marshal(h.catalog())
	c.HTML(http.StatusOK, "schedule.html", gin.H{
		"nav":          "schedules",
		"schedule":     s,
		"runs":         rows,
		"scheduleJSON": template.JS(scheduleJSON),
		"catalogJSON":  template.JS(catalog),
	})
}

// SystemPage shows workers, live runs, the queue and engine capabilities.
func (h *Handler) SystemPage(c *gin.Context) {
	stats := h.jobManager.GetEngineStats()
	queued, _, _ := h.jobManager.ListJobs(jobs.JobFilter{Status: "queued", Limit: 50})
	catalog := h.catalog()
	c.HTML(http.StatusOK, "system.html", gin.H{
		"nav":      "system",
		"stats":    stats,
		"runs":     h.jobManager.Engine().Runs(),
		"workers":  h.jobManager.Workers(),
		"queued":   queued,
		"catalog":  catalog,
		"apiOpen":  len(h.jobManager.Config().API.Tokens) == 0,
		"uiOpen":   h.jobManager.Config().UI.Username == "",
		"llmModel": catalog.LLMModel,
		"config":   h.jobManager.Config(),
	})
}
