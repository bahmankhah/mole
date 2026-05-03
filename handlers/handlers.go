package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
)

// Handler holds all HTTP handlers
type Handler struct {
	jobManager *jobs.Manager
}

// NewHandler creates a new handler
func NewHandler(jm *jobs.Manager) *Handler {
	return &Handler{
		jobManager: jm,
	}
}

// Response is a standard API response
type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// Index renders the main dashboard
func (h *Handler) Index(c *gin.Context) {
	jobs, _, _ := h.jobManager.GetJobs(10, 0)
	discoveryJobs, _, _ := h.jobManager.GetDiscoveryJobs(10, 0)
	matches, _, _ := h.jobManager.GetAllPhraseMatches(10, 0)
	phrases, _ := h.jobManager.GetRecentSearchPhrases(10)
	allPhrases, _ := h.jobManager.GetSearchPhrases()
	totalPhrases := 0
	if allPhrases != nil {
		totalPhrases = len(allPhrases)
	}
	activeJob := h.jobManager.GetActiveJob()
	stats := h.jobManager.GetEngineStats()

	c.HTML(http.StatusOK, "index.html", gin.H{
		"jobs":          jobs,
		"discoveryJobs": discoveryJobs,
		"matches":       matches,
		"phrases":       phrases,
		"totalPhrases":  totalPhrases,
		"activeJob":     activeJob,
		"stats":         stats,
	})
}

// CreateJob creates a new crawl job.
// Supports URL templates with {{VAR}} placeholders.
// When template_vars is provided, the URL is expanded into multiple seed URLs.
func (h *Handler) CreateJob(c *gin.Context) {
	var req struct {
		TargetURL    string              `json:"target_url" form:"target_url"`
		Domain       string              `json:"domain" form:"domain"`
		MaxDepth     int                 `json:"max_depth" form:"max_depth"`
		Settings     *models.JobSettings `json:"settings"`
		TemplateVars map[string]string   `json:"template_vars"` // var_name → value expression (e.g. "1,hamster,5-10")
	}

	contentType := c.GetHeader("Content-Type")
	isForm := strings.Contains(contentType, "application/x-www-form-urlencoded") ||
		strings.Contains(contentType, "multipart/form-data")

	if isForm {
		req.TargetURL = c.PostForm("target_url")
		req.Domain = c.PostForm("domain")
		req.MaxDepth, _ = strconv.Atoi(c.PostForm("max_depth"))

		// Parse template_vars from form: keys like "var_NAME" → value expression.
		req.TemplateVars = make(map[string]string)
		for key, vals := range c.Request.PostForm {
			if strings.HasPrefix(key, "var_") && len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
				varName := strings.TrimPrefix(key, "var_")
				req.TemplateVars[varName] = vals[0]
			}
		}
	} else {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
			return
		}
	}

	// Use target_url if provided, otherwise fall back to domain
	target := req.TargetURL
	if target == "" {
		target = req.Domain
	}
	if target == "" {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: "target_url or domain is required"})
		return
	}

	if req.MaxDepth == 0 {
		req.MaxDepth = 10
	}

	// ── Expand URL template if it contains {{VAR}} placeholders ──
	var seedURLs models.StringSlice
	if modules.HasTemplateVars(target) && len(req.TemplateVars) > 0 {
		// Expand each variable's value expression.
		expandedVars := make(map[string][]string, len(req.TemplateVars))
		for name, expr := range req.TemplateVars {
			vals, err := modules.ExpandValueExpr(expr)
			if err != nil {
				errMsg := fmt.Sprintf("error expanding variable {{%s}}: %v", name, err)
				if isForm {
					c.Redirect(http.StatusFound, "/?error="+errMsg)
					return
				}
				c.JSON(http.StatusBadRequest, Response{Success: false, Error: errMsg})
				return
			}
			if len(vals) == 0 {
				errMsg := fmt.Sprintf("variable {{%s}} has no values", name)
				if isForm {
					c.Redirect(http.StatusFound, "/?error="+errMsg)
					return
				}
				c.JSON(http.StatusBadRequest, Response{Success: false, Error: errMsg})
				return
			}
			expandedVars[name] = vals
		}

		urls, err := modules.ExpandTemplateURL(target, expandedVars, 50000)
		if err != nil {
			if isForm {
				c.Redirect(http.StatusFound, "/?error="+err.Error())
				return
			}
			c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
			return
		}
		seedURLs = urls
		log.Printf("[Handler] URL template expanded to %d seed URLs", len(seedURLs))
	}

	job, err := h.jobManager.CreateJobWithSettings(target, req.MaxDepth, req.Settings)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	// Attach seed URLs if template was expanded
	if len(seedURLs) > 0 {
		job.SeedURLs = seedURLs
		if err := h.jobManager.UpdateJobSeedURLs(job.ID, seedURLs); err != nil {
			log.Printf("[Handler] Warning: failed to save seed URLs: %v", err)
		}
	}

	// Check if request is from a form submission
	if isForm || c.GetHeader("Accept") == "text/html" {
		c.Redirect(http.StatusFound, "/jobs/"+job.ID)
		return
	}

	c.JSON(http.StatusCreated, Response{Success: true, Data: job})
}

