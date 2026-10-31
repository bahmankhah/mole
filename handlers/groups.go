package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
)

// groupingPage reads the comparison table's paging from the request.
func groupingPage(c *gin.Context) (page, perPage int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ = strconv.Atoi(c.DefaultQuery("per_page", "0"))
	return page, perPage
}

// ── scope-generic grouping endpoints ───────────────────────────────────

// jobScope resolves the grouping scope of the crawl named in the path.
func (h *Handler) jobScope(c *gin.Context) (scopeType, scopeID string, okScope bool) {
	job, err := h.jobManager.GetJob(c.Param("id"))
	if err != nil {
		abortV1(c, http.StatusNotFound, "not_found", "crawl not found")
		return "", "", false
	}
	scopeType, scopeID, grouped := jobs.ScopeForJob(job)
	if !grouped {
		abortV1(c, http.StatusConflict, "conflict", `this crawl does not group its products; set "group_products" in its settings`)
		return "", "", false
	}
	return scopeType, scopeID, true
}

// V1CrawlGrouping returns the product groups of a crawl — of its schedule,
// when it has one, so the table spans every run.
func (h *Handler) V1CrawlGrouping(c *gin.Context) {
	scopeType, scopeID, okScope := h.jobScope(c)
	if !okScope {
		return
	}
	page, perPage := groupingPage(c)
	dash, err := h.jobManager.GetGroupingDashboard(scopeType, scopeID, c.Query("field"), page, perPage)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, dash)
}

// V1RegroupCrawl recomputes a crawl's (or its schedule's) product groups
// from the pages already crawled; nothing is re-crawled.
func (h *Handler) V1RegroupCrawl(c *gin.Context) {
	scopeType, scopeID, okScope := h.jobScope(c)
	if !okScope {
		return
	}
	h.regroupAndReturn(c, scopeType, scopeID)
}

// V1ScheduleGrouping returns the product groups of a schedule: every run
// feeds the same groups, so the table carries their history.
func (h *Handler) V1ScheduleGrouping(c *gin.Context) {
	s, oks := h.loadSchedule(c)
	if !oks {
		return
	}
	page, perPage := groupingPage(c)
	dash, err := h.jobManager.GetGroupingDashboard(models.ScopeSchedule, s.ID, c.Query("field"), page, perPage)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, dash)
}

// V1RegroupSchedule recomputes a schedule's product groups from its latest
// finished run.
func (h *Handler) V1RegroupSchedule(c *gin.Context) {
	s, oks := h.loadSchedule(c)
	if !oks {
		return
	}
	h.regroupAndReturn(c, models.ScopeSchedule, s.ID)
}

// regroupAndReturn runs a matching pass on demand and answers with both
// what it did and the refreshed table. Asking for it by hand matches what
// has been crawled so far, so a crawl that is still running can be grouped
// without waiting for it to finish.
func (h *Handler) regroupAndReturn(c *gin.Context, scopeType, scopeID string) {
	res, err := h.jobManager.Regroup(scopeType, scopeID, jobs.RegroupOptions{Partial: true})
	if err != nil {
		v1Error(c, err)
		return
	}
	page, perPage := groupingPage(c)
	dash, err := h.jobManager.GetGroupingDashboard(scopeType, scopeID, c.Query("field"), page, perPage)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, gin.H{"result": res, "dashboard": dash})
}

// V1GroupingFields lists the product fields a group can compare, for the
// settings form's field picker. A job may also name any of its own
// extract_fields, which are read from each page's extracted JSON.
func (h *Handler) V1GroupingFields(c *gin.Context) {
	ok(c, http.StatusOK, gin.H{
		"fields":   content.ComparableFields,
		"defaults": content.DefaultCompareFields,
	})
}

// V1CreateProductGroup starts a new, empty product group in a scope that
// members can be moved into by hand.
func (h *Handler) V1CreateProductGroup(c *gin.Context) {
	var in struct {
		ScopeType string `json:"scope_type"`
		ScopeID   string `json:"scope_id"`
		Label     string `json:"label"`
		Brand     string `json:"brand"`
	}
	if _, bound := bindJSON(c, &in); !bound {
		return
	}
	pg, err := h.jobManager.CreateManualProductGroup(in.ScopeType, in.ScopeID, in.Label, in.Brand)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusCreated, pg)
}

// V1AssignUnmatched places a page the latest regroup could not match into a
// product group by hand.
func (h *Handler) V1AssignUnmatched(c *gin.Context) {
	var in struct {
		ScopeType      string `json:"scope_type"`
		ScopeID        string `json:"scope_id"`
		ProductGroupID string `json:"product_group_id"`
		CrawlJobID     string `json:"crawl_job_id"`
		PageID         uint   `json:"page_id"`
		SiteKey        string `json:"site_key"`
		SiteLabel      string `json:"site_label"`
		URL            string `json:"url"`
		VariantKey     string `json:"variant_key"`
	}
	if _, bound := bindJSON(c, &in); !bound {
		return
	}
	err := h.jobManager.AssignUnmatchedToGroup(in.ScopeType, in.ScopeID, in.ProductGroupID, in.CrawlJobID, in.PageID, in.SiteKey, in.SiteLabel, in.URL, in.VariantKey)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, gin.H{"assigned": true})
}

func memberIDParam(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("memberId"), 10, 64)
	if err != nil {
		abortV1(c, http.StatusBadRequest, "invalid_request", "invalid member id")
		return 0, false
	}
	return uint(id), true
}

// v1MemberAction wraps the pin/exclude style member actions, which work in
// any scope because a member id identifies its scope.
func (h *Handler) v1MemberAction(act func(uint) error, result gin.H) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, oks := memberIDParam(c)
		if !oks {
			return
		}
		if err := act(id); err != nil {
			v1Error(c, err)
			return
		}
		ok(c, http.StatusOK, result)
	}
}

// V1MoveGroupMember reassigns a member to another product group by hand.
func (h *Handler) V1MoveGroupMember(c *gin.Context) {
	id, oks := memberIDParam(c)
	if !oks {
		return
	}
	var in struct {
		ProductGroupID string `json:"product_group_id"`
	}
	if _, bound := bindJSON(c, &in); !bound {
		return
	}
	if err := h.jobManager.MoveGroupMember(id, in.ProductGroupID); err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, gin.H{"moved": true})
}
