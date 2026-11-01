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

// listingIDParam reads the listing a request acts on. The path segment is
// still called memberId on the older routes, which addressed the same rows
// under their former name and the same ids.
func listingIDParam(c *gin.Context) (uint, bool) {
	raw := c.Param("listingId")
	if raw == "" {
		raw = c.Param("memberId")
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		abortV1(c, http.StatusBadRequest, "invalid_request", "invalid listing id")
		return 0, false
	}
	return uint(id), true
}

// v1ListingAction wraps the pin/exclude style listing actions, which work
// in any scope because a listing id identifies its scope.
func (h *Handler) v1ListingAction(act func(uint) error, result gin.H) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, oks := listingIDParam(c)
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

// V1MoveListing reassigns a listing to another product group by hand. The
// listing's history moves with it, so this is also how an offer nothing
// matched is attached to a product.
func (h *Handler) V1MoveListing(c *gin.Context) {
	id, oks := listingIDParam(c)
	if !oks {
		return
	}
	var in struct {
		ProductGroupID string `json:"product_group_id"`
	}
	if _, bound := bindJSON(c, &in); !bound {
		return
	}
	if err := h.jobManager.MoveListing(id, in.ProductGroupID); err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, gin.H{"moved": true})
}

// V1ListingHistory returns one listing's tracked history of one field:
// what this shop has asked for this product over time, which is a
// different question from what the shops ask for it against each other.
func (h *Handler) V1ListingHistory(c *gin.Context) {
	id, oks := listingIDParam(c)
	if !oks {
		return
	}
	hist, err := h.jobManager.GetListingHistory(id, c.Query("field"))
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, hist)
}

// ── start URLs ─────────────────────────────────────────────────────────

// urlListBody is the body of the add/remove start-URL endpoints. Each
// entry may hold several whitespace-separated URLs, so posting a pasted
// block works the same as posting them one at a time.
type urlListBody struct {
	URLs []string `json:"urls"`
}

// V1ScheduleURLs lists a schedule's start URLs, target first.
func (h *Handler) V1ScheduleURLs(c *gin.Context) {
	s, oks := h.loadSchedule(c)
	if !oks {
		return
	}
	urls, err := h.jobManager.ScheduleURLs(s.ID)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, gin.H{"urls": urls})
}

// V1AddScheduleURLs adds sites to a schedule, from its next run.
func (h *Handler) V1AddScheduleURLs(c *gin.Context) {
	s, oks := h.loadSchedule(c)
	if !oks {
		return
	}
	var in urlListBody
	if _, bound := bindJSON(c, &in); !bound {
		return
	}
	res, err := h.jobManager.AddScheduleURLs(s.ID, in.URLs)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, res)
}

// V1RemoveScheduleURLs stops a schedule crawling the given sites. What they
// have already recorded is kept.
func (h *Handler) V1RemoveScheduleURLs(c *gin.Context) {
	s, oks := h.loadSchedule(c)
	if !oks {
		return
	}
	var in urlListBody
	if _, bound := bindJSON(c, &in); !bound {
		return
	}
	res, err := h.jobManager.RemoveScheduleURLs(s.ID, in.URLs)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, res)
}

// V1CrawlURLs lists a crawl's start URLs, target first.
func (h *Handler) V1CrawlURLs(c *gin.Context) {
	urls, err := h.jobManager.JobURLs(c.Param("id"))
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, gin.H{"urls": urls})
}

// V1AddCrawlURLs adds sites to a crawl. A crawl that is still running has
// them queued straight away; one that has not started picks them up when
// it does.
func (h *Handler) V1AddCrawlURLs(c *gin.Context) {
	var in urlListBody
	if _, bound := bindJSON(c, &in); !bound {
		return
	}
	res, err := h.jobManager.AddJobURLs(c.Param("id"), in.URLs)
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, res)
}

// urlCard is the data the shared start-URL card renders from. What adding
// a URL does depends on where the thing is, so the effect is worked out
// here and shown before the user acts rather than reported afterwards.
func urlCard(scope, id string, urls []string, removable bool, effect string, closed bool) gin.H {
	return gin.H{
		"urlScope": scope, "urlScopeID": id, "urls": urls,
		"urlRemovable": removable, "urlEffect": effect, "urlClosed": closed,
	}
}

// The card lists one row per site, which is the right shape for a crawl —
// a handful of sites, each worth naming. A batch is a different thing: a
// flat list of up to ten thousand pages, where the bulk textarea in the
// settings form is the better editor and a row apiece would be unusable.
// So the card is for crawls, and a batch keeps the textarea. The API and
// the CLI edit either.
func urlCardApplies(kind string) bool { return kind != models.JobKindBatch }

// scheduleURLCard describes a schedule's sites. A run already in progress
// is left alone: changing what a run covers halfway through would make its
// results mean two things.
func (h *Handler) scheduleURLCard(s *models.Schedule) gin.H {
	if !urlCardApplies(s.Template.Kind) {
		return nil
	}
	urls, err := h.jobManager.ScheduleURLs(s.ID)
	if err != nil {
		return nil
	}
	effect := "They are crawled from the next run."
	if !s.Enabled {
		effect = "This schedule is paused, so they are crawled when you resume it."
	}
	return urlCard("schedule", s.ID, urls, true, effect, false)
}

// crawlURLCard describes a crawl's sites. A crawl that has finished cannot
// be extended — its results describe the sites it covered, and quietly
// adding one it never visited would make them describe nothing in
// particular — so the card says so instead of offering the form.
func (h *Handler) crawlURLCard(job *models.CrawlJob) gin.H {
	if !urlCardApplies(job.Kind) {
		return nil
	}
	urls, err := h.jobManager.JobURLs(job.ID)
	if err != nil {
		return nil
	}
	switch job.Status {
	case models.JobStatusCompleted, models.JobStatusFailed, models.JobStatusCancelled:
		return urlCard("crawl", job.ID, urls, false,
			"This crawl has already "+string(job.Status)+", so it cannot be given another site. "+
				"Duplicate it to run again with one more, or put it on a schedule so every run picks up the current list.", true)
	case models.JobStatusRunning:
		return urlCard("crawl", job.ID, urls, false,
			"They are queued on the running crawl straight away — it starts fetching them within a few seconds.", false)
	}
	// A crawl that has started and stopped resumes from the queue it
	// already has, so a new site goes into that queue now rather than
	// waiting to be seeded — a resuming run never re-adds its start URLs.
	if job.StartedAt != nil {
		return urlCard("crawl", job.ID, urls, false,
			"They are queued now, and fetched as soon as this crawl picks up again.", false)
	}
	return urlCard("crawl", job.ID, urls, false, "They are crawled when this crawl starts.", false)
}
