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

// Match methods recorded on a ProductGroup / Listing, strongest first.
const (
	MatchByGTIN   = "gtin"
	MatchByMPN    = "mpn"
	MatchByFuzzy  = "fuzzy"
	MatchByManual = "manual"
)

// Listing key methods, strongest first. These say how a Listing is
// recognised again on the next run, and so how much to trust that this
// run's value belongs on the same history as the last one's.
//
// They are not the same thing as a MatchBy* method: a match method is how
// confident we are that this listing is the same *product* as the others
// in its group (a guess, across sites), while a key method is how
// confident we are that it is the same *listing* as last run (a fact,
// within one site).
const (
	ListingBySKU  = "sku"  // the site's own product id, read off the page
	ListingByGTIN = "gtin" // barcode, scoped to the site
	ListingByMPN  = "mpn"  // brand + manufacturer part number, scoped to the site
	ListingByPath = "path" // stable id segments of the URL path; survives a slug rewrite
	ListingByURL  = "url"  // whole normalized URL — the last resort, broken by any URL change
)

// StableListingKey reports whether a key method survives a URL change. The
// dashboard warns about listings keyed the one way that does not.
func StableListingKey(method string) bool {
	return method == ListingBySKU || method == ListingByGTIN || method == ListingByMPN || method == ListingByPath
}

// Grouping scopes. A ProductGroup belongs to exactly one scope, which
// decides how long the group lives. A site is the page's own domain in
// both, so one crawl given several start URLs produces one column per
// site:
//
//   - ScopeSchedule: every run of a Schedule, so groups and their field
//     history survive each rerun (a run creates new page rows). This is
//     the scope that tracks a listing over time.
//   - ScopeCrawl:    one one-off crawl job, which has a single run and so
//     compares sites without tracking anything.
const (
	ScopeSchedule = "schedule"
	ScopeCrawl    = "crawl"
)

// ValidGroupingScope reports whether t is a grouping scope type.
func ValidGroupingScope(t string) bool {
	return t == ScopeSchedule || t == ScopeCrawl
}

// Tracks reports whether a scope accumulates history across runs. A
// schedule reruns and so has a time series per listing; a one-off crawl
// has one point and nothing to compare it with, so the UI hides the
// tracking affordances rather than showing empty sparklines.
func ScopeTracks(scopeType string) bool { return scopeType == ScopeSchedule }

// ProductGroup is a set of listings within one grouping scope believed to
// be the same product (or, with variant grouping on, the same variant of
// it). CanonicalKey is how it is found again on the next run: a GTIN or
// brand+MPN when one is available, otherwise a loose key from the
// normalized name, so fuzzy-matched groups usually stay stable too.
//
// A group is a *guess*, and a correctable one: moving a listing between
// groups never disturbs the listing's own history, because observations
// hang off the listing, not off the group.
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
	// ListingCount is how many live listings the group holds. The column
	// is still called member_count, from when a listing was a "member".
	ListingCount int `gorm:"column:member_count;default:0" json:"listing_count"`
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