// GetJobs returns all jobs
func (h *Handler) GetJobs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	jobs, total, err := h.jobManager.GetJobs(limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"jobs":   jobs,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		},
	})
}

// GetJob returns a single job
func (h *Handler) GetJob(c *gin.Context) {
	jobID := c.Param("id")

	job, err := h.jobManager.GetJob(jobID)
	if err != nil {
		c.JSON(http.StatusNotFound, Response{Success: false, Error: "Job not found"})
		return
	}

	subdomains, _ := h.jobManager.GetSubdomains(jobID)
	matches, matchTotal, _ := h.jobManager.GetPhraseMatches(jobID, 50, 0)
	pages, pageTotal, _ := h.jobManager.GetCrawledPages(jobID, 50, 0)
	stats, _ := h.jobManager.GetJobStats(jobID)

	// Check if request accepts HTML
	if c.GetHeader("Accept") == "text/html" || c.Query("format") == "html" {
		defaultSettings := h.jobManager.GetDefaultJobSettings()
		// If the job has no custom settings, provide nil to the template
		var settingsJSON template.JS = "null"
		if job.Settings != nil {
			b, _ := json.Marshal(job.Settings)
			settingsJSON = template.JS(b)
		}
		defaultSettingsB, _ := json.Marshal(defaultSettings)

		c.HTML(http.StatusOK, "job.html", gin.H{
			"job":                 job,
			"subdomains":          subdomains,
			"matches":             matches,
			"matchTotal":          matchTotal,
			"pages":               pages,
			"pageTotal":           pageTotal,
			"stats":               stats,
			"settingsJSON":        settingsJSON,
			"defaultSettingsJSON": template.JS(defaultSettingsB),
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"job":        job,
			"subdomains": subdomains,
			"matches":    matches,
			"pages":      pages,
			"stats":      stats,
		},
	})
}

// StartJob starts a crawl job
func (h *Handler) StartJob(c *gin.Context) {
	jobID := c.Param("id")

	if err := h.jobManager.StartJob(jobID); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Message: "Job started"})
}

// StopJob stops the current crawl job
func (h *Handler) StopJob(c *gin.Context) {
	if err := h.jobManager.StopJob(); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Message: "Job stopped"})
}

// PauseJob pauses the current crawl job
func (h *Handler) PauseJob(c *gin.Context) {
	if err := h.jobManager.PauseJob(); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Message: "Job paused"})
}

// ResumeJob resumes the current crawl job
func (h *Handler) ResumeJob(c *gin.Context) {
	if err := h.jobManager.ResumeJob(); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Message: "Job resumed"})
}

// DeleteJob deletes a job
func (h *Handler) DeleteJob(c *gin.Context) {
	jobID := c.Param("id")

	if err := h.jobManager.DeleteJob(jobID); err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Message: "Job deleted"})
}

// StartSubdomainDiscovery starts subdomain discovery for a job
func (h *Handler) StartSubdomainDiscovery(c *gin.Context) {
	jobID := c.Param("id")

	if err := h.jobManager.StartSubdomainDiscovery(jobID); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Message: "Subdomain discovery started"})
}

// GetSubdomains returns subdomains for a job
func (h *Handler) GetSubdomains(c *gin.Context) {
	jobID := c.Param("id")

	subdomains, err := h.jobManager.GetSubdomains(jobID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Data: subdomains})
}

