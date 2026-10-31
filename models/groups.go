package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Match modes for product matching, set per crawl or schedule under
// group_products (see JobSettings.MatchMode).
const (
	MatchModeAuto      = "auto"       // exact keys, then fuzzy name/token matching
	MatchModeExactOnly = "exact_only" // only GTIN / brand+MPN; never fuzzy-matched
	MatchModeManual    = "manual"     // matching never runs; members are placed by hand
)

// Match methods recorded on a ProductGroup / ProductGroupMember, strongest first.
const (
	MatchByGTIN   = "gtin"
	MatchByMPN    = "mpn"
	MatchByFuzzy  = "fuzzy"
	MatchByManual = "manual"
)

// Grouping scopes. A ProductGroup belongs to exactly one scope, which
// decides how long the group lives. A site is the page's own domain in
// both, so one crawl given several start URLs produces one column per
// site:
//
//   - ScopeSchedule: every run of a Schedule, so groups and their field
//     history survive each rerun (a run creates new page rows).
//   - ScopeCrawl:    one one-off crawl job.
const (
	ScopeSchedule = "schedule"
	ScopeCrawl    = "crawl"
)

// ValidGroupingScope reports whether t is a grouping scope type.
func ValidGroupingScope(t string) bool {
	return t == ScopeSchedule || t == ScopeCrawl
}

