package jobs

import (
	"sort"
	"time"

	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/models"
	"gorm.io/gorm"
)

// maxFieldHistory bounds how many past values the dashboard keeps per
// group/site/field, for its sparkline.
const maxFieldHistory = 60

// GroupSiteStatus is one column of the comparison table: a domain the
// scope's crawl covered.
type GroupSiteStatus struct {
	SiteKey      string     `json:"site_key"`
	Label        string     `json:"label"`
	TargetURL    string     `json:"target_url,omitempty"`
	Disabled     bool       `json:"disabled,omitempty"`
	LatestJobID  string     `json:"latest_job_id,omitempty"`
	Status       string     `json:"status,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	ProductCount int        `json:"product_count"`
}

// FieldPoint is one past observation, for a history sparkline.
type FieldPoint struct {
	Num        *float64  `json:"num,omitempty"`
	Text       string    `json:"text,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// FieldCell is one site's value of the selected field for a row, as of
// that site's most recent run. Several pages of the site can feed one
// cell (the same product reached by different URLs): a numeric field then
// shows the best (lowest) of them, with Low/High saying how far the pages
// disagreed, and a text field shows the newest.
type FieldCell struct {
	Num          *float64     `json:"num,omitempty"`
	Text         string       `json:"text,omitempty"`
	Currency     string       `json:"currency,omitempty"`
	URL          string       `json:"url"`
	MemberID     uint         `json:"member_id,omitempty"`
	Pinned       bool         `json:"pinned,omitempty"`
	Pages        int          `json:"pages,omitempty"`     // pages of this site merged into the cell
	AlsoURLs     []string     `json:"also_urls,omitempty"` // the other pages' URLs, for "merged from"
	Low          *float64     `json:"low,omitempty"`       // spread across those pages, when they disagree
	High         *float64     `json:"high,omitempty"`
	ObservedAt   time.Time    `json:"observed_at"`
	Previous     *float64     `json:"previous,omitempty"` // this site's value in the run before, when it changed
	PreviousText string       `json:"previous_text,omitempty"`
	PreviousAt   *time.Time   `json:"previous_at,omitempty"`
	Stale        bool         `json:"stale,omitempty"` // the site's latest run no longer carries this product
	Runs         int          `json:"runs,omitempty"`  // runs this site has recorded for the row
	History      []FieldPoint `json:"history,omitempty"`
}

// Changed reports whether the value differs from the previous run's.
func (c FieldCell) Changed() bool {
	return c.Num != nil && c.Previous != nil && *c.Num != *c.Previous
}

// Delta is the change since the previous run (0 when unchanged).
func (c FieldCell) Delta() float64 {
	if !c.Changed() {
		return 0
	}
	return *c.Num - *c.Previous
}

// TextChanged reports whether a text field (availability, seller, …)
// differs from the previous run's — "out of stock" becoming "in stock" is
// the change a numeric delta cannot show.
func (c FieldCell) TextChanged() bool {
	return c.Num == nil && c.Text != "" && c.PreviousText != "" && c.Text != c.PreviousText
}

// Disagrees reports whether the pages merged into this cell gave different
// values in the latest run — a sign they may not all be the same product.
func (c FieldCell) Disagrees() bool {
	return c.Low != nil && c.High != nil && *c.High > *c.Low
}

// ProductRow is one ProductGroup with its members and the selected field's
// current value at every site that carries it.
type ProductRow struct {
	models.ProductGroup
	Members []models.ProductGroupMember `json:"members"`
	Cells   map[string]*FieldCell       `json:"cells"` // keyed by site key
}

// UnmatchedRow is a page nothing matched yet (exact_only/manual mode, or an
// auto-mode row below the threshold), offered for assignment by hand.
type UnmatchedRow struct {
	CrawlJobID string    `json:"crawl_job_id"`
	SiteKey    string    `json:"site_key"`
	SiteLabel  string    `json:"site_label"`
	PageID     uint      `json:"page_id"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	VariantKey string    `json:"variant_key"`
	Num        *float64  `json:"num,omitempty"`
	Text       string    `json:"text,omitempty"`
	Currency   string    `json:"currency,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// GroupingDashboard is everything a comparison table shows, for any scope.
// Rows are one page of the scope's groups; TotalGroups is how many there
// are in all.
type GroupingDashboard struct {
	ScopeType      string            `json:"scope_type"`
	ScopeID        string            `json:"scope_id"`
	Fields         []string          `json:"fields"` // the scope's compared fields
	Field          string            `json:"field"`  // the one this table shows
	FieldKind      string            `json:"field_kind"`
	PrimaryField   string            `json:"primary_field"`
	Sites          []GroupSiteStatus `json:"sites"`
	Rows           []ProductRow      `json:"rows"`
	Unmatched      []UnmatchedRow    `json:"unmatched"`
	LastObservedAt *time.Time        `json:"last_observed_at,omitempty"`

	TotalGroups int    `json:"total_groups"`
	Page        int    `json:"page"`
	PerPage     int    `json:"per_page"`
	TotalPages  int    `json:"total_pages"`
	MergedPages int    `json:"merged_pages"` // pages this page's groups were built from
	BasePath    string `json:"-"`            // where the table lives, for its paging links
	Anchor      string `json:"-"`            // the tab to come back to
}

// defaultGroupsPerPage is how many product groups one page of the table shows.
const defaultGroupsPerPage = 50

// GetGroupingDashboard assembles the comparison table of one scope. field
// selects which compared field the table shows; empty means the scope's
// primary field (price, when it is compared).
func (m *Manager) GetGroupingDashboard(scopeType, scopeID, field string, page, perPage int) (*GroupingDashboard, error) {
	if !models.ValidGroupingScope(scopeType) {
		return nil, invalidInput("unknown grouping scope %q", scopeType)
	}
	if perPage <= 0 || perPage > 500 {
		perPage = defaultGroupsPerPage
	}
	if page < 1 {
		page = 1
	}
	plan, err := m.planFor(groupScope{Type: scopeType, ID: scopeID})
	if err != nil {
		return nil, err
	}
	if field == "" || !containsString(plan.compareFields, field) {
		field = plan.primaryField()
	}
	dash := &GroupingDashboard{
		ScopeType: scopeType, ScopeID: scopeID,
		Fields: plan.compareFields, Field: field, FieldKind: content.FieldKindOf(field), PrimaryField: plan.primaryField(),
		Page: page, PerPage: perPage,
	}
	if dash.FieldKind == "" {
		dash.FieldKind = content.FieldKindText // an extract field: compared as whatever it parsed to
	}
	var allMembers []models.ProductGroupMember
	m.db.Where("scope_type = ? AND scope_id = ?", scopeType, scopeID).Find(&allMembers)
	members := make([]models.ProductGroupMember, 0, len(allMembers))
	counts := map[string]int{}
	labels := map[string]string{}
	for _, mem := range allMembers {
		if mem.Excluded {
			continue
		}
		// A stale member still renders (greyed, with its history and its
		// pin/exclude buttons), but it is not counted as a product the
		// site currently carries.
		members = append(members, mem)
		if !mem.Stale {
			counts[mem.SiteKey]++
		}
		if mem.SiteLabel != "" {
			labels[mem.SiteKey] = mem.SiteLabel
		}
	}
	dash.Sites = m.siteColumns(plan, counts, labels)

	var total int64
	m.db.Model(&models.ProductGroup{}).Where("scope_type = ? AND scope_id = ?", scopeType, scopeID).Count(&total)
	dash.TotalGroups = int(total)
	dash.TotalPages = (dash.TotalGroups + perPage - 1) / perPage
	if dash.TotalPages < 1 {
		dash.TotalPages = 1
	}

	var groups []models.ProductGroup
	m.db.Where("scope_type = ? AND scope_id = ?", scopeType, scopeID).
		Order("member_count DESC, label ASC").Limit(perPage).Offset((page - 1) * perPage).Find(&groups)
	if len(groups) > 0 {
		ids := make([]string, len(groups))
		for i, g := range groups {
			ids[i] = g.ID
		}
		membersByGroup := map[string][]models.ProductGroupMember{}
		for _, mem := range members {
			membersByGroup[mem.ProductGroupID] = append(membersByGroup[mem.ProductGroupID], mem)
		}
		var obs []models.FieldObservation
		m.db.Where("product_group_id IN ? AND field = ?", ids, field).Order("observed_at ASC, id ASC").Find(&obs)
		type key struct{ group, site string }
		history := map[key][]models.FieldObservation{}
		for _, o := range obs {
			history[key{o.ProductGroupID, o.SiteKey}] = append(history[key{o.ProductGroupID, o.SiteKey}], o)
			if dash.LastObservedAt == nil || o.ObservedAt.After(*dash.LastObservedAt) {
				at := o.ObservedAt
				dash.LastObservedAt = &at
			}
		}
		// When a site's latest run is newer than a row's newest value for
		// it, that row was not found there any more: the cell is stale,
		// not current.
		siteLatest := m.latestRunPerSite(scopeType, scopeID, field)
		for _, g := range groups {
			row := ProductRow{ProductGroup: g, Members: membersByGroup[g.ID], Cells: map[string]*FieldCell{}}
			dash.MergedPages += len(row.Members)
			membersBySite := map[string][]models.ProductGroupMember{}
			for _, mem := range row.Members {
				membersBySite[mem.SiteKey] = append(membersBySite[mem.SiteKey], mem)
			}
			for site, members := range membersBySite {
				cell := buildCell(history[key{g.ID, site}], members)
				if cell == nil {
					continue
				}
				if at, ok := siteLatest[site]; ok && at.After(cell.ObservedAt) {
					cell.Stale = true
				}
				row.Cells[site] = cell
			}
			dash.Rows = append(dash.Rows, row)
		}
	}

	var pending []models.FieldObservation
	m.db.Where("scope_type = ? AND scope_id = ? AND product_group_id = ? AND field = ?", scopeType, scopeID, "", field).
		Order("observed_at DESC").Find(&pending)
	seen := map[string]bool{}
	for _, o := range pending {
		k := overrideKey(o.SiteKey, o.URL, o.VariantKey)
		if seen[k] {
			continue
		}
		seen[k] = true
		dash.Unmatched = append(dash.Unmatched, UnmatchedRow{
			CrawlJobID: o.CrawlJobID, SiteKey: o.SiteKey, SiteLabel: o.SiteLabel, PageID: o.PageID,
			Title: o.Title, URL: o.URL, VariantKey: o.VariantKey,
			Num: o.NumValue, Text: o.TextValue, Currency: o.Currency, ObservedAt: o.ObservedAt,
		})
	}
	sort.Slice(dash.Unmatched, func(i, j int) bool { return dash.Unmatched[i].ObservedAt.After(dash.Unmatched[j].ObservedAt) })
	return dash, nil
}

// buildCell turns one site's observations of one product into the table's
// cell. Observations are grouped into the runs that produced them, so the
// value shown is this site's value in its latest run and "previous" is its
// value in the run before — not, as a page-by-page reading would have it,
// another page of the same site in the same run.
func buildCell(obs []models.FieldObservation, members []models.ProductGroupMember) *FieldCell {
	if len(obs) == 0 {
		return nil
	}
	runs := groupObservationsByRun(obs)
	if len(runs) == 0 {
		return nil
	}
	latest := runs[len(runs)-1]
	cell := &FieldCell{
		Num: latest.num, Text: latest.text, Currency: latest.currency, URL: latest.url,
		Pages: latest.pages, AlsoURLs: latest.alsoURLs, ObservedAt: latest.at, Runs: len(runs),
	}
	if latest.low != nil && latest.high != nil && *latest.high > *latest.low {
		cell.Low, cell.High = latest.low, latest.high
	}
	if len(runs) > 1 {
		prev := runs[len(runs)-2]
		at := prev.at
		cell.Previous, cell.PreviousText, cell.PreviousAt = prev.num, prev.text, &at
	}
	start := len(runs) - maxFieldHistory
	if start < 0 {
		start = 0
	}
	for _, r := range runs[start:] {
		cell.History = append(cell.History, FieldPoint{Num: r.num, Text: r.text, ObservedAt: r.at})
	}
	// Pin and exclude act on the page the cell is showing.
	for _, mem := range members {
		if cell.MemberID == 0 || mem.URL == cell.URL {
			cell.MemberID, cell.Pinned = mem.ID, mem.Pinned
		}
		if mem.URL == cell.URL {
			break
		}
	}
	return cell
}

// runValue is what one site recorded for one product in one run, after the
// pages of that site are reconciled into a single value.
type runValue struct {
	at        time.Time
	num       *float64
	low, high *float64
	text      string
	currency  string
	url       string
	pages     int
	alsoURLs  []string
}

// groupObservationsByRun folds observations (already ordered oldest first)
// into one value per crawl run, oldest run first. A numeric field takes the
// lowest value the site offered in that run — the cheapest of several
// listings is the one a buyer would take — and records the spread. A text
// field takes the newest observation of the run.
func groupObservationsByRun(obs []models.FieldObservation) []runValue {
	order := []string{}
	byRun := map[string][]models.FieldObservation{}
	for _, o := range obs {
		if _, seen := byRun[o.CrawlJobID]; !seen {
			order = append(order, o.CrawlJobID)
		}
		byRun[o.CrawlJobID] = append(byRun[o.CrawlJobID], o)
	}
	out := make([]runValue, 0, len(order))
	for _, jobID := range order {
		group := byRun[jobID]
		currency := modalCurrency(group)
		rv := runValue{at: group[0].ObservedAt, currency: currency, pages: len(group)}
		for _, o := range group {
			if o.ObservedAt.After(rv.at) {
				rv.at = o.ObservedAt
			}
			if o.NumValue == nil {
				continue
			}
			if currency != "" && o.Currency != "" && o.Currency != currency {
				continue // a value in another currency is not comparable with these
			}
			v := *o.NumValue
			if rv.low == nil || v < *rv.low {
				low := v
				rv.low, rv.num, rv.url = &low, &low, o.URL
			}
			if rv.high == nil || v > *rv.high {
				high := v
				rv.high = &high
			}
		}
		if rv.num == nil {
			// A text field (or a run with no numbers): the newest wins.
			newest := group[0]
			for _, o := range group {
				if o.ObservedAt.After(newest.ObservedAt) || (o.ObservedAt.Equal(newest.ObservedAt) && o.ID > newest.ID) {
					newest = o
				}
			}
			rv.text, rv.url, rv.currency = newest.TextValue, newest.URL, newest.Currency
		}
		for _, o := range group {
			if o.URL != rv.url && len(rv.alsoURLs) < 20 {
				rv.alsoURLs = append(rv.alsoURLs, o.URL)
			}
		}
		out = append(out, rv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

// modalCurrency is the currency most of a run's observations are in; ties
// go to the alphabetically first, so the choice is stable between reads.
func modalCurrency(obs []models.FieldObservation) string {
	counts := map[string]int{}
	for _, o := range obs {
		if o.Currency != "" {
			counts[o.Currency]++
		}
	}
	best, bestN := "", 0
	for cur, n := range counts {
		if n > bestN || (n == bestN && cur < best) {
			best, bestN = cur, n
		}
	}
	return best
}

// latestRunPerSite is when each site of a scope last recorded the field at
// all, which is how a cell left behind by the latest run is recognised.
func (m *Manager) latestRunPerSite(scopeType, scopeID, field string) map[string]time.Time {
	var rows []struct {
		SiteKey string
		LastAt  time.Time
	}
	m.db.Model(&models.FieldObservation{}).
		Select("site_key AS site_key, MAX(observed_at) AS last_at").
		Where("scope_type = ? AND scope_id = ? AND field = ?", scopeType, scopeID, field).
		Group("site_key").Scan(&rows)
	out := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		out[r.SiteKey] = r.LastAt
	}
	return out
}

// siteColumns builds the table's columns: the sites the scope's crawl
// actually produced members for, busiest first.
func (m *Manager) siteColumns(plan groupPlan, counts map[string]int, labels map[string]string) []GroupSiteStatus {
	var out []GroupSiteStatus
	sources, _, _ := m.sourceJobs(plan.scope, true)
	jobID, status := "", ""
	var completedAt *time.Time
	if len(sources) > 0 {
		jobID = sources[0].jobID
		var job models.CrawlJob
		if err := m.db.First(&job, "id = ?", jobID).Error; err == nil {
			status, completedAt = string(job.Status), job.CompletedAt
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		label := labels[k]
		if label == "" {
			label = k
		}
		out = append(out, GroupSiteStatus{SiteKey: k, Label: label, ProductCount: counts[k], LatestJobID: jobID, Status: status, CompletedAt: completedAt})
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ── member actions ────────────────────────────────────────────────────

// PinGroupMember marks a member as a confirmed match: future regroups keep
// it in this group even if automatic matching would place it elsewhere.
func (m *Manager) PinGroupMember(memberID uint) error {
	return m.updateMember(memberID, map[string]interface{}{"pinned": true})
}

// UnpinGroupMember lets automatic matching place the member again.
func (m *Manager) UnpinGroupMember(memberID uint) error {
	return m.updateMember(memberID, map[string]interface{}{"pinned": false})
}

// ExcludeGroupMember marks a page as not this product: it leaves the
// comparison table and future regroups never place it in any group of this
// scope again. The row is kept (not deleted) so the decision is
// remembered; UnexcludeGroupMember reverses it.
func (m *Manager) ExcludeGroupMember(memberID uint) error {
	return m.updateMember(memberID, map[string]interface{}{"excluded": true, "pinned": false})
}

// UnexcludeGroupMember makes the page eligible for matching again.
func (m *Manager) UnexcludeGroupMember(memberID uint) error {
	return m.updateMember(memberID, map[string]interface{}{"excluded": false})
}

func (m *Manager) updateMember(memberID uint, updates map[string]interface{}) error {
	res := m.db.Model(&models.ProductGroupMember{}).Where("id = ?", memberID).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveGroupMember reassigns a member to another product group of the same
// scope by hand and pins it there.
func (m *Manager) MoveGroupMember(memberID uint, targetProductGroupID string) error {
	var mem models.ProductGroupMember
	if err := m.db.First(&mem, "id = ?", memberID).Error; err != nil {
		return notFound(err)
	}
	var target models.ProductGroup
	if err := m.db.First(&target, "id = ? AND scope_type = ? AND scope_id = ?", targetProductGroupID, mem.ScopeType, mem.ScopeID).Error; err != nil {
		return notFound(err)
	}
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&mem).Updates(map[string]interface{}{
			"product_group_id": targetProductGroupID, "pinned": true, "match_method": models.MatchByManual, "confidence": 1,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.FieldObservation{}).
			Where("scope_type = ? AND scope_id = ? AND site_key = ? AND url = ? AND variant_key = ?", mem.ScopeType, mem.ScopeID, mem.SiteKey, mem.URL, mem.VariantKey).
			Update("product_group_id", targetProductGroupID).Error; err != nil {
			return err
		}
		plan, err := m.planFor(groupScope{Type: mem.ScopeType, ID: mem.ScopeID})
		if err != nil {
			return err
		}
		return m.refreshProductGroupStats(tx, targetProductGroupID, plan.primaryField())
	})
}

// CreateManualProductGroup starts a new, empty product group in a scope,
// for members to be moved into by hand.
func (m *Manager) CreateManualProductGroup(scopeType, scopeID, label, brand string) (*models.ProductGroup, error) {
	if !models.ValidGroupingScope(scopeType) {
		return nil, invalidInput("unknown grouping scope %q", scopeType)
	}
	if _, err := m.planFor(groupScope{Type: scopeType, ID: scopeID}); err != nil {
		return nil, err
	}
	pg := &models.ProductGroup{
		ScopeType: scopeType, ScopeID: scopeID, Label: label, Brand: brand,
		MatchMethod: models.MatchByManual, Confidence: 1,
	}
	if err := m.db.Create(pg).Error; err != nil {
		return nil, err
	}
	return pg, nil
}

// AssignUnmatchedToGroup places a page the latest regroup could not match
// into a product group by hand: it creates a pinned member for it (so
// future regroups keep it there) and attaches the field history recorded
// while it was unmatched.
func (m *Manager) AssignUnmatchedToGroup(scopeType, scopeID, targetProductGroupID, crawlJobID string, pageID uint, siteKey, siteLabel, url, variantKey string) error {
	var target models.ProductGroup
	if err := m.db.First(&target, "id = ? AND scope_type = ? AND scope_id = ?", targetProductGroupID, scopeType, scopeID).Error; err != nil {
		return notFound(err)
	}
	plan, err := m.planFor(groupScope{Type: scopeType, ID: scopeID})
	if err != nil {
		return err
	}
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("scope_type = ? AND scope_id = ? AND site_key = ? AND url = ? AND variant_key = ?",
			scopeType, scopeID, siteKey, url, variantKey).Delete(&models.ProductGroupMember{}).Error; err != nil {
			return err
		}
		member := models.ProductGroupMember{
			ProductGroupID: targetProductGroupID, ScopeType: scopeType, ScopeID: scopeID,
			SiteKey: siteKey, SiteLabel: siteLabel, CrawlJobID: crawlJobID, PageID: pageID,
			URL: url, VariantKey: variantKey,
			MatchMethod: models.MatchByManual, Confidence: 1, Pinned: true,
		}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.FieldObservation{}).
			Where("scope_type = ? AND scope_id = ? AND site_key = ? AND url = ? AND variant_key = ? AND product_group_id = ?",
				scopeType, scopeID, siteKey, url, variantKey, "").
			Update("product_group_id", targetProductGroupID).Error; err != nil {
			return err
		}
		return m.refreshProductGroupStats(tx, targetProductGroupID, plan.primaryField())
	})
}