// StartCrawlForSubdomain creates and optionally starts a crawl job for a subdomain
func (h *Handler) StartCrawlForSubdomain(c *gin.Context) {
	subdomainIDStr := c.Param("id")
	subdomainID, err := strconv.ParseUint(subdomainIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: "Invalid subdomain ID"})
		return
	}

	var req struct {
		MaxDepth  int  `json:"max_depth"`
		AutoStart bool `json:"auto_start"`
	}
	c.ShouldBindJSON(&req)

	if req.MaxDepth == 0 {
		req.MaxDepth = 10
	}

	// Create the crawl job
	job, err := h.jobManager.CreateCrawlJobFromSubdomain(uint(subdomainID), req.MaxDepth)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	// Optionally start it immediately
	if req.AutoStart {
		if err := h.jobManager.StartJob(job.ID); err != nil {
			c.JSON(http.StatusOK, Response{
				Success: true,
				Message: "Crawl job created but could not auto-start: " + err.Error(),
				Data:    job,
			})
			return
		}
	}

	c.JSON(http.StatusCreated, Response{Success: true, Data: job, Message: "Crawl job created"})
}

// GetDiscoveryJobs returns all discovery jobs
func (h *Handler) GetDiscoveryJobs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	jobs, total, err := h.jobManager.GetDiscoveryJobs(limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"jobs":   jobs,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		},
	})
}

// CreateDiscoveryJob creates a new discovery job directly for a domain
func (h *Handler) CreateDiscoveryJob(c *gin.Context) {
	var req struct {
		Domain string `json:"domain" form:"domain" binding:"required"`
	}

	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	job, err := h.jobManager.CreateDiscoveryJobForDomain(req.Domain)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	// Check if request is from a form submission
	contentType := c.GetHeader("Content-Type")
	if contentType == "application/x-www-form-urlencoded" || c.GetHeader("Accept") == "text/html" {
		c.Redirect(http.StatusFound, "/discovery/"+job.ID)
		return
	}

	c.JSON(http.StatusCreated, Response{Success: true, Data: job})
}

// GetDiscoveryJob returns a single discovery job with its subdomains
func (h *Handler) GetDiscoveryJob(c *gin.Context) {
	jobID := c.Param("id")

	job, err := h.jobManager.GetDiscoveryJob(jobID)
	if err != nil {
		c.JSON(http.StatusNotFound, Response{Success: false, Error: "Discovery job not found"})
		return
	}

	subdomains, _ := h.jobManager.GetSubdomainsByDiscoveryJob(jobID)

	// Check if request accepts HTML
	if c.GetHeader("Accept") == "text/html" || c.Query("format") == "html" {
		c.HTML(http.StatusOK, "discovery.html", gin.H{
			"job":        job,
			"subdomains": subdomains,
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"job":        job,
			"subdomains": subdomains,
		},
	})
}

// GetMatches returns phrase matches
func (h *Handler) GetMatches(c *gin.Context) {
	jobID := c.Query("job_id")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	var matches []models.PhraseMatch
	var total int64
	var err error

	if jobID != "" {
		matches, total, err = h.jobManager.GetPhraseMatches(jobID, limit, offset)
	} else {
		matches, total, err = h.jobManager.GetAllPhraseMatches(limit, offset)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"matches": matches,
			"total":   total,
			"limit":   limit,
			"offset":  offset,
		},
	})
}

// GetPhrases returns all search phrases
func (h *Handler) GetPhrases(c *gin.Context) {
	phrases, err := h.jobManager.GetSearchPhrases()
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Data: phrases})
}

// AddPhrase adds a new search phrase
func (h *Handler) AddPhrase(c *gin.Context) {
	var req struct {
		Phrase string `json:"phrase" form:"phrase" binding:"required"`
	}

	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	phrase, err := h.jobManager.AddSearchPhrase(req.Phrase)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	// Check if request is from a form submission
	contentType := c.GetHeader("Content-Type")
	if contentType == "application/x-www-form-urlencoded" || c.GetHeader("Accept") == "text/html" {
		referer := c.GetHeader("Referer")
		if strings.Contains(referer, "/phrases") {
			c.Redirect(http.StatusFound, "/phrases")
		} else {
			c.Redirect(http.StatusFound, "/")
		}
		return
	}

	c.JSON(http.StatusCreated, Response{Success: true, Data: phrase})
}

// PhrasesPage renders the dedicated phrases management page
func (h *Handler) PhrasesPage(c *gin.Context) {
	phrases, err := h.jobManager.GetSearchPhrasesWithStats()
	if err != nil {
		phrases = nil
	}

	c.HTML(http.StatusOK, "phrases.html", gin.H{
		"phrases": phrases,
	})
}

