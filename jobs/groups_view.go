package jobs

import (
	"sort"
	"time"

	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/models"
)

// maxFieldHistory bounds how many recent points the table keeps per
// listing for its sparkline. The full series — including the days that raw
// runs have been compacted into — is read per listing by
// GetListingHistory, which is what the history view shows.
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
	ListingCount int        `json:"listing_count"`
}

// CellListing is one shop's individual offer of a product: one Listing,
// with its own value, its own movement since the previous run and its own
// sparkline. A shop with several offers of one product (a marketplace with
// two sellers, a bundle beside the bare item) contributes several of these
// to one cell, each tracked separately — which is the point of giving a
// listing its own identity rather than averaging the shop into one number.
type CellListing struct {
	ListingID    uint           `json:"listing_id"`
	URL          string         `json:"url"`
	Title        string         `json:"title,omitempty"`
	Num          *float64       `json:"num,omitempty"`
	Text         string         `json:"text,omitempty"`
	Currency     string         `json:"currency,omitempty"`
	ObservedAt   time.Time      `json:"observed_at"`
	Previous     *float64       `json:"previous,omitempty"`
	PreviousText string         `json:"previous_text,omitempty"`
	Runs         int            `json:"runs"`
	Pinned       bool           `json:"pinned,omitempty"`
	Stale        bool           `json:"stale,omitempty"`
	KeyMethod    string         `json:"key_method,omitempty"`
	KeyStable    bool           `json:"key_stable"`
	KeyNote      string         `json:"key_note,omitempty"`
	History      []HistoryPoint `json:"history,omitempty"`
}

// Changed reports whether this offer's value differs from its previous run.
func (c CellListing) Changed() bool {
	return c.Num != nil && c.Previous != nil && *c.Num != *c.Previous
}

// Delta is the movement since the previous run (0 when unchanged).
func (c CellListing) Delta() float64 {
	if !c.Changed() {
		return 0
	}
	return *c.Num - *c.Previous
}

// TextChanged reports whether a text field moved — "out of stock" becoming
// "in stock" is the change a numeric delta cannot show.
func (c CellListing) TextChanged() bool {
	return c.Num == nil && c.Text != "" && c.PreviousText != "" && c.Text != c.PreviousText
}

