package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// JobsPage renders the paginated crawl-job list.
func (h *Handler) JobsPage(c *gin.Context) {
	page, limit, offset := parsePage(c, 20)
	jobs, total, err := h.jobManager.GetJobs(limit, offset)
	if err != nil {
		jobs = nil
	}
	totalPages := pageCount(total, limit)
	if page > totalPages && total > 0 {
		c.Redirect(http.StatusFound, "/jobs?page="+strconv.Itoa(totalPages))
		return
	}
	from, to := showingRange(total, offset, len(jobs))
	c.HTML(http.StatusOK, "jobs.html", gin.H{
		"jobs":       jobs,
		"page":       page,
		"totalPages": totalPages,
		"pageBase":   "/jobs?",
		"total":      int(total),
		"from":       from,
		"to":         to,
		"activeJob":  h.jobManager.GetActiveJob(),
	})
}

// DiscoveryJobsPage renders the paginated subdomain-discovery list.
func (h *Handler) DiscoveryJobsPage(c *gin.Context) {
	page, limit, offset := parsePage(c, 20)
	jobs, total, err := h.jobManager.GetDiscoveryJobs(limit, offset)
	if err != nil {
		jobs = nil
	}
	totalPages := pageCount(total, limit)
	if page > totalPages && total > 0 {
		c.Redirect(http.StatusFound, "/discovery?page="+strconv.Itoa(totalPages))
		return
	}
	from, to := showingRange(total, offset, len(jobs))
	c.HTML(http.StatusOK, "discovery_jobs.html", gin.H{
		"jobs":       jobs,
		"page":       page,
		"totalPages": totalPages,
		"pageBase":   "/discovery?",
		"total":      int(total),
		"from":       from,
		"to":         to,
	})
}

// PortScansPage renders the paginated port-scan list.
func (h *Handler) PortScansPage(c *gin.Context) {
	page, limit, offset := parsePage(c, 20)
	jobs, total, err := h.jobManager.GetPortScans(limit, offset)
	if err != nil {
		jobs = nil
	}
	totalPages := pageCount(total, limit)
	if page > totalPages && total > 0 {
		c.Redirect(http.StatusFound, "/ports?page="+strconv.Itoa(totalPages))
		return
	}
	from, to := showingRange(total, offset, len(jobs))
	c.HTML(http.StatusOK, "port_scans.html", gin.H{
		"jobs":       jobs,
		"page":       page,
		"totalPages": totalPages,
		"pageBase":   "/ports?",
		"total":      int(total),
		"from":       from,
		"to":         to,
	})
}
