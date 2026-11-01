package jobs

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/database"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"gorm.io/gorm"
)

// builtinStopWords are marketing/filler words dropped before fuzzy name
// matching; a JobGroup's config.Grouping.ExtraStopWords adds to this list.
var builtinStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "new": true, "original": true, "genuine": true,
	"free": true, "shipping": true, "sale": true, "off": true, "best": true, "top": true,
	"pack": true, "set": true, "with": true, "for": true, "and": true,
	"اصل": true, "اورجینال": true, "گارانتی": true, "ارسال": true, "رایگان": true, "جدید": true,
	"فروش": true, "ویژه": true, "تخفیف": true, "پک": true, "با": true, "برای": true, "و": true,
	"اصلی": true, "گارانتی‌دار": true, "خرید": true,
}

var tokenSplitRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// nameTokens normalizes and tokenizes a product name for fuzzy matching:
// Persian letter forms unified, lower-cased, split on non-alphanumerics,
// marketing stop words and the brand's own words dropped (so a brand
// written differently in the title doesn't bias the score).
func nameTokens(name, brand string, extraStop []string) []string {
	brandWords := map[string]bool{}
	for _, w := range tokenSplitRe.Split(normKey(brand), -1) {
		if w != "" {
			brandWords[w] = true
		}
	}
	stop := builtinStopWords
	if len(extraStop) > 0 {
		stop = make(map[string]bool, len(builtinStopWords)+len(extraStop))
		for k := range builtinStopWords {
			stop[k] = true
		}
		for _, w := range extraStop {
			if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
				stop[w] = true
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, w := range tokenSplitRe.Split(normKey(name), -1) {
		if w == "" || stop[w] || brandWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// jaccard is the token-set similarity of two already-deduplicated token lists.
func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	bs := make(map[string]bool, len(b))
	for _, t := range b {
		bs[t] = true
	}
	inter := 0
	for _, t := range a {
		if bs[t] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// normKey lower-cases and unifies Persian letter forms, for keys and tokens
// that must compare equal however a site capitalises or spells them.
func normKey(s string) string {
	return strings.ToLower(strings.TrimSpace(modules.NormalizePersian(s)))
}

// ── union-find over candidate rows and existing product groups ──────────

// matchSet unions candidate row indices with each other and with existing
// ProductGroup ids ("existing:<id>" nodes), so a row that matches an
// existing group by any route ends up in its component.
type matchSet struct {
	parent map[string]string
	info   map[int]matchInfo // best justification found for each row index
}

type matchInfo struct {
	method     string
	confidence float64
}

func newMatchSet(n int) *matchSet {
	ms := &matchSet{parent: make(map[string]string, n*2), info: make(map[int]matchInfo, n)}
	for i := 0; i < n; i++ {
		ms.parent[rowNode(i)] = rowNode(i)
	}
	return ms
}

func rowNode(i int) string          { return fmt.Sprintf("row:%d", i) }
func existingNode(id string) string { return "existing:" + id }

func (ms *matchSet) find(x string) string {
	if _, ok := ms.parent[x]; !ok {
		ms.parent[x] = x
	}
	for ms.parent[x] != x {
		x = ms.parent[x]
	}
	return x
}

func (ms *matchSet) union(a, b string) {
	ra, rb := ms.find(a), ms.find(b)
	if ra == rb {
		return
	}
	// Existing-group nodes win as the root, so a component's identity
	// resolves to a real group id whenever one is present.
	if strings.HasPrefix(rb, "existing:") && !strings.HasPrefix(ra, "existing:") {
		ra, rb = rb, ra
	}
	ms.parent[rb] = ra
}

func (ms *matchSet) note(row int, method string, confidence float64) {
	if cur, ok := ms.info[row]; !ok || confidence > cur.confidence {
		ms.info[row] = matchInfo{method, confidence}
	}
}

// pageSource is the page a candidate was read from, and how its site is
// labelled in the comparison table.
type pageSource struct {
	jobID, siteKey, siteLabel, url string
	pageID                         uint
}

// candidate is one product, or (with variant grouping) one of its variants,
// found on one site's latest finished crawl, ready to be matched. fields
// holds the compared values read off the page, keyed by field name.
type candidate struct {
	jobID, siteKey, siteLabel, url string
	pageID                         uint
	name, brand, gtin, mpn, sku    string
	variantKey, variantLabel       string
	fields                         map[string]content.FieldValue
	tokens                         []string
	// Listing identity: which offer at this shop this row is, independent
	// of the URL it was found at. fallbackHashes are the weaker identities
	// the same offer would have been filed under before this page carried
	// a strong id, so an existing listing can be adopted and upgraded.
	listingKey, listingHash, keyMethod string
	fallbackHashes                     []string
}

// listingIdentity derives this row's listing key and hash. Called once per
// row per pass, before matching, because a listing's stored pinned /
// excluded decision and its current group are inputs to matching.
func (c *candidate) listingIdentity() {
	primary := listingKeyFor(c.siteKey, c.url, c.sku, c.gtin, c.brand, c.mpn, c.variantKey)
	c.listingKey, c.keyMethod = primary.key, primary.method
	c.listingHash = listingHash(primary.key)
	c.fallbackHashes = nil
	for _, alt := range listingFallbackKeys(c.siteKey, c.url, c.variantKey) {
		if h := listingHash(alt.key); h != c.listingHash {
			c.fallbackHashes = append(c.fallbackHashes, h)
		}
	}
}

// exactKey is the strongest stable identity a candidate carries: a GTIN
// barcode, else brand+MPN. Empty when neither is present, meaning only
// fuzzy name matching can place this candidate.
func (c *candidate) exactKey() (key, method string) {
	if g := normKey(c.gtin); g != "" {
		return "gtin:" + g, models.MatchByGTIN
	}
	if b, p := normKey(c.brand), normKey(c.mpn); b != "" && p != "" {
		return "mpn:" + b + "|" + p, models.MatchByMPN
	}
	return "", ""
}

// title is what the dashboard shows for this row before it is grouped.
func (c *candidate) title() string {
	return strings.TrimSpace(c.name + " " + c.variantLabel)
}

// candidatesFromProduct expands one page's detected product into the rows
// grouping compares: one row for the base product, or one row per variant
// when the plan groups by variant and the page lists variants.
func candidatesFromProduct(src pageSource, p *content.Product, extracted []byte, plan groupPlan) []candidate {
	base := candidate{
		jobID: src.jobID, siteKey: src.siteKey, siteLabel: src.siteLabel, url: src.url, pageID: src.pageID,
		name: p.Name, brand: p.Brand, gtin: p.GTIN, mpn: p.MPN, sku: p.SKU,
	}
	if !plan.byVariant || len(p.Variants) == 0 {
		base.tokens = nameTokens(base.name, base.brand, plan.extraStop)
		base.fields = compareValues(p, nil, extracted, plan.compareFields)
		return []candidate{base}
	}
	out := make([]candidate, 0, len(p.Variants))
	for i := range p.Variants {
		v := &p.Variants[i]
		c := base
		c.variantLabel = v.Name
		attrs := v.Attributes
		if attrs == nil {
			attrs = content.ParseVariantAttributes(v.Name)
		}
		c.variantKey = content.VariantKey(attrs)
		switch {
		case v.GTIN != "":
			c.gtin, c.mpn = v.GTIN, "" // the variant's own barcode identifies it precisely
		case v.MPN != "":
			c.mpn = v.MPN
		}
		// A variant's own SKU identifies that variant's listing; without
		// one, the page's SKU plus the variant key still does, since the
		// key is suffixed with the variant.
		if v.SKU != "" {
			c.sku = v.SKU
		}
		c.tokens = nameTokens(strings.TrimSpace(base.name+" "+v.Name), base.brand, plan.extraStop)
		c.fields = compareValues(p, v, extracted, plan.compareFields)
		out = append(out, c)
	}
	return out
}

// compareValues reads the planned fields off a product (and, for the fields
// a variant carries of its own, off the variant), falling back to the
// page's extracted JSON for a field the detectors do not know — that is how
// a job's own extract_fields become comparable.
func compareValues(p *content.Product, v *content.ProductVariant, extracted []byte, fields []string) map[string]content.FieldValue {
	out := make(map[string]content.FieldValue, len(fields))
	for _, f := range fields {
		if val, ok := content.ProductFieldValue(p, v, f); ok {
			out[f] = val
			continue
		}
		if val, ok := content.ExtractedFieldValue(extracted, f); ok {
			out[f] = val
		}
	}
	return out
}

// groupScope names what is being grouped: every run of a schedule, or one
// one-off crawl. See models.ScopeSchedule.
type groupScope struct {
	Type string
	ID   string
}

// groupPlan is the matching configuration resolved for one scope.
type groupPlan struct {
	scope         groupScope
	matchMode     string
	threshold     float64
	byVariant     bool
	compareFields []string
	extraStop     []string
}

// primaryField is the field the table ranks by — the cheapest cell and a
// group's low/high summary: price when it is compared, else the first
// compared field.
func (p groupPlan) primaryField() string {
	for _, f := range p.compareFields {
		if f == "price" {
			return f
		}
	}
	if len(p.compareFields) > 0 {
		return p.compareFields[0]
	}
	return "price"
}

// planFor resolves a scope's matching configuration: the engine defaults
// from config.Grouping, overridden by the comparison's own fields or by the
// grouping settings of the schedule's/crawl's JobSettings.
func (m *Manager) planFor(scope groupScope) (groupPlan, error) {
	g := m.config.Grouping
	plan := groupPlan{
		scope:         scope,
		matchMode:     g.DefaultMatchMode,
		threshold:     g.DefaultThreshold,
		byVariant:     g.DefaultGroupByVariant,
		compareFields: content.DefaultCompareFields,
		extraStop:     g.ExtraStopWords,
	}
	if len(g.DefaultCompareFields) > 0 {
		plan.compareFields = g.DefaultCompareFields
	}
	if plan.matchMode == "" {
		plan.matchMode = models.MatchModeAuto
	}
	if plan.threshold <= 0 {
		plan.threshold = 0.72
	}
	switch scope.Type {
	case models.ScopeSchedule:
		s, err := m.GetSchedule(scope.ID)
		if err != nil {
			return plan, err
		}
		applyGroupingSettings(&plan, s.Template.Settings)
	case models.ScopeCrawl:
		job, err := m.GetJob(scope.ID)
		if err != nil {
			return plan, err
		}
		applyGroupingSettings(&plan, job.Settings)
	default:
		return plan, invalidInput("unknown grouping scope %q", scope.Type)
	}
	return plan, nil
}

// applyGroupingSettings layers a crawl's or schedule's grouping settings
// over the engine defaults.
func applyGroupingSettings(plan *groupPlan, s *models.JobSettings) {
	if s == nil {
		return
	}
	if len(s.CompareFields) > 0 {
		plan.compareFields = s.CompareFields
	}
	if s.GroupByVariant != nil {
		plan.byVariant = *s.GroupByVariant
	}
	if s.MatchThreshold != nil && *s.MatchThreshold > 0 {
		plan.threshold = *s.MatchThreshold
	}
	if s.MatchMode != nil && *s.MatchMode != "" {
		plan.matchMode = *s.MatchMode
	}
}

// ScopeForJob returns the grouping scope a finished crawl belongs to, and
// whether it is grouped at all: a crawl needs group_products in its
// settings. A scheduled crawl is
// scoped to its schedule, not to the single run, so that groups and their
// field history survive each rerun.
func ScopeForJob(job *models.CrawlJob) (scopeType, scopeID string, grouped bool) {
	if job.Settings == nil || job.Settings.GroupProducts == nil || !*job.Settings.GroupProducts {
		return "", "", false
	}
	if job.ScheduleID != nil && *job.ScheduleID != "" {
		return models.ScopeSchedule, *job.ScheduleID, true
	}
	return models.ScopeCrawl, job.ID, true
}

// ResolveGroupingScope accepts either a schedule id or a crawl id and
// returns the grouping scope it belongs to, so a caller can name whichever
// it has to hand. A scheduled crawl resolves to its schedule, whose table
// spans every run.
//
// The lookups use Find rather than First: an id that is not a schedule is
// the ordinary case here, and First would log every one as an error.
func (m *Manager) ResolveGroupingScope(id string) (scopeType, scopeID string, err error) {
	var schedules []models.Schedule
	if err := m.db.Select("id").Where("id = ?", id).Limit(1).Find(&schedules).Error; err != nil {
		return "", "", err
	}
	if len(schedules) > 0 {
		return models.ScopeSchedule, id, nil
	}
	var crawls []models.CrawlJob
	if err := m.db.Select("id", "settings", "schedule_id").Where("id = ?", id).Limit(1).Find(&crawls).Error; err != nil {
		return "", "", err
	}
	if len(crawls) == 0 {
		return "", "", fmt.Errorf("%w: no schedule or crawl with id %s", ErrNotFound, id)
	}
	st, sid, grouped := ScopeForJob(&crawls[0])
	if !grouped {
		return "", "", invalidState(`this crawl does not group its products; set "group_products" in its settings`)
	}
	return st, sid, nil
}

// Regroup matches a scope's latest finished crawl(s) into ProductGroups and
// records a FieldObservation per compared field. Safe to call repeatedly:
// existing groups are found again by CanonicalKey and reused, not
// recreated, and pinned/excluded member decisions are honoured.
func (m *Manager) Regroup(scopeType, scopeID string, opts RegroupOptions) (RegroupResult, error) {
	var res RegroupResult
	if !models.ValidGroupingScope(scopeType) {
		return res, invalidInput("unknown grouping scope %q", scopeType)
	}
	// One pass per scope at a time. Two sites of a comparison finishing in
	// the same instant, or a re-match pressed while the automatic pass is
	// running, would otherwise each see no existing group and create their
	// own copy of it.
	acquired, err := database.WithLock(m.db, scopeLockName(scopeType, scopeID), regroupLockWait, func() error {
		var err error
		res, err = m.regroupLocked(scopeType, scopeID, opts)
		return err
	})
	if err != nil {
		return res, err
	}
	if !acquired {
		res.Locked = true
		res.Reason = "matching is already running for this crawl; its result will be here in a moment"
	}
	return res, nil
}

// regroupLockWait is how long a pass waits for another pass on the same
// scope before giving up and leaving the work to it.
const regroupLockWait = 10 * time.Second

func (m *Manager) regroupLocked(scopeType, scopeID string, opts RegroupOptions) (RegroupResult, error) {
	var res RegroupResult
	scope := groupScope{Type: scopeType, ID: scopeID}
	plan, err := m.planFor(scope)
	if err != nil {
		return res, err
	}
	res.Fields = plan.compareFields
	sources, reason, err := m.sourceJobs(scope, opts.Partial)
	if err != nil {
		return res, err
	}
	res.Sources = len(sources)
	if len(sources) == 0 {
		res.Reason = reason
		return res, nil
	}
	rows, stats, err := m.loadCandidates(plan, sources)
	if err != nil {
		return res, err
	}
	res.ProductPages, res.Candidates = stats.productPages, len(rows)
	if len(rows) == 0 {
		res.Reason = noCandidateReason(stats)
		return res, nil
	}
	// Listing identity comes first: which offer at which shop each row is.
	// A listing carries the human decisions made on it and the group it is
	// currently believed to belong to, and both are inputs to matching, so
	// it has to be resolved before matching rather than written after it.
	sortCandidates(rows)
	known, err := m.loadListings(scope)
	if err != nil {
		return res, err
	}
	rowListing := make([]*models.Listing, len(rows))
	fresh, moved := map[string]bool{}, map[uint]bool{}
	for i := range rows {
		rows[i].listingIdentity()
		rowListing[i] = resolveKnownListing(known, rows[i])
		switch {
		case rowListing[i] == nil:
			fresh[rows[i].listingHash] = true
		case rowListing[i].URL != rows[i].url && !moved[rowListing[i].ID]:
			moved[rowListing[i].ID] = true
		}
	}
	res.NewListings, res.URLChanges = len(fresh), len(moved)

	byKey, fuzzyExisting, err := m.loadExistingGroups(plan)
	if err != nil {
		return res, err
	}

	ms := newMatchSet(len(rows))
	live := make([]bool, len(rows)) // false = excluded, dropped from matching entirely
	for i := range rows {
		live[i] = true
	}

	// Human decisions, read off the listing. Keyed this way they survive a
	// URL change, which is exactly what used to lose them.
	//
	// seenExcluded are offers the crawl did find but a human has excluded.
	// They take no further part in matching, but the sweep below must not
	// read their absence from it as the shop having delisted them.
	var seenExcluded []uint
	for i := range rows {
		l := rowListing[i]
		if l == nil {
			continue
		}
		if l.Excluded {
			live[i] = false
			seenExcluded = append(seenExcluded, l.ID)
			continue
		}
		if l.Pinned && l.ProductGroupID != "" {
			if _, exists := byKey.byID[l.ProductGroupID]; exists {
				ms.union(rowNode(i), existingNode(l.ProductGroupID))
				ms.note(i, models.MatchByManual, 1)
			}
		}
	}

	// A listing already placed in a group stays there when nothing stronger
	// decides otherwise. Without this, a listing whose title drifted below
	// the fuzzy threshold would be torn out of its group on the next run
	// and start a second history for the same offer. Rows carrying an exact
	// key are left out: exact matching places them, and is more reliable
	// than this continuity.
	if plan.matchMode != models.MatchModeManual {
		for i := range rows {
			l := rowListing[i]
			if l == nil || !live[i] || l.ProductGroupID == "" {
				continue
			}
			if key, _ := rows[i].exactKey(); key != "" {
				continue
			}
			if _, exists := byKey.byID[l.ProductGroupID]; !exists {
				continue
			}
			ms.union(rowNode(i), existingNode(l.ProductGroupID))
			if _, noted := ms.info[i]; !noted {
				method := l.MatchMethod
				if method == "" {
					method = models.MatchByFuzzy
				}
				ms.note(i, method, l.Confidence)
			}
		}
	}

	autoMatch := plan.matchMode != models.MatchModeManual
	fuzzyMatch := plan.matchMode == models.MatchModeAuto

	// Exact keys: GTIN, then brand+MPN.
	newExactGroups := map[string]int{} // key -> first row index with it, clustering new rows that share one
	if autoMatch {
		for i, r := range rows {
			if !live[i] {
				continue
			}
			key, method := r.exactKey()
			if key == "" {
				continue
			}
			if existingID, ok := byKey.byCanonicalKey[scopedCanonicalKey(key, r.variantKey)]; ok {
				ms.union(rowNode(i), existingNode(existingID))
				ms.note(i, method, 1)
				continue
			}
			clusterKey := scopedCanonicalKey(key, r.variantKey)
			if first, ok := newExactGroups[clusterKey]; ok {
				ms.union(rowNode(i), rowNode(first))
			} else {
				newExactGroups[clusterKey] = i
			}
			ms.note(i, method, 1)
		}
	}

	// Fuzzy name/token matching, bucketed by normalized brand to keep the
	// pairwise comparisons bounded.
	if fuzzyMatch {
		buckets := map[string][]int{}
		for i, r := range rows {
			if !live[i] {
				continue
			}
			if key, _ := r.exactKey(); key != "" {
				continue // already has a stronger identity
			}
			buckets[normKey(r.brand)] = append(buckets[normKey(r.brand)], i)
		}
		for _, idxs := range buckets {
			// Each row first tries the existing groups' remembered tokens.
			for _, i := range idxs {
				best, bestID := 0.0, ""
				for _, eg := range fuzzyExisting {
					if eg.variantKey != rows[i].variantKey {
						continue // a variant only matches the same variant
					}
					if eg.brand != "" && normKey(rows[i].brand) != "" && eg.brand != normKey(rows[i].brand) {
						continue
					}
					if s := jaccard(rows[i].tokens, eg.tokens); s > best {
						best, bestID = s, eg.id
					}
				}
				if best >= plan.threshold && bestID != "" {
					ms.union(rowNode(i), existingNode(bestID))
					ms.note(i, models.MatchByFuzzy, best)
				}
			}
			// Then each other within the same batch, so two sites that both
			// lack a GTIN/MPN can still form a new shared group.
			for a := 0; a < len(idxs); a++ {
				for b := a + 1; b < len(idxs); b++ {
					i, j := idxs[a], idxs[b]
					if rows[i].variantKey != rows[j].variantKey {
						continue
					}
					if s := jaccard(rows[i].tokens, rows[j].tokens); s >= plan.threshold {
						ms.union(rowNode(i), rowNode(j))
						ms.note(i, models.MatchByFuzzy, s)
						ms.note(j, models.MatchByFuzzy, s)
					}
				}
			}
		}
	}

	// A complete pass re-read every site of the scope, so whatever it did
	// not find again is gone and can be retired.
	saved, err := m.saveRegroupResult(plan, rows, live, ms, sweep{run: !opts.Partial}, seenExcluded)
	if err != nil {
		return res, err
	}
	res.Created, res.Groups, res.Observations = saved.created, saved.groups, saved.observations
	res.Merged, res.Retired, res.Changes = saved.merged, saved.retired, saved.changes
	for _, alive := range live {
		if !alive {
			res.Excluded++
		}
	}
	if res.Groups == 0 && res.Reason == "" {
		res.Reason = fmt.Sprintf("%d product pages were read but nothing was grouped; matching mode is %q", res.Candidates, plan.matchMode)
	}
	return res, nil
}

// RegroupOptions tunes one matching pass.
type RegroupOptions struct {
	// Partial matches whatever has been crawled so far, instead of waiting
	// for every source crawl to finish. The automatic pass after a crawl
	// ends leaves it off; the "Re-match" button turns it on, so a crawl
	// that is still running (or was stopped) can still be grouped.
	Partial bool
}

// RegroupResult reports what one pass did, so "nothing matched" can be told
// apart from "nothing was read", and the UI can say which it was.
type RegroupResult struct {
	Sources      int      `json:"sources"`       // crawls read
	ProductPages int      `json:"product_pages"` // product pages found in them
	Candidates   int      `json:"candidates"`    // products/variants compared
	Groups       int      `json:"groups"`        // groups touched by this pass
	Created      int      `json:"created"`       // of those, newly created
	Observations int      `json:"observations"`  // field values recorded
	Excluded     int      `json:"excluded"`      // candidates you excluded by hand
	Merged       int      `json:"merged"`        // groups folded into another, found to be the same product
	Retired      int      `json:"retired"`       // listings no longer found, now marked stale
	NewListings  int      `json:"new_listings"`  // offers seen here for the first time
	URLChanges   int      `json:"url_changes"`   // listings whose shop moved them to a new URL
	Changes      int      `json:"changes"`       // tracked values that moved since the previous run
	Fields       []string `json:"fields"`
	Reason       string   `json:"reason,omitempty"` // why nothing came of it
	Locked       bool     `json:"locked,omitempty"` // another pass on this scope was already running
}

// noCandidateReason explains an empty candidate list in the terms the user
// can act on.
func noCandidateReason(s candidateStats) string {
	switch {
	case s.productPages == 0:
		return "no kept pages are typed as a product — check the crawl's tag rules and keep_tags, or whether it has reached any product pages yet"
	case s.withProduct == 0:
		return fmt.Sprintf("%d product pages were found but none carry detected product data — enable the \"product\" format under Output & extraction and crawl again", s.productPages)
	}
	return fmt.Sprintf("%d product pages were found but none produced a comparable value for the tracked fields", s.productPages)
}

// scopedCanonicalKey qualifies an identity with the variant it belongs to,
// so the 128GB and 256GB rows of one model stay separate groups.
func scopedCanonicalKey(key, variantKey string) string {
	if variantKey == "" {
		return key
	}
	return key + "#" + variantKey
}

// siteJob is one finished crawl whose pages a scope compares. When
// byDomain is set — which is how every scope reads now — each page's own
// base domain is its site, so one crawl over several sites produces one
// column per site.
type siteJob struct {
	jobID, siteKey, siteLabel string
	byDomain                  bool
}

// sourceJobs returns the finished crawl a scope compares.
func (m *Manager) sourceJobs(scope groupScope, partial bool) ([]siteJob, string, error) {
	switch scope.Type {
	case models.ScopeSchedule:
		// A complete pass reads the latest finished run. A partial one —
		// asked for by hand, or made while a run is in progress — reads
		// the newest run whatever state it is in, which is the point of
		// asking: match what has been crawled so far.
		var job models.CrawlJob
		if partial {
			if err := m.db.Where("schedule_id = ?", scope.ID).Order("created_at DESC").First(&job).Error; err != nil {
				return nil, "this schedule has not run yet", nil
			}
			return []siteJob{{jobID: job.ID, byDomain: true}}, "", nil
		}
		err := m.db.Where("schedule_id = ? AND status = ?", scope.ID, models.JobStatusCompleted).
			Order("completed_at DESC").First(&job).Error
		if err != nil {
			return nil, "no run of this schedule has finished yet", nil
		}
		return []siteJob{{jobID: job.ID, byDomain: true}}, "", nil
	default:
		job, err := m.GetJob(scope.ID)
		if err != nil {
			return nil, "", err
		}
		// A crawl that is still running, paused or stopped has usable
		// pages already; only the automatic pass waits for the end.
		if job.Status != models.JobStatusCompleted && !partial {
			return nil, fmt.Sprintf("the crawl is %s; it is matched automatically when it finishes", job.Status), nil
		}
		return []siteJob{{jobID: job.ID, byDomain: true}}, "", nil
	}
}

// candidateStats records what loadCandidates saw, so an empty result can
// be explained rather than reported as "no matches".
type candidateStats struct {
	productPages int // kept pages typed as a product
	withProduct  int // of those, pages carrying detected product data
}

// loadCandidates reads the product pages of a scope's source crawls.
func (m *Manager) loadCandidates(plan groupPlan, sources []siteJob) ([]candidate, candidateStats, error) {
	var out []candidate
	var stats candidateStats
	for _, sj := range sources {
		var pages []models.CrawledPage
		if err := m.db.Where("crawl_job_id = ? AND page_type = ? AND skipped = ? AND is_archived = ?",
			sj.jobID, models.PageTypeProduct, false, false).Find(&pages).Error; err != nil {
			return nil, stats, err
		}
		stats.productPages += len(pages)
		if len(pages) == 0 {
			continue
		}
		ids := make([]uint, len(pages))
		byID := make(map[uint]models.CrawledPage, len(pages))
		for i, p := range pages {
			ids[i] = p.ID
			byID[p.ID] = p
		}
		var contents []models.PageContent
		if err := m.db.Where("page_id IN ?", ids).Find(&contents).Error; err != nil {
			return nil, stats, err
		}
		for _, c := range contents {
			if len(c.Product) == 0 {
				continue
			}
			var p content.Product
			if err := json.Unmarshal(c.Product, &p); err != nil {
				continue
			}
			stats.withProduct++
			page := byID[c.PageID]
			src := pageSource{jobID: sj.jobID, siteKey: sj.siteKey, siteLabel: sj.siteLabel, url: page.URL, pageID: page.ID}
			if sj.byDomain {
				// One crawl covering several shops: the page's own domain
				// is the column it belongs to.
				if _, domain, err := ParseTarget(page.URL); err == nil && domain != "" {
					src.siteKey, src.siteLabel = domain, domain
				} else {
					src.siteKey, src.siteLabel = "unknown", "unknown"
				}
			}
			out = append(out, candidatesFromProduct(src, &p, c.Extracted, plan)...)
		}
	}
	return out, stats, nil
}

// loadListings indexes every listing a scope already knows by its
// identity hash, so this pass can recognise each row as an offer it has
// seen before — with its history, its group and the decisions made on it —
// rather than as a new page at a new URL.
func (m *Manager) loadListings(scope groupScope) (map[string]*models.Listing, error) {
	var listings []models.Listing
	if err := m.db.Where("scope_type = ? AND scope_id = ?", scope.Type, scope.ID).Find(&listings).Error; err != nil {
		return nil, err
	}
	out := make(map[string]*models.Listing, len(listings))
	for i := range listings {
		out[listings[i].ListingHash] = &listings[i]
	}
	return out, nil
}

// resolveKnownListing finds the listing a row belongs to among the ones
// this scope already knows, trying the row's strong identity first and
// then the weaker ones it would have been filed under before — the same
// order findListing uses when writing, so matching and saving agree on
// which offer a row is.
func resolveKnownListing(known map[string]*models.Listing, r candidate) *models.Listing {
	if l := known[r.listingHash]; l != nil {
		return l
	}
	for _, hash := range r.fallbackHashes {
		if l := known[hash]; l != nil {
			return l
		}
	}
	return nil
}

// sortCandidates puts a pass's rows in a stable order, so that a rerun
// over the same pages makes the same decisions: which row represents a
// new group, and which page's URL a listing reached by several URLs
// records. Without it both followed whatever order the database returned.
func sortCandidates(rows []candidate) {
	sort.Slice(rows, func(a, b int) bool {
		x, y := rows[a], rows[b]
		if x.siteKey != y.siteKey {
			return x.siteKey < y.siteKey
		}
		if x.variantKey != y.variantKey {
			return x.variantKey < y.variantKey
		}
		if x.url != y.url {
			return x.url < y.url
		}
		return x.pageID < y.pageID
	})
}

// existingIndex is a scope's ProductGroups, indexed for matching.
type existingIndex struct {
	byID           map[string]models.ProductGroup
	byCanonicalKey map[string]string // exact canonical key -> product group id
}

type fuzzyGroupEntry struct {
	id, brand, variantKey string
	tokens                []string
}

func (m *Manager) loadExistingGroups(plan groupPlan) (existingIndex, []fuzzyGroupEntry, error) {
	var groups []models.ProductGroup
	if err := m.db.Where("scope_type = ? AND scope_id = ?", plan.scope.Type, plan.scope.ID).Find(&groups).Error; err != nil {
		return existingIndex{}, nil, err
	}
	idx := existingIndex{byID: make(map[string]models.ProductGroup, len(groups)), byCanonicalKey: map[string]string{}}
	var fuzzy []fuzzyGroupEntry
	for _, g := range groups {
		idx.byID[g.ID] = g
		if strings.HasPrefix(g.CanonicalKey, "gtin:") || strings.HasPrefix(g.CanonicalKey, "mpn:") {
			idx.byCanonicalKey[g.CanonicalKey] = g.ID
			continue
		}
		// Tokenized exactly like the candidates it is compared with —
		// including the scope's extra stop words, or the two sides would
		// not be comparable.
		fuzzy = append(fuzzy, fuzzyGroupEntry{id: g.ID, brand: normKey(g.Brand), variantKey: g.VariantKey, tokens: nameTokens(g.Label, g.Brand, plan.extraStop)})
	}
	return idx, fuzzy, nil
}

// saveRegroupResult writes the matched ProductGroups, their members and a
// FieldObservation per compared field, from the union-find components.
// saveCounts is what one save pass wrote.
type saveCounts struct{ created, groups, observations, merged, retired, changes int }

// sweep says whether this pass may retire the members it did not find
// again. Only a complete pass may: a partial one (made while the crawl is
// still going) has not reached every page yet, and a page it has not
// reached is not gone.
type sweep struct {
	run bool
}

func (m *Manager) saveRegroupResult(plan groupPlan, rows []candidate, live []bool, ms *matchSet, sw sweep, seenExcluded []uint) (saveCounts, error) {
	var counts saveCounts
	components := map[string][]int{}
	for i := range rows {
		if !live[i] {
			continue
		}
		root := ms.find(rowNode(i))
		components[root] = append(components[root], i)
	}
	maxGroups := m.config.Grouping.MaxProductGroupsPerJobGroup
	if maxGroups <= 0 {
		maxGroups = 5000
	}
	singletonsAllowed := plan.matchMode == models.MatchModeAuto

	err := m.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		// Two existing groups pulled into one component are the same
		// product found twice: fold the absorbed ones into the component's
		// root so their members and their history move with them, instead
		// of leaving an empty group behind and splitting the series.
		merged, err := m.mergeAbsorbedGroups(tx, plan, ms)
		if err != nil {
			return err
		}
		counts.merged = merged
		created := 0
		for root, idxs := range components {
			var productGroupID string
			switch {
			case strings.HasPrefix(root, "existing:"):
				productGroupID = strings.TrimPrefix(root, "existing:")
			case len(idxs) == 1 && !singletonsAllowed:
				// exact_only/manual: nothing was matched for it. It is
				// still a listing, and still tracked — it just has no
				// group yet, so the dashboard can offer it for
				// assignment by hand with its history already attached.
				if err := m.saveRows(tx, plan, "", rows, idxs, ms, now, &counts); err != nil {
					return err
				}
				continue
			default:
				if created >= maxGroups {
					log.Printf("[Grouping] Scope %s/%s: product group cap (%d) reached, skipping a new match", plan.scope.Type, plan.scope.ID, maxGroups)
					continue
				}
				created++
				counts.created++
				pg, err := m.createProductGroup(tx, plan, rows, idxs, ms)
				if err != nil {
					return err
				}
				productGroupID = pg.ID
			}
			if err := m.saveRows(tx, plan, productGroupID, rows, idxs, ms, now, &counts); err != nil {
				return err
			}
			counts.groups++
			if err := m.refreshProductGroupStats(tx, productGroupID, plan.primaryField()); err != nil {
				return err
			}
		}
		// An excluded offer the crawl still found is present, just not
		// compared. Touching it here keeps the sweep from reporting it as
		// delisted on the strength of a decision the user made.
		if len(seenExcluded) > 0 {
			if err := tx.Model(&models.Listing{}).Where("id IN ?", seenExcluded).
				Updates(map[string]interface{}{"last_seen_at": now, "stale": false}).Error; err != nil {
				return err
			}
		}
		retired, err := m.retireUnseenListings(tx, plan, sw, now)
		if err != nil {
			return err
		}
		counts.retired = retired
		return nil
	})
	return counts, err
}

// mergeAbsorbedGroups folds every existing product group that ended up in
// another's component into that component's root group: members and
// recorded history move across and the absorbed group row is deleted. It
// returns how many groups were folded in.
func (m *Manager) mergeAbsorbedGroups(tx *gorm.DB, plan groupPlan, ms *matchSet) (int, error) {
	nodes := make([]string, 0, len(ms.parent))
	for node := range ms.parent {
		if strings.HasPrefix(node, "existing:") {
			nodes = append(nodes, node)
		}
	}
	byRoot := map[string][]string{}
	for _, node := range nodes {
		id := strings.TrimPrefix(node, "existing:")
		root := strings.TrimPrefix(ms.find(node), "existing:")
		if root != id {
			byRoot[root] = append(byRoot[root], id)
		}
	}
	folded := 0
	for root, absorbed := range byRoot {
		sort.Strings(absorbed) // deterministic order, so a retry does the same thing
		for _, id := range absorbed {
			// Everything that names the absorbed group follows the fold,
			// so a merge never splits a listing's series or leaves a
			// rolled-up day or a change event pointing at a dead group.
			for _, model := range []interface{}{
				&models.Listing{}, &models.FieldObservation{},
				&models.FieldObservationDay{}, &models.ListingChange{},
			} {
				if err := tx.Model(model).Where("product_group_id = ?", id).Update("product_group_id", root).Error; err != nil {
					return folded, err
				}
			}
			if err := tx.Where("id = ? AND scope_type = ? AND scope_id = ?", id, plan.scope.Type, plan.scope.ID).
				Delete(&models.ProductGroup{}).Error; err != nil {
				return folded, err
			}
			folded++
		}
	}
	return folded, nil
}

// retireUnseenListings marks the listings a complete pass did not find
// again as stale and refreshes the summary of every group that lost one,
// so a delisted product stops being quoted as a live offer. The rows,
// their history and their pinned/excluded decisions are all kept: a
// listing that comes back is the same listing, with its series intact.
func (m *Manager) retireUnseenListings(tx *gorm.DB, plan groupPlan, sw sweep, now time.Time) (int, error) {
	if !sw.run {
		return 0, nil
	}
	var unseen []models.Listing
	if err := tx.Where("scope_type = ? AND scope_id = ? AND stale = ? AND last_seen_at < ?",
		plan.scope.Type, plan.scope.ID, false, now).Find(&unseen).Error; err != nil {
		return 0, err
	}
	if len(unseen) == 0 {
		return 0, nil
	}
	ids := make([]uint, 0, len(unseen))
	groups := map[string]bool{}
	for _, l := range unseen {
		ids = append(ids, l.ID)
		groups[l.ProductGroupID] = true
	}
	if err := tx.Model(&models.Listing{}).Where("id IN ?", ids).Update("stale", true).Error; err != nil {
		return 0, err
	}
	for id := range groups {
		if id == "" {
			continue // an ungrouped listing has no summary to refresh
		}
		if err := m.refreshProductGroupStats(tx, id, plan.primaryField()); err != nil {
			return 0, err
		}
	}
	return len(unseen), nil
}

func (m *Manager) createProductGroup(tx *gorm.DB, plan groupPlan, rows []candidate, idxs []int, ms *matchSet) (*models.ProductGroup, error) {
	rep := rows[idxs[0]]
	bestMethod, bestConf := "", 0.0
	for _, i := range idxs {
		if info, ok := ms.info[i]; ok && info.confidence >= bestConf {
			bestMethod, bestConf = info.method, info.confidence
		}
	}
	// The group is found again next run by this key, so it must be the
	// strongest identity anyone in the group carries: a GTIN beats a
	// brand+MPN, and either beats the name digest. Taking the first row's
	// key instead would hide a GTIN behind a fuzzy key and create a second
	// group for the same product on the next run.
	key := strongestKey(rows, idxs)
	if key == "" {
		key = "fuzzy:" + fuzzyDigest(rep.tokens)
	}
	// Distinct listings, which is what the group will hold — not rows,
	// since two pages of one shop can be one listing.
	hashes := map[string]bool{}
	for _, i := range idxs {
		hashes[rows[i].listingHash] = true
	}
	if bestMethod == "" {
		bestMethod = models.MatchByFuzzy
	}
	pg := &models.ProductGroup{
		ScopeType:    plan.scope.Type,
		ScopeID:      plan.scope.ID,
		Label:        rep.name,
		Brand:        rep.brand,
		VariantKey:   rep.variantKey,
		VariantLabel: rep.variantLabel,
		CanonicalKey: scopedCanonicalKey(key, rep.variantKey),
		MatchMethod:  bestMethod,
		Confidence:   bestConf,
		ListingCount: len(hashes),
	}
	if err := tx.Create(pg).Error; err != nil {
		return nil, err
	}
	return pg, nil
}

// strongestKey is the best exact identity among a component's rows: a
// GTIN if any row has one, else a brand+MPN, else "".
func strongestKey(rows []candidate, idxs []int) string {
	fallback := ""
	for _, i := range idxs {
		key, method := rows[i].exactKey()
		if method == models.MatchByGTIN {
			return key
		}
		if key != "" && fallback == "" {
			fallback = key
		}
	}
	return fallback
}

func fuzzyDigest(tokens []string) string {
	h := sha1.New()
	h.Write([]byte(strings.Join(tokens, "|")))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// saveRows writes one component's listings and their observations. The
// listing is upserted by identity, so an offer the shop moved to a new URL
// keeps its row, its whole history and the decisions made on it; then this
// run's value of every compared field is recorded against it.
func (m *Manager) saveRows(tx *gorm.DB, plan groupPlan, productGroupID string, rows []candidate, idxs []int, ms *matchSet, now time.Time, counts *saveCounts) error {
	// Several pages of one shop can resolve to one listing (the same offer
	// reached by two URLs). The first in sorted order is the one whose URL
	// and page the listing records; the rest only confirm it is still there.
	seen := map[string]bool{}
	for _, i := range idxs {
		r := rows[i]
		l, err := m.upsertListing(tx, plan, productGroupID, r, ms.info[i], now, seen[r.listingHash])
		if err != nil {
			return err
		}
		seen[r.listingHash] = true
		written, changed, err := m.recordObservations(tx, plan, l, r, now)
		if err != nil {
			return err
		}
		counts.observations += written
		counts.changes += changed
	}
	return nil
}

// maxPreviousURLs bounds the URL trail kept on a listing, so a shop that
// rewrites its slugs every week cannot grow one row without end.
const maxPreviousURLs = 10

// upsertListing finds this row's listing by identity and brings it up to
// date, or creates it. Identity is deliberately not the URL: when a shop
// moves an offer, the URL is what changes and the listing is what stays,
// so the old URL is filed in PreviousURLs and everything else carries on.
//
// A pinned listing keeps the group a human put it in; otherwise this
// pass's match decides. repeat says another row of this pass already
// claimed this listing, in which case the URL and page are left alone.
func (m *Manager) upsertListing(tx *gorm.DB, plan groupPlan, productGroupID string, r candidate, info matchInfo, now time.Time, repeat bool) (*models.Listing, error) {
	l, err := m.findListing(tx, plan, r)
	if err != nil {
		return nil, err
	}
	if l != nil {
		updates := map[string]interface{}{
			"stale": false, "last_seen_at": now, "crawl_job_id": r.jobID,
			"site_label": r.siteLabel,
		}
		if l.ListingHash != r.listingHash {
			// Adopted by a weaker key and now carrying a stronger one:
			// upgrade the identity in place, so the series continues.
			updates["listing_hash"] = r.listingHash
			l.ListingHash = r.listingHash
		}
		if l.ListingKey != r.listingKey || l.KeyMethod != r.keyMethod {
			updates["listing_key"], updates["key_method"] = r.listingKey, r.keyMethod
			l.ListingKey, l.KeyMethod = r.listingKey, r.keyMethod
		}
		if l.CrawlJobID != r.jobID {
			updates["runs"] = l.Runs + 1
			l.Runs++
		}
		if !l.Pinned {
			updates["product_group_id"] = productGroupID
			updates["match_method"] = info.method
			updates["confidence"] = info.confidence
			l.ProductGroupID, l.MatchMethod, l.Confidence = productGroupID, info.method, info.confidence
		}
		if !repeat {
			updates["page_id"], updates["title"] = r.pageID, r.title()
			l.PageID, l.Title = r.pageID, r.title()
			if l.URL != r.url {
				trail := append([]string(l.PreviousURLs), l.URL)
				if len(trail) > maxPreviousURLs {
					trail = trail[len(trail)-maxPreviousURLs:]
				}
				updates["url"], updates["previous_urls"] = r.url, models.StringSlice(trail)
				l.URL, l.PreviousURLs = r.url, trail
			}
		}
		l.Stale, l.LastSeenAt, l.CrawlJobID = false, now, r.jobID
		if err := tx.Model(&models.Listing{}).Where("id = ?", l.ID).Updates(updates).Error; err != nil {
			return nil, err
		}
		return l, nil
	}
	fresh := &models.Listing{
		ScopeType: plan.scope.Type, ScopeID: plan.scope.ID,
		ListingKey: r.listingKey, ListingHash: r.listingHash, KeyMethod: r.keyMethod,
		SiteKey: r.siteKey, SiteLabel: r.siteLabel, VariantKey: r.variantKey, Title: r.title(),
		URL: r.url, ProductGroupID: productGroupID,
		MatchMethod: info.method, Confidence: info.confidence,
		CrawlJobID: r.jobID, PageID: r.pageID, Runs: 1,
		FirstSeenAt: now, LastSeenAt: now,
	}
	if err := tx.Create(fresh).Error; err != nil {
		return nil, err
	}
	return fresh, nil
}

// findListing resolves a row to the listing it belongs to: by its strong
// identity, else by the weaker identities the same offer would have been
// filed under before the page carried a strong id. Not finding a row is
// the ordinary case for a new offer, so these use Find rather than First,
// which would log every miss as an error.
func (m *Manager) findListing(tx *gorm.DB, plan groupPlan, r candidate) (*models.Listing, error) {
	for _, hash := range append([]string{r.listingHash}, r.fallbackHashes...) {
		var found []models.Listing
		if err := tx.Where("scope_type = ? AND scope_id = ? AND listing_hash = ?",
			plan.scope.Type, plan.scope.ID, hash).Limit(1).Find(&found).Error; err != nil {
			return nil, err
		}
		if len(found) > 0 {
			return &found[0], nil
		}
	}
	return nil, nil
}

// recordObservations appends this run's value of every compared field to
// the listing's series — the time series behind both the comparison table
// and the per-listing history chart — and records a ListingChange wherever
// a value actually moved since the listing's previous run.
func (m *Manager) recordObservations(tx *gorm.DB, plan groupPlan, l *models.Listing, r candidate, now time.Time) (written, changed int, err error) {
	for _, field := range plan.compareFields {
		val, ok := r.fields[field]
		if !ok || (val.Num == nil && val.Text == "") {
			continue
		}
		// One observation per (run, listing, field). Matching may run
		// several times over the same run — automatically when the crawl
		// ends, again on "Re-match", and during the crawl while it fills
		// in — and every pass must update that run's value, never append
		// a second point to the history.
		var existing []models.FieldObservation
		if err := tx.Where("listing_id = ? AND crawl_job_id = ? AND field = ?", l.ID, r.jobID, field).
			Limit(1).Find(&existing).Error; err != nil {
			return written, changed, err
		}
		if len(existing) > 0 {
			if err := tx.Model(&models.FieldObservation{}).Where("id = ?", existing[0].ID).Updates(map[string]interface{}{
				"product_group_id": l.ProductGroupID, "site_key": r.siteKey, "site_label": r.siteLabel,
				"page_id": r.pageID, "title": r.title(),
				"num_value": val.Num, "text_value": val.Text, "currency": val.Currency, "url": r.url,
			}).Error; err != nil {
				return written, changed, err
			}
			written++
			continue
		}
		// A fresh point. What did this listing last say, in an earlier run?
		var prev []models.FieldObservation
		if err := tx.Where("listing_id = ? AND field = ? AND crawl_job_id <> ?", l.ID, field, r.jobID).
			Order("observed_at DESC, id DESC").Limit(1).Find(&prev).Error; err != nil {
			return written, changed, err
		}
		obs := models.FieldObservation{
			ScopeType: plan.scope.Type, ScopeID: plan.scope.ID,
			ListingID: l.ID, ProductGroupID: l.ProductGroupID, Field: field,
			CrawlJobID: r.jobID, PageID: r.pageID,
			SiteKey: r.siteKey, SiteLabel: r.siteLabel, VariantKey: r.variantKey, Title: r.title(),
			NumValue: val.Num, TextValue: val.Text, Currency: val.Currency, URL: r.url,
			ObservedAt: now,
		}
		if err := tx.Create(&obs).Error; err != nil {
			return written, changed, err
		}
		written++
		if len(prev) == 0 || !observationMoved(prev[0], val) {
			continue
		}
		change := models.ListingChange{
			ScopeType: plan.scope.Type, ScopeID: plan.scope.ID,
			ListingID: l.ID, Field: field, ProductGroupID: l.ProductGroupID,
			CrawlJobID: r.jobID, SiteKey: r.siteKey, Title: r.title(), URL: r.url,
			OldNum: prev[0].NumValue, NewNum: val.Num,
			OldText: prev[0].TextValue, NewText: val.Text,
			Currency: val.Currency, DetectedAt: now,
		}
		if err := tx.Create(&change).Error; err != nil {
			return written, changed, err
		}
		changed++
	}
	return written, changed, nil
}

// observationMoved reports whether a new value differs from the listing's
// previous one. A numeric field compares numbers (a price written
// "42000.0" and "42000" has not moved); a text field compares text.
func observationMoved(prev models.FieldObservation, val content.FieldValue) bool {
	if val.Num != nil || prev.NumValue != nil {
		if val.Num == nil || prev.NumValue == nil {
			return true // appeared or disappeared
		}
		return *val.Num != *prev.NumValue
	}
	return val.Text != prev.TextValue
}

// refreshProductGroupStats recomputes a group's summary of the primary
// compared field: the lowest and highest a live site offered for it, plus
// how many live listings the group holds.
//
// A shop may hold more than one listing of the same product (a marketplace
// with several sellers, a bundle beside the bare item). The summary takes
// each shop's cheapest live listing, exactly as the table's cell does, so
// the "cheapest" it names is the cell the table highlights — and shops
// quoting another currency are left out rather than compared as bare
// numbers.
func (m *Manager) refreshProductGroupStats(tx *gorm.DB, productGroupID, primaryField string) error {
	if productGroupID == "" {
		return nil
	}
	var listings []models.Listing
	if err := tx.Where("product_group_id = ? AND excluded = ? AND stale = ?", productGroupID, false, false).
		Find(&listings).Error; err != nil {
		return err
	}
	if len(listings) == 0 {
		return tx.Model(&models.ProductGroup{}).Where("id = ?", productGroupID).Updates(map[string]interface{}{
			"member_count": 0, "lowest_value": nil, "highest_value": nil, "currency": "",
		}).Error
	}
	ids := make([]uint, len(listings))
	siteOf := make(map[uint]string, len(listings))
	for i, l := range listings {
		ids[i] = l.ID
		siteOf[l.ID] = l.SiteKey
	}
	latest, err := latestObservationPerListing(tx, ids, primaryField)
	if err != nil {
		return err
	}
	// Each shop's cheapest live listing, with the currency it quoted.
	type siteValue struct {
		amount   float64
		currency string
	}
	bySite := map[string]siteValue{}
	for listingID, o := range latest {
		if o.NumValue == nil {
			continue // a text field has no low/high
		}
		site := siteOf[listingID]
		if cur, ok := bySite[site]; ok && cur.amount <= *o.NumValue {
			continue
		}
		bySite[site] = siteValue{*o.NumValue, o.Currency}
	}
	// Prices in different currencies are not comparable, so the summary
	// speaks for the currency most of the shops quote.
	counts := map[string]int{}
	for _, v := range bySite {
		if v.currency != "" {
			counts[v.currency]++
		}
	}
	currency, bestN := "", 0
	for cur, n := range counts {
		if n > bestN || (n == bestN && cur < currency) {
			currency, bestN = cur, n
		}
	}
	var low, high *float64
	for _, v := range bySite {
		if currency != "" && v.currency != "" && v.currency != currency {
			continue
		}
		amount := v.amount
		if low == nil || amount < *low {
			value := amount
			low = &value
		}
		if high == nil || amount > *high {
			value := amount
			high = &value
		}
	}
	return tx.Model(&models.ProductGroup{}).Where("id = ?", productGroupID).Updates(map[string]interface{}{
		"member_count": len(listings), "lowest_value": low, "highest_value": high, "currency": currency,
	}).Error
}