// FieldCell is one shop's column for one product: the shop's cheapest live
// offer, plus every offer it has so the cell can be opened up. Low/High
// bound what the shop's own offers disagree by.
type FieldCell struct {
	ListingID    uint           `json:"listing_id,omitempty"` // the offer the headline value came from
	Num          *float64       `json:"num,omitempty"`
	Text         string         `json:"text,omitempty"`
	Currency     string         `json:"currency,omitempty"`
	URL          string         `json:"url"`
	Title        string         `json:"title,omitempty"`
	ObservedAt   time.Time      `json:"observed_at"`
	Previous     *float64       `json:"previous,omitempty"`
	PreviousText string         `json:"previous_text,omitempty"`
	PreviousAt   *time.Time     `json:"previous_at,omitempty"`
	Runs         int            `json:"runs,omitempty"`
	Pinned       bool           `json:"pinned,omitempty"`
	Stale        bool           `json:"stale,omitempty"` // every offer this shop had is gone
	KeyStable    bool           `json:"key_stable"`
	Low          *float64       `json:"low,omitempty"` // spread across this shop's offers
	High         *float64       `json:"high,omitempty"`
	History      []HistoryPoint `json:"history,omitempty"`
	Listings     []CellListing  `json:"listings,omitempty"`
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

// TextChanged reports whether a text field differs from the previous run's.
func (c FieldCell) TextChanged() bool {
	return c.Num == nil && c.Text != "" && c.PreviousText != "" && c.Text != c.PreviousText
}

// Offers is how many offers this shop has for the product.
func (c FieldCell) Offers() int { return len(c.Listings) }

// Disagrees reports whether the shop's own offers quote different values.
// That is ordinary for a marketplace, and a hint of a bad match otherwise.
func (c FieldCell) Disagrees() bool {
	return c.Low != nil && c.High != nil && *c.High > *c.Low
}

// ProductRow is one ProductGroup with its listings and the selected
// field's current value at every shop that carries it.
type ProductRow struct {
	models.ProductGroup
	Listings []models.Listing      `json:"listings"`
	Cells    map[string]*FieldCell `json:"cells"` // keyed by site key
}

// UnmatchedListing is a listing no group holds yet: found at one shop with
// no confident match elsewhere. It is tracked all the same — it has an
// identity and a history already — and can be assigned to a product by
// hand, which moves that history with it.
type UnmatchedListing struct {
	models.Listing
	Num        *float64  `json:"num,omitempty"`
	Text       string    `json:"text,omitempty"`
	Currency   string    `json:"currency,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// GroupingDashboard is everything a comparison table shows, for any scope.
// Rows are one page of the scope's groups; TotalGroups is how many there
// are in all.
type GroupingDashboard struct {
	ScopeType      string             `json:"scope_type"`
	ScopeID        string             `json:"scope_id"`
	Fields         []string           `json:"fields"` // the scope's compared fields
	Field          string             `json:"field"`  // the one this table shows
	FieldKind      string             `json:"field_kind"`
	PrimaryField   string             `json:"primary_field"`
	Sites          []GroupSiteStatus  `json:"sites"`
	Rows           []ProductRow       `json:"rows"`
	Unmatched      []UnmatchedListing `json:"unmatched"`
	LastObservedAt *time.Time         `json:"last_observed_at,omitempty"`

	// Tracks says whether this scope accumulates history. A schedule
	// reruns, so every listing has a series and the table shows movement
	// and sparklines; a one-off crawl has a single run, so there is
	// nothing to track and the tracking affordances are hidden rather
	// than shown empty.
	Tracks bool `json:"tracks"`

	TotalGroups   int    `json:"total_groups"`
	TotalListings int    `json:"total_listings"`
	Page          int    `json:"page"`
	PerPage       int    `json:"per_page"`
	TotalPages    int    `json:"total_pages"`
	BasePath      string `json:"-"` // where the table lives, for its paging links
	Anchor        string `json:"-"` // the tab to come back to
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
		Tracks: models.ScopeTracks(scopeType),
		Page:   page, PerPage: perPage,
	}
	if dash.FieldKind == "" {
		dash.FieldKind = content.FieldKindText // an extract field: compared as whatever it parsed to
	}

	var allListings []models.Listing
	if err := m.db.Where("scope_type = ? AND scope_id = ?", scopeType, scopeID).Find(&allListings).Error; err != nil {
		return nil, err
	}
	counts := map[string]int{}
	labels := map[string]string{}
	byGroup := map[string][]models.Listing{}
	var ungrouped []models.Listing
	for _, l := range allListings {
		if l.SiteLabel != "" {
			labels[l.SiteKey] = l.SiteLabel
		}
		if l.Excluded {
			continue
		}
		// A stale listing still renders (greyed, with its history and its
		// pin/exclude buttons), but it is not counted as something the
		// shop currently carries.
		if !l.Stale {
			counts[l.SiteKey]++
			dash.TotalListings++
		}
		if l.ProductGroupID == "" {
			ungrouped = append(ungrouped, l)
			continue
		}
		byGroup[l.ProductGroupID] = append(byGroup[l.ProductGroupID], l)
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
		// Every listing on this page of the table, so their series load in
		// one query rather than one per cell.
		var ids []uint
		for _, g := range groups {
			for _, l := range byGroup[g.ID] {
				ids = append(ids, l.ID)
			}
		}
		series, err := m.loadSeries(ids, field)
		if err != nil {
			return nil, err
		}
		for _, points := range series {
			if len(points) == 0 {
				continue
			}
			at := points[len(points)-1].ObservedAt
			if dash.LastObservedAt == nil || at.After(*dash.LastObservedAt) {
				dash.LastObservedAt = &at
			}
		}
		for _, g := range groups {
			row := ProductRow{ProductGroup: g, Listings: byGroup[g.ID], Cells: map[string]*FieldCell{}}
			bySite := map[string][]models.Listing{}
			for _, l := range row.Listings {
				bySite[l.SiteKey] = append(bySite[l.SiteKey], l)
			}
			for site, siteListings := range bySite {
				if cell := buildCell(siteListings, series); cell != nil {
					row.Cells[site] = cell
				}
			}
			dash.Rows = append(dash.Rows, row)
		}
	}

	if len(ungrouped) > 0 {
		ids := make([]uint, len(ungrouped))
		for i, l := range ungrouped {
			ids[i] = l.ID
		}
		series, err := m.loadSeries(ids, field)
		if err != nil {
			return nil, err
		}
		for _, l := range ungrouped {
			u := UnmatchedListing{Listing: l, ObservedAt: l.LastSeenAt}
			if points := series[l.ID]; len(points) > 0 {
				last := points[len(points)-1]
				u.Num, u.Text, u.Currency, u.ObservedAt = last.NumValue, last.TextValue, last.Currency, last.ObservedAt
			}
			dash.Unmatched = append(dash.Unmatched, u)
		}
		sort.Slice(dash.Unmatched, func(i, j int) bool {
			return dash.Unmatched[i].ObservedAt.After(dash.Unmatched[j].ObservedAt)
		})
	}
	return dash, nil
}

// loadSeries reads the recent raw observations of one field for a set of
// listings, oldest first, bucketed by listing.
//
// Only raw rows are read: the table's sparkline shows recent movement, and
// the full span — including compacted days — is what the per-listing
// history view assembles.
func (m *Manager) loadSeries(listingIDs []uint, field string) (map[uint][]models.FieldObservation, error) {
	out := map[uint][]models.FieldObservation{}
	if len(listingIDs) == 0 {
		return out, nil
	}
	var obs []models.FieldObservation
	if err := m.db.Where("listing_id IN ? AND field = ?", listingIDs, field).
		Order("observed_at ASC, id ASC").Find(&obs).Error; err != nil {
		return nil, err
	}
	for _, o := range obs {
		out[o.ListingID] = append(out[o.ListingID], o)
	}
	return out, nil
}

// buildCell turns one shop's listings of one product into the table's
// cell: every offer the shop has, and as the headline the cheapest live
// one — the offer a buyer would actually take.
//
// Each offer's own value is the last point of its own series, so "previous"
// is genuinely that offer's previous run and never, as a page-by-page
// reading would have it, a different offer at the same shop.
func buildCell(listings []models.Listing, series map[uint][]models.FieldObservation) *FieldCell {
	cell := &FieldCell{}
	for _, l := range listings {
		points := series[l.ID]
		if len(points) == 0 {
			continue
		}
		latest := points[len(points)-1]
		cl := CellListing{
			ListingID: l.ID, URL: l.URL, Title: l.Title,
			Num: latest.NumValue, Text: latest.TextValue, Currency: latest.Currency,
			ObservedAt: latest.ObservedAt, Runs: l.Runs,
			Pinned: l.Pinned, Stale: l.Stale,
			KeyMethod: l.KeyMethod, KeyStable: models.StableListingKey(l.KeyMethod),
			KeyNote: describeListingKey(l.KeyMethod),
		}
		if cl.URL == "" {
			cl.URL = latest.URL
		}
		if len(points) > 1 {
			prev := points[len(points)-2]
			cl.Previous, cl.PreviousText = prev.NumValue, prev.TextValue
		}
		start := len(points) - maxFieldHistory
		if start < 0 {
			start = 0
		}
		for _, o := range points[start:] {
			cl.History = append(cl.History, HistoryPoint{
				At: o.ObservedAt, Num: o.NumValue, Text: o.TextValue, Currency: o.Currency,
			})
		}
		cell.Listings = append(cell.Listings, cl)
	}
	if len(cell.Listings) == 0 {
		return nil
	}
	// A stable order, so the cell reads the same way on every reload.
	sort.Slice(cell.Listings, func(a, b int) bool {
		x, y := cell.Listings[a], cell.Listings[b]
		if x.Stale != y.Stale {
			return !x.Stale // live offers first
		}
		if x.Num != nil && y.Num != nil && *x.Num != *y.Num {
			return *x.Num < *y.Num // cheapest first
		}
		if x.ObservedAt != y.ObservedAt {
			return x.ObservedAt.After(y.ObservedAt)
		}
		return x.ListingID < y.ListingID
	})
	promoteHeadline(cell)
	return cell
}

// promoteHeadline picks the offer the cell leads with and the spread its
// other offers disagree by. Offers quoting a currency most of the shop's
// offers do not use are left out of the comparison rather than ranked as
// bare numbers against it.
func promoteHeadline(cell *FieldCell) {
	currencies := make([]string, 0, len(cell.Listings))
	for _, cl := range cell.Listings {
		if !cl.Stale {
			currencies = append(currencies, cl.Currency)
		}
	}
	currency := modalCurrency(currencies)

	var best *CellListing
	for i := range cell.Listings {
		cl := &cell.Listings[i]
		if cl.Stale || cl.Num == nil {
			continue
		}
		if currency != "" && cl.Currency != "" && cl.Currency != currency {
			continue
		}
		if cell.Low == nil || *cl.Num < *cell.Low {
			v := *cl.Num
			cell.Low = &v
		}
		if cell.High == nil || *cl.Num > *cell.High {
			v := *cl.Num
			cell.High = &v
		}
		if best == nil || *cl.Num < *best.Num {
			best = cl
		}
	}
	if best == nil {
		// No live numeric offer: lead with the newest live offer (a text
		// field such as availability), or — if every offer is gone — the
		// newest of those, marked stale so the table says so.
		for i := range cell.Listings {
			cl := &cell.Listings[i]
			if cl.Stale {
				continue
			}
			if best == nil || cl.ObservedAt.After(best.ObservedAt) {
				best = cl
			}
		}
	}
	if best == nil {
		cell.Stale = true
		for i := range cell.Listings {
			cl := &cell.Listings[i]
			if best == nil || cl.ObservedAt.After(best.ObservedAt) {
				best = cl
			}
		}
	}
	cell.ListingID, cell.URL, cell.Title = best.ListingID, best.URL, best.Title
	cell.Num, cell.Text, cell.Currency = best.Num, best.Text, best.Currency
	cell.ObservedAt, cell.Runs, cell.Pinned = best.ObservedAt, best.Runs, best.Pinned
	cell.KeyStable = best.KeyStable
	cell.Previous, cell.PreviousText = best.Previous, best.PreviousText
	cell.History = best.History
	if len(best.History) > 1 {
		at := best.History[len(best.History)-2].At
		cell.PreviousAt = &at
	}
}

// modalCurrency is the currency most of a shop's offers quote; ties break
// alphabetically so the choice is stable.
func modalCurrency(currencies []string) string {
	counts := map[string]int{}
	for _, c := range currencies {
		if c != "" {
			counts[c]++
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

// siteColumns builds the table's columns: the shops the scope's crawl
// actually produced listings for.
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
		out = append(out, GroupSiteStatus{
			SiteKey: k, Label: label, ListingCount: counts[k],
			LatestJobID: jobID, Status: status, CompletedAt: completedAt,
		})
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

// CreateManualProductGroup starts a new, empty product group in a scope,
// for listings to be moved into by hand.
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