// UpdatePhrase updates a search phrase
func (h *Handler) UpdatePhrase(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)

	var req struct {
		IsActive bool `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	if err := h.jobManager.UpdateSearchPhrase(uint(id), req.IsActive); err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Message: "Phrase updated"})
}

// DeletePhrase deletes a search phrase
func (h *Handler) DeletePhrase(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)

	if err := h.jobManager.DeleteSearchPhrase(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Message: "Phrase deleted"})
}

// GetStats returns current crawler statistics
func (h *Handler) GetStats(c *gin.Context) {
	stats := h.jobManager.GetEngineStats()
	activeJob := h.jobManager.GetActiveJob()

	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"engine":    stats,
			"activeJob": activeJob,
		},
	})
}

// GetCrawledPages returns crawled pages for a job
func (h *Handler) GetCrawledPages(c *gin.Context) {
	jobID := c.Param("id")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	pages, total, err := h.jobManager.GetCrawledPages(jobID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"pages":  pages,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		},
	})
}

// DuplicateJob creates a new job by copying an existing job's configuration
func (h *Handler) DuplicateJob(c *gin.Context) {
	jobID := c.Param("id")

	newJob, err := h.jobManager.DuplicateJob(jobID)
	if err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	// If request is from browser, redirect to the new job's page
	if c.GetHeader("Accept") == "text/html" || c.Query("redirect") == "true" {
		c.Redirect(http.StatusFound, "/jobs/"+newJob.ID)
		return
	}

	c.JSON(http.StatusCreated, Response{Success: true, Data: newJob, Message: "Job duplicated"})
}

// UpdateJobSettings updates settings for a pending job
func (h *Handler) UpdateJobSettings(c *gin.Context) {
	jobID := c.Param("id")

	// Read raw body to detect reset request
	var raw map[string]interface{}
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	// If the payload contains "reset": true, clear settings to nil (use defaults)
	if reset, ok := raw["reset"]; ok {
		if r, ok := reset.(bool); ok && r {
			if err := h.jobManager.UpdateJobSettings(jobID, nil); err != nil {
				c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
				return
			}
			c.JSON(http.StatusOK, Response{Success: true, Message: "Settings reset to defaults"})
			return
		}
	}

	// Re-marshal and unmarshal to get a proper JobSettings
	b, _ := json.Marshal(raw)
	var settings models.JobSettings
	if err := json.Unmarshal(b, &settings); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	// Check if settings are completely empty (all zero values) — treat as nil
	if settings.MaxConcurrentRequests == nil && settings.RequestTimeoutSec == nil &&
		settings.PolitenessDelayMs == nil && settings.MaxDepth == nil &&
		settings.MaxPages == nil &&
		settings.UserAgent == nil && settings.MaxRetries == nil &&
		settings.RespectRobotsTxt == nil &&
		settings.SkipContentDuplicates == nil &&
		settings.UseHeadlessBrowser == nil && settings.HeadlessWaitSelector == nil &&
		settings.EnableSemanticSearch == nil && settings.AfterCrawlScript == nil && settings.AfterJobScript == nil &&
		settings.SaveTextContent == nil && settings.EnableWordExtraction == nil &&
		settings.EnableStemming == nil && settings.EnableLemmatization == nil &&
		settings.DefaultLanguage == nil && settings.UseCrawlPhrasesOnly == nil &&
		len(settings.SkipExtensions) == 0 && len(settings.URLIncludePatterns) == 0 &&
		len(settings.URLExcludePatterns) == 0 && len(settings.ExtraTrackingParams) == 0 {
		if err := h.jobManager.UpdateJobSettings(jobID, nil); err != nil {
			c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
			return
		}
		c.JSON(http.StatusOK, Response{Success: true, Message: "Settings reset to defaults"})
		return
	}

	if err := h.jobManager.UpdateJobSettings(jobID, &settings); err != nil {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{Success: true, Message: "Settings updated"})
}

// GetDefaultSettings returns the default crawler settings
func (h *Handler) GetDefaultSettings(c *gin.Context) {
	settings := h.jobManager.GetDefaultJobSettings()
	c.JSON(http.StatusOK, Response{Success: true, Data: settings})
}

// SearchPage renders the search page
func (h *Handler) SearchPage(c *gin.Context) {
	query := c.Query("q")
	mode := c.DefaultQuery("mode", "keyword") // "keyword" or "semantic"
	crawlJobID := c.Query("crawl_job_id")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit := 20
	offset := (page - 1) * limit

	var results []models.SearchResult
	var semanticResults []models.SemanticSearchResult
	var total int64
	phraseExists := false

	semanticStats := h.jobManager.GetSemanticSearchStats()

	// Get crawl jobs for the filter dropdown
	crawlJobs, _, _ := h.jobManager.GetJobs(200, 0)

	if query != "" {
		if mode == "semantic" {
			// Semantic vector search
			var err error
			semanticResults, err = h.jobManager.SemanticSearch(query, limit, crawlJobID)
			if err != nil {
				semanticResults = nil
			}
			total = int64(len(semanticResults))
		} else {
			// Keyword search (existing n-gram matching)
			var err error
			results, total, err = h.jobManager.SearchPhraseMatches(query, crawlJobID, limit, offset)
			if err != nil {
				results = nil
				total = 0
			}
		}

		// Check if the search query already exists as a phrase
		phrases, _ := h.jobManager.GetSearchPhrases()
		for _, p := range phrases {
			if strings.EqualFold(p.Phrase, query) {
				phraseExists = true
				break
			}
		}
	}

	totalPages := int((total + int64(limit) - 1) / int64(limit))
	if totalPages < 1 {
		totalPages = 1
	}

	c.HTML(http.StatusOK, "search.html", gin.H{
		"query":           query,
		"mode":            mode,
		"crawlJobID":      crawlJobID,
		"crawlJobs":       crawlJobs,
		"results":         results,
		"semanticResults": semanticResults,
		"total":           total,
		"page":            page,
		"totalPages":      totalPages,
		"limit":           limit,
		"phraseExists":    phraseExists,
		"semanticStats":   semanticStats,
	})
}

// SearchAPI handles API search requests
func (h *Handler) SearchAPI(c *gin.Context) {
	query := c.Query("q")
	mode := c.DefaultQuery("mode", "keyword")
	crawlJobID := c.Query("crawl_job_id")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	if query == "" {
		c.JSON(http.StatusBadRequest, Response{Success: false, Error: "query parameter 'q' is required"})
		return
	}

	if mode == "semantic" {
		results, err := h.jobManager.SemanticSearch(query, limit, crawlJobID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
			return
		}
		c.JSON(http.StatusOK, Response{
			Success: true,
			Data: gin.H{
				"results":      results,
				"total":        len(results),
				"query":        query,
				"mode":         "semantic",
				"crawl_job_id": crawlJobID,
			},
		})
		return
	}

	results, total, err := h.jobManager.SearchPhraseMatches(query, crawlJobID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"results":      results,
			"total":        total,
			"limit":        limit,
			"offset":       offset,
			"query":        query,
			"mode":         "keyword",
			"crawl_job_id": crawlJobID,
		},
	})
}

// RebuildSemanticIndex triggers a rebuild of the FAISS semantic search index.
// Accepts an optional crawl_job_id query parameter to build a per-crawl index.
func (h *Handler) RebuildSemanticIndex(c *gin.Context) {
	crawlJobID := c.Query("crawl_job_id")
	go func() {
		if err := h.jobManager.RebuildSemanticIndexForCrawlJob(crawlJobID); err != nil {
			log.Printf("[Handler] Failed to rebuild semantic index: %v", err)
		}
	}()
	c.JSON(http.StatusOK, Response{Success: true, Message: "FAISS index rebuild started"})
}

// GetSemanticSearchStats returns semantic search stats
func (h *Handler) GetSemanticSearchStats(c *gin.Context) {
	stats := h.jobManager.GetSemanticSearchStats()
	c.JSON(http.StatusOK, Response{Success: true, Data: stats})
}

// GetJobExtractedPhrases returns paginated search phrases extracted by a crawl job.
func (h *Handler) GetJobExtractedPhrases(c *gin.Context) {
	jobID := c.Param("id")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	search := c.DefaultQuery("search", "")

	phrases, total, err := h.jobManager.GetJobExtractedPhrases(jobID, search, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, Response{
		Success: true,
		Data: gin.H{
			"phrases": phrases,
			"total":   total,
			"limit":   limit,
			"offset":  offset,
		},
	})
}
