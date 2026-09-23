package handlers

import (
	"encoding/json"
	"errors"
	"html"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
)

func formSubmission(c *gin.Context) bool {
	contentType := c.GetHeader("Content-Type")
	return strings.Contains(contentType, "application/x-www-form-urlencoded") ||
		strings.Contains(contentType, "multipart/form-data")
}

func (h *Handler) portScanError(c *gin.Context, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, modules.ErrBadScanTarget) ||
		errors.Is(err, modules.ErrResolveFailed) ||
		errors.Is(err, jobs.ErrPortScanRunning) {
		code = http.StatusBadRequest
	}
	if formSubmission(c) {
		if errors.Is(err, jobs.ErrPortScanRunning) {
			if id := h.jobManager.ActivePortScanID(); id != "" {
				c.Redirect(http.StatusFound, "/ports/"+id)
				return
			}
		}
		page := `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Port scan</title></head><body style="font-family:sans-serif;padding:2rem;max-width:36rem">
<h1>Couldn’t start the port scan</h1>
<p>` + html.EscapeString(err.Error()) + `</p>
<p><a href="/">Back to dashboard</a></p>
</body></html>`
		c.Data(code, "text/html; charset=utf-8", []byte(page))
		return
	}
	c.JSON(code, Response{Success: false, Error: err.Error()})
}

// CreatePortScan starts a port scan for a domain or IP address.
func (h *Handler) CreatePortScan(c *gin.Context) {
	var req struct {
		Target string `json:"target" form:"target" binding:"required"`
	}
	if err := c.ShouldBind(&req); err != nil {
		h.portScanError(c, modules.ErrBadScanTarget)
		return
	}

	job, err := h.jobManager.CreatePortScan(req.Target)
	if err != nil {
		h.portScanError(c, err)
		return
	}

	if formSubmission(c) || c.GetHeader("Accept") == "text/html" {
		c.Redirect(http.StatusFound, "/ports/"+job.ID)
		return
	}
	c.JSON(http.StatusCreated, Response{Success: true, Data: job})
}

// GetPortScans returns port scan jobs.
func (h *Handler) GetPortScans(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	scans, total, err := h.jobManager.GetPortScans(limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"jobs":   scans,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		},
	})
}

// GetPortScan returns one scan and the open ports found so far.
func (h *Handler) GetPortScan(c *gin.Context) {
	jobID := c.Param("id")
	job, err := h.jobManager.GetPortScan(jobID)
	if err != nil {
		c.JSON(http.StatusNotFound, Response{Success: false, Error: "Port scan not found"})
		return
	}
	ports, _ := h.jobManager.GetDiscoveredPorts(jobID)
	if ports == nil {
		ports = []models.DiscoveredPort{}
	}

	if c.GetHeader("Accept") == "text/html" || c.Query("format") == "html" {
		payload, _ := json.Marshal(gin.H{"job": job, "ports": ports})
		c.HTML(http.StatusOK, "ports.html", gin.H{
			"job":     job,
			"ports":   ports,
			"initial": template.JS(payload),
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"job":   job,
			"ports": ports,
		},
	})
}

// DeletePortScan removes a port scan job and its discovered ports.
func (h *Handler) DeletePortScan(c *gin.Context) {
	if err := h.jobManager.DeletePortScan(c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, Response{Success: true, Message: "Port scan deleted"})
}

// StopPortScan cancels a running port scan.
func (h *Handler) StopPortScan(c *gin.Context) {
	if err := h.jobManager.StopPortScan(c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, Response{Success: true, Message: "Port scan stopping"})
}