// ProductGroup is a set of pages within one grouping scope believed to be
// the same product (or, with variant grouping on, the same variant of it).
// CanonicalKey is how it is found again on the next run: a GTIN or
// brand+MPN when one is available, otherwise a loose key from the
// normalized name, so fuzzy-matched groups usually stay stable too.
type ProductGroup struct {
	ID           string  `gorm:"type:varchar(36);primaryKey" json:"id"`
	ScopeType    string  `gorm:"type:varchar(16);index:idx_pg_scope,priority:1;not null" json:"scope_type"`
	ScopeID      string  `gorm:"type:varchar(36);index:idx_pg_scope,priority:2;not null" json:"scope_id"`
	Label        string  `gorm:"type:varchar(255)" json:"label"`
	Brand        string  `gorm:"type:varchar(128);default:''" json:"brand,omitempty"`
	VariantKey   string  `gorm:"type:varchar(255);default:''" json:"variant_key,omitempty"`
	VariantLabel string  `gorm:"type:varchar(255);default:''" json:"variant_label,omitempty"`
	CanonicalKey string  `gorm:"type:varchar(128);index;default:''" json:"canonical_key,omitempty"`
	MatchMethod  string  `gorm:"type:varchar(16);default:''" json:"match_method,omitempty"`
	Confidence   float64 `gorm:"default:0" json:"confidence"`
	MemberCount  int     `gorm:"default:0" json:"member_count"`
	// Summary of the scope's primary compared field (price, unless the
	// scope names another first), across the group's sites.
	LowestValue  *float64  `json:"lowest_value,omitempty"`
	HighestValue *float64  `json:"highest_value,omitempty"`
	Currency     string    `gorm:"type:varchar(8);default:''" json:"currency,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// BeforeCreate generates a UUID for new product groups.
func (g *ProductGroup) BeforeCreate(tx *gorm.DB) error {
	if g.ID == "" {
		g.ID = uuid.New().String()
	}
	return nil
}

// ProductGroupMember is one page's membership of a ProductGroup on its
// site's latest run. SiteKey is the group's site id in a comparison scope
// and the page's domain otherwise, so the comparison table has one column
// per site either way. Pinned and Excluded are human decisions that
// override matching: a pinned member keeps its group on the next regroup,
// an excluded page is never placed in any group of this scope again.
type ProductGroupMember struct {
	ID             uint    `gorm:"primaryKey" json:"id"`
	ProductGroupID string  `gorm:"type:varchar(36);index:idx_pgm_group;not null" json:"product_group_id"`
	ScopeType      string  `gorm:"type:varchar(16);index:idx_pgm_scope,priority:1;not null" json:"scope_type"`
	ScopeID        string  `gorm:"type:varchar(36);index:idx_pgm_scope,priority:2;not null" json:"scope_id"`
	SiteKey        string  `gorm:"type:varchar(191);index;default:''" json:"site_key,omitempty"`
	SiteLabel      string  `gorm:"type:varchar(128);default:''" json:"site_label,omitempty"`
	CrawlJobID     string  `gorm:"type:varchar(36);index;not null" json:"crawl_job_id"`
	PageID         uint    `gorm:"index;not null" json:"page_id"`
	URL            string  `gorm:"type:varchar(2048);default:''" json:"url,omitempty"`
	VariantKey     string  `gorm:"type:varchar(255);default:''" json:"variant_key,omitempty"` // "" for a page with no variants, else which variant this membership is
	MatchMethod    string  `gorm:"type:varchar(16);default:''" json:"match_method,omitempty"`
	Confidence     float64 `gorm:"default:0" json:"confidence"`
	Pinned         bool    `gorm:"default:false" json:"pinned"`
	Excluded       bool    `gorm:"default:false" json:"excluded"`
	// Stale marks a member the latest complete matching pass no longer
	// found: the page is gone from the site, or the site was dropped from
	// the crawl. It keeps its history and its pinned/excluded decision,
	// but is left out of the group's member count and price summary so a
	// delisted product stops being quoted as a live offer.
	Stale      bool      `gorm:"default:false;index" json:"stale,omitempty"`
	LastSeenAt time.Time `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// FieldObservation is one compared field of one page at the end of one
// crawl run: the time series behind the comparison table and its history.
// Numeric fields (price, rating, review_count…) fill NumValue; text fields
// (availability, seller, condition…) fill TextValue. A row with no
// ProductGroupID is an observation of a page nothing matched yet; it is
// attached to a group when the page is matched or assigned by hand.
type FieldObservation struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ScopeType      string    `gorm:"type:varchar(16);index:idx_fo_scope,priority:1;not null" json:"scope_type"`
	ScopeID        string    `gorm:"type:varchar(36);index:idx_fo_scope,priority:2;not null" json:"scope_id"`
	ProductGroupID string    `gorm:"type:varchar(36);index:idx_fo_group,priority:1;default:''" json:"product_group_id,omitempty"`
	Field          string    `gorm:"type:varchar(64);index:idx_fo_group,priority:2;not null" json:"field"`
	CrawlJobID     string    `gorm:"type:varchar(36);index;not null" json:"crawl_job_id"`
	PageID         uint      `gorm:"index;not null" json:"page_id"`
	SiteKey        string    `gorm:"type:varchar(191);index;default:''" json:"site_key,omitempty"`
	SiteLabel      string    `gorm:"type:varchar(128);default:''" json:"site_label,omitempty"`
	VariantKey     string    `gorm:"type:varchar(255);default:''" json:"variant_key,omitempty"`
	Title          string    `gorm:"type:varchar(512);default:''" json:"title,omitempty"` // product/variant name when observed, for display before it is matched
	NumValue       *float64  `json:"num_value,omitempty"`
	TextValue      string    `gorm:"type:varchar(512);default:''" json:"text_value,omitempty"`
	Currency       string    `gorm:"type:varchar(8);default:''" json:"currency,omitempty"`
	URL            string    `gorm:"type:varchar(2048)" json:"url"`
	ObservedAt     time.Time `gorm:"index:idx_fo_group,priority:3" json:"observed_at"`
}

// Value returns the observation as a comparable number plus its display
// text, so callers do not branch on the field's kind.
func (o FieldObservation) Value() (num float64, hasNum bool, text string) {
	if o.NumValue != nil {
		return *o.NumValue, true, o.TextValue
	}
	return 0, false, o.TextValue
}