// Listing is one store's offer of one product variant, within one grouping
// scope: the cell of the comparison table and, at the same time, the thing
// a price history belongs to. It is the unit the user clicks, tracks and
// is alerted on.
//
// Identity is ListingHash — a digest of ListingKey, which is derived from
// the strongest per-site id the page carries (see the ListingBy*
// constants) and deliberately *not* from the URL. URL is an attribute that
// may change under a listing; when it does, PreviousURLs records it and
// the listing — with its whole history and its pinned/excluded decision —
// carries on unbroken.
//
// Pinned and Excluded are human decisions that override matching: a pinned
// listing keeps its group on the next regroup, an excluded one is never
// placed in any group of this scope again.
type Listing struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	ScopeType string `gorm:"type:varchar(16);index:idx_listing_scope,priority:1;uniqueIndex:idx_listing_identity,priority:1;not null" json:"scope_type"`
	ScopeID   string `gorm:"type:varchar(36);index:idx_listing_scope,priority:2;uniqueIndex:idx_listing_identity,priority:2;not null" json:"scope_id"`

	// Identity, independent of the URL.
	ListingKey  string `gorm:"type:varchar(512);default:''" json:"listing_key,omitempty"` // readable, for debugging
	ListingHash string `gorm:"type:varchar(40);uniqueIndex:idx_listing_identity,priority:3;default:''" json:"listing_hash,omitempty"`
	KeyMethod   string `gorm:"type:varchar(16);default:''" json:"key_method,omitempty"`

	SiteKey    string `gorm:"type:varchar(191);index;default:''" json:"site_key,omitempty"`
	SiteLabel  string `gorm:"type:varchar(128);default:''" json:"site_label,omitempty"`
	VariantKey string `gorm:"type:varchar(255);default:''" json:"variant_key,omitempty"` // "" for a page with no variants
	Title      string `gorm:"type:varchar(512);default:''" json:"title,omitempty"`

	// Where it currently lives, and where it used to.
	URL          string      `gorm:"type:varchar(2048);default:''" json:"url,omitempty"`
	PreviousURLs StringSlice `gorm:"type:json" json:"previous_urls,omitempty"`

	// The group this listing is believed to belong to — the mutable guess.
	ProductGroupID string  `gorm:"type:varchar(36);index:idx_listing_group;default:''" json:"product_group_id,omitempty"`
	MatchMethod    string  `gorm:"type:varchar(16);default:''" json:"match_method,omitempty"`
	Confidence     float64 `gorm:"default:0" json:"confidence"`

	// Human overrides. These live on the listing, so they survive a URL
	// change and a regroup alike.
	Pinned   bool `gorm:"default:false" json:"pinned"`
	Excluded bool `gorm:"default:false" json:"excluded"`
	// Stale marks a listing the latest complete matching pass no longer
	// found: the page is gone from the site, or the site was dropped from
	// the crawl. It keeps its history and its pinned/excluded decision,
	// but is left out of the group's listing count and price summary so a
	// delisted product stops being quoted as a live offer.
	Stale bool `gorm:"default:false;index" json:"stale,omitempty"`

	// Provenance of the most recent sighting.
	CrawlJobID  string    `gorm:"type:varchar(36);index;default:''" json:"crawl_job_id,omitempty"`
	PageID      uint      `gorm:"index;default:0" json:"page_id,omitempty"`
	Runs        int       `gorm:"default:0" json:"runs"` // runs that have recorded a value for it
	FirstSeenAt time.Time `json:"first_seen_at,omitempty"`
	LastSeenAt  time.Time `json:"last_seen_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// FieldObservation is one compared field of one listing at the end of one
// crawl run: the time series behind both the comparison table and the
// per-listing history chart.
//
// ListingID is what the series hangs off, so a listing's history is
// unbroken by a URL change and moves whole when the listing is reassigned
// to another group. ProductGroupID is carried too, but only as the group
// the listing was in at the time — it is rewritten in place when a listing
// moves, and a row with none is an observation of a listing nothing has
// matched yet.
//
// Numeric fields (price, rating, review_count…) fill NumValue; text fields
// (availability, seller, condition…) fill TextValue.
type FieldObservation struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ScopeType      string    `gorm:"type:varchar(16);index:idx_fo_scope,priority:1;not null" json:"scope_type"`
	ScopeID        string    `gorm:"type:varchar(36);index:idx_fo_scope,priority:2;not null" json:"scope_id"`
	ListingID      uint      `gorm:"index:idx_fo_listing,priority:1;default:0" json:"listing_id,omitempty"`
	ProductGroupID string    `gorm:"type:varchar(36);index:idx_fo_group,priority:1;default:''" json:"product_group_id,omitempty"`
	Field          string    `gorm:"type:varchar(64);index:idx_fo_group,priority:2;index:idx_fo_listing,priority:2;not null" json:"field"`
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
	ObservedAt     time.Time `gorm:"index:idx_fo_group,priority:3;index:idx_fo_listing,priority:3" json:"observed_at"`
}

// Value returns the observation as a comparable number plus its display
// text, so callers do not branch on the field's kind.
func (o FieldObservation) Value() (num float64, hasNum bool, text string) {
	if o.NumValue != nil {
		return *o.NumValue, true, o.TextValue
	}
	return 0, false, o.TextValue
}

// FieldObservationDay is one listing's field rolled up to one day: what
// raw observations older than the raw-retention window are compacted into,
// so a schedule can keep years of price history without keeping a row per
// run per field forever.
//
// Open/Close are the day's first and last value and Low/High its extremes,
// which is what a long-range chart needs; Samples says how many raw rows
// were folded in. Compaction never deletes a day it has not written.
type FieldObservationDay struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ScopeType      string    `gorm:"type:varchar(16);index:idx_fod_scope,priority:1;not null" json:"scope_type"`
	ScopeID        string    `gorm:"type:varchar(36);index:idx_fod_scope,priority:2;not null" json:"scope_id"`
	ListingID      uint      `gorm:"uniqueIndex:idx_fod_listing,priority:1;not null" json:"listing_id"`
	Field          string    `gorm:"type:varchar(64);uniqueIndex:idx_fod_listing,priority:2;not null" json:"field"`
	Day            time.Time `gorm:"type:date;uniqueIndex:idx_fod_listing,priority:3;not null" json:"day"`
	ProductGroupID string    `gorm:"type:varchar(36);index;default:''" json:"product_group_id,omitempty"`
	SiteKey        string    `gorm:"type:varchar(191);default:''" json:"site_key,omitempty"`
	OpenValue      *float64  `json:"open_value,omitempty"`
	CloseValue     *float64  `json:"close_value,omitempty"`
	LowValue       *float64  `json:"low_value,omitempty"`
	HighValue      *float64  `json:"high_value,omitempty"`
	TextValue      string    `gorm:"type:varchar(512);default:''" json:"text_value,omitempty"` // the day's last text value
	Currency       string    `gorm:"type:varchar(8);default:''" json:"currency,omitempty"`
	Samples        int       `gorm:"default:0" json:"samples"`
}

// ListingChange is one step in a listing's history where a tracked field
// actually moved: the product-level change signal, keyed to the listing
// rather than to a URL in one run. It is derived from consecutive
// observations, which is why it survives a URL change where the
// crawl-level PageChange does not.
type ListingChange struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ScopeType      string    `gorm:"type:varchar(16);index:idx_lc_scope,priority:1;not null" json:"scope_type"`
	ScopeID        string    `gorm:"type:varchar(36);index:idx_lc_scope,priority:2;not null" json:"scope_id"`
	ListingID      uint      `gorm:"index:idx_lc_listing,priority:1;not null" json:"listing_id"`
	Field          string    `gorm:"type:varchar(64);index:idx_lc_listing,priority:2;not null" json:"field"`
	ProductGroupID string    `gorm:"type:varchar(36);index;default:''" json:"product_group_id,omitempty"`
	CrawlJobID     string    `gorm:"type:varchar(36);index;not null" json:"crawl_job_id"`
	SiteKey        string    `gorm:"type:varchar(191);default:''" json:"site_key,omitempty"`
	Title          string    `gorm:"type:varchar(512);default:''" json:"title,omitempty"`
	URL            string    `gorm:"type:varchar(2048);default:''" json:"url,omitempty"`
	OldNum         *float64  `json:"old_num,omitempty"`
	NewNum         *float64  `json:"new_num,omitempty"`
	OldText        string    `gorm:"type:varchar(512);default:''" json:"old_text,omitempty"`
	NewText        string    `gorm:"type:varchar(512);default:''" json:"new_text,omitempty"`
	Currency       string    `gorm:"type:varchar(8);default:''" json:"currency,omitempty"`
	DetectedAt     time.Time `gorm:"index" json:"detected_at"`
}

// Direction reports which way a numeric change went: 1 up, -1 down, 0 for
// a text change or no numeric movement.
func (c ListingChange) Direction() int {
	if c.OldNum == nil || c.NewNum == nil {
		return 0
	}
	switch {
	case *c.NewNum > *c.OldNum:
		return 1
	case *c.NewNum < *c.OldNum:
		return -1
	}
	return 0
}

// Delta is the numeric movement (0 for a text change).
func (c ListingChange) Delta() float64 {
	if c.OldNum == nil || c.NewNum == nil {
		return 0
	}
	return *c.NewNum - *c.OldNum
}
