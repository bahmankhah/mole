package jobs

import (
	"sort"
	"time"

	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/models"
	"gorm.io/gorm"
)

// ── reading a listing's series ──────────────────────────────────────────

// latestObservationPerListing returns each listing's most recent value of
// one field, in a single query.
//
// The newest row is taken as the one with the highest id, which holds
// because observations are only ever inserted in run order (a rerun of the
// same pass updates its run's row in place rather than appending) and a
// scope only ever matches its newest run.
func latestObservationPerListing(tx *gorm.DB, listingIDs []uint, field string) (map[uint]models.FieldObservation, error) {
	out := map[uint]models.FieldObservation{}
	if len(listingIDs) == 0 {
		return out, nil
	}
	sub := tx.Model(&models.FieldObservation{}).
		Select("MAX(id) AS id").
		Where("listing_id IN ? AND field = ?", listingIDs, field).
		Group("listing_id")
	var rows []models.FieldObservation
	if err := tx.Model(&models.FieldObservation{}).
		Where("id IN (?)", sub).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, o := range rows {
		out[o.ListingID] = o
	}
	return out, nil
}

// HistoryPoint is one value in a listing's history. A point either came
// from a single run (Rolled false) or from a day that raw runs were
// compacted into (Rolled true), in which case Low and High bound what the
// value did inside that day and Samples says how many runs it covers.
type HistoryPoint struct {
	At       time.Time `json:"at"`
	Num      *float64  `json:"num,omitempty"`
	Text     string    `json:"text,omitempty"`
	Currency string    `json:"currency,omitempty"`
	Rolled   bool      `json:"rolled,omitempty"`
	Low      *float64  `json:"low,omitempty"`
	High     *float64  `json:"high,omitempty"`
	Samples  int       `json:"samples,omitempty"`
}

// ListingHistory is one listing's tracked history of one field: the series
// the chart draws, the moves worth calling out, and how trustworthy the
// listing's identity is over that span.
type ListingHistory struct {
	Listing   models.Listing       `json:"listing"`
	Group     *models.ProductGroup `json:"group,omitempty"`
	Field     string               `json:"field"`
	FieldKind string               `json:"field_kind"`
	Fields    []string             `json:"fields"` // the scope's other tracked fields, for the switcher

	Points  []HistoryPoint         `json:"points"`
	Changes []models.ListingChange `json:"changes"`

	// Summary over the span, for the header.
	First     *float64 `json:"first,omitempty"`
	Current   *float64 `json:"current,omitempty"`
	Low       *float64 `json:"low,omitempty"`
	High      *float64 `json:"high,omitempty"`
	Currency  string   `json:"currency,omitempty"`
	Runs      int      `json:"runs"`
	KeyNote   string   `json:"key_note,omitempty"` // what the identity rests on
	KeyStable bool     `json:"key_stable"`         // false = a URL change would restart this history
	Tracks    bool     `json:"tracks"`             // false = a one-off crawl, so there is nothing to track
}

// maxHistoryPoints bounds a history response.
const maxHistoryPoints = 1000

// GetListingHistory assembles one listing's history of one field, reading
// the compacted days before the raw runs so a long span stays complete
// after old raw rows have been rolled up.
func (m *Manager) GetListingHistory(listingID uint, field string) (*ListingHistory, error) {
	var l models.Listing
	if err := m.db.First(&l, "id = ?", listingID).Error; err != nil {
		return nil, notFound(err)
	}
	plan, err := m.planFor(groupScope{Type: l.ScopeType, ID: l.ScopeID})
	if err != nil {
		return nil, err
	}
	if field == "" || !containsString(plan.compareFields, field) {
		field = plan.primaryField()
	}
	out := &ListingHistory{
		Listing: l, Field: field, Fields: plan.compareFields,
		FieldKind: content.FieldKindOf(field),
		Runs:      l.Runs,
		KeyNote:   describeListingKey(l.KeyMethod),
		KeyStable: models.StableListingKey(l.KeyMethod),
		Tracks:    models.ScopeTracks(l.ScopeType),
	}
	if out.FieldKind == "" {
		out.FieldKind = content.FieldKindText
	}
	if l.ProductGroupID != "" {
		var g models.ProductGroup
		if err := m.db.First(&g, "id = ?", l.ProductGroupID).Error; err == nil {
			out.Group = &g
		}
	}

	var days []models.FieldObservationDay
	if err := m.db.Where("listing_id = ? AND field = ?", l.ID, field).
		Order("day ASC").Find(&days).Error; err != nil {
		return nil, err
	}
	for _, d := range days {
		out.Points = append(out.Points, HistoryPoint{
			At: d.Day, Num: d.CloseValue, Text: d.TextValue, Currency: d.Currency,
			Rolled: true, Low: d.LowValue, High: d.HighValue, Samples: d.Samples,
		})
	}

	var raw []models.FieldObservation
	if err := m.db.Where("listing_id = ? AND field = ?", l.ID, field).
		Order("observed_at ASC, id ASC").Limit(maxHistoryPoints).Find(&raw).Error; err != nil {
		return nil, err
	}
	for _, o := range raw {
		out.Points = append(out.Points, HistoryPoint{
			At: o.ObservedAt, Num: o.NumValue, Text: o.TextValue, Currency: o.Currency,
		})
	}
	sort.Slice(out.Points, func(a, b int) bool { return out.Points[a].At.Before(out.Points[b].At) })

	for _, pt := range out.Points {
		if pt.Currency != "" && out.Currency == "" {
			out.Currency = pt.Currency
		}
		if pt.Num == nil {
			continue
		}
		if out.First == nil {
			v := *pt.Num
			out.First = &v
		}
		v := *pt.Num
		out.Current = &v
		low := pt.Num
		if pt.Low != nil {
			low = pt.Low
		}
		high := pt.Num
		if pt.High != nil {
			high = pt.High
		}
		if out.Low == nil || *low < *out.Low {
			x := *low
			out.Low = &x
		}
		if out.High == nil || *high > *out.High {
			x := *high
			out.High = &x
		}
	}

	if err := m.db.Where("listing_id = ? AND field = ?", l.ID, field).
		Order("detected_at DESC").Limit(100).Find(&out.Changes).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ── human decisions on a listing ───────────────────────────────────────

// PinListing marks a listing as a confirmed match: future regroups keep it
// in this group even if automatic matching would place it elsewhere.
func (m *Manager) PinListing(id uint) error {
	return m.updateListing(id, map[string]interface{}{"pinned": true})
}

// UnpinListing lets automatic matching place the listing again.
func (m *Manager) UnpinListing(id uint) error {
	return m.updateListing(id, map[string]interface{}{"pinned": false})
}

// ExcludeListing marks a listing as not this product: it leaves the
// comparison table and future regroups never place it in any group of this
// scope again. The row and its history are kept, so the decision is
// remembered and reversible.
func (m *Manager) ExcludeListing(id uint) error {
	return m.updateListing(id, map[string]interface{}{"excluded": true, "pinned": false})
}

// UnexcludeListing makes the listing eligible for matching again.
func (m *Manager) UnexcludeListing(id uint) error {
	return m.updateListing(id, map[string]interface{}{"excluded": false})
}

func (m *Manager) updateListing(id uint, updates map[string]interface{}) error {
	var l models.Listing
	if err := m.db.First(&l, "id = ?", id).Error; err != nil {
		return notFound(err)
	}
	if err := m.db.Model(&models.Listing{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	if l.ProductGroupID == "" {
		return nil
	}
	plan, err := m.planFor(groupScope{Type: l.ScopeType, ID: l.ScopeID})
	if err != nil {
		return nil // the summary is refreshed by the next pass
	}
	return m.db.Transaction(func(tx *gorm.DB) error {
		return m.refreshProductGroupStats(tx, l.ProductGroupID, plan.primaryField())
	})
}

// MoveListing reassigns a listing to another product group of the same
// scope by hand and pins it there, so the next matching pass respects the
// decision.
//
// The listing's history moves with it whole: observations, rolled-up days
// and change events all hang off the listing id, so re-pointing the group
// is a single update per table and cannot lose or split a series — which
// is the point of giving a listing its own identity.
func (m *Manager) MoveListing(id uint, targetProductGroupID string) error {
	var l models.Listing
	if err := m.db.First(&l, "id = ?", id).Error; err != nil {
		return notFound(err)
	}
	var target models.ProductGroup
	if err := m.db.First(&target, "id = ? AND scope_type = ? AND scope_id = ?",
		targetProductGroupID, l.ScopeType, l.ScopeID).Error; err != nil {
		return notFound(err)
	}
	plan, err := m.planFor(groupScope{Type: l.ScopeType, ID: l.ScopeID})
	if err != nil {
		return err
	}
	previous := l.ProductGroupID
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Listing{}).Where("id = ?", l.ID).Updates(map[string]interface{}{
			"product_group_id": targetProductGroupID, "pinned": true,
			"match_method": models.MatchByManual, "confidence": 1, "excluded": false,
		}).Error; err != nil {
			return err
		}
		for _, model := range []interface{}{
			&models.FieldObservation{}, &models.FieldObservationDay{}, &models.ListingChange{},
		} {
			if err := tx.Model(model).Where("listing_id = ?", l.ID).
				Update("product_group_id", targetProductGroupID).Error; err != nil {
				return err
			}
		}
		if err := m.refreshProductGroupStats(tx, targetProductGroupID, plan.primaryField()); err != nil {
			return err
		}
		if previous != "" && previous != targetProductGroupID {
			return m.refreshProductGroupStats(tx, previous, plan.primaryField())
		}
		return nil
	})
}

// ── compaction ─────────────────────────────────────────────────────────

// compactDaysPerPass bounds how many days one compaction pass folds, so a
// backlog is worked through over several passes instead of in one long
// transaction.
const compactDaysPerPass = 14

// CompactObservations folds raw observations older than the configured raw
// retention window into one row per listing per field per day, then
// deletes the raw rows it folded.
//
// Tracking implies long horizons, and a daily schedule over a large
// catalogue writes a row per listing per field per run forever. Keeping
// open/close/low/high per day is what a long-range chart actually draws,
// at a fraction of the rows. Compaction is deliberately not a
// write-only-on-change scheme: "the price held at this value for six
// weeks" is a fact the series has to be able to state, and dropping
// unchanged points would lose it.
func (m *Manager) CompactObservations() (rolled, deleted int, err error) {
	days := m.config.Grouping.ObservationRetentionDays
	if days <= 0 {
		return 0, 0, nil // compaction off: raw rows are kept for good
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Truncate(24 * time.Hour)
	for pass := 0; pass < compactDaysPerPass; pass++ {
		var oldest time.Time
		row := m.db.Model(&models.FieldObservation{}).
			Select("MIN(observed_at)").Where("observed_at < ?", cutoff).Row()
		var found *time.Time
		if err := row.Scan(&found); err != nil || found == nil {
			break
		}
		oldest = found.UTC()
		dayStart := time.Date(oldest.Year(), oldest.Month(), oldest.Day(), 0, 0, 0, 0, time.UTC)
		dayEnd := dayStart.AddDate(0, 0, 1)
		n, d, err := m.compactDay(dayStart, dayEnd)
		if err != nil {
			return rolled, deleted, err
		}
		rolled += n
		deleted += d
		if d == 0 {
			break // nothing moved; stop rather than spin on the same day
		}
	}
	m.pruneRolledDays()
	return rolled, deleted, nil
}

// compactDay folds one UTC day's raw observations into FieldObservationDay
// rows and removes them. A day is folded in one transaction, so a crash
// cannot leave a day half-rolled and half-deleted.
func (m *Manager) compactDay(dayStart, dayEnd time.Time) (rolled, deleted int, err error) {
	var raw []models.FieldObservation
	if err := m.db.Where("observed_at >= ? AND observed_at < ?", dayStart, dayEnd).
		Order("listing_id ASC, field ASC, observed_at ASC, id ASC").Find(&raw).Error; err != nil {
		return 0, 0, err
	}
	if len(raw) == 0 {
		return 0, 0, nil
	}
	type key struct {
		listingID uint
		field     string
	}
	folded := map[key]*models.FieldObservationDay{}
	var order []key
	for _, o := range raw {
		// An observation with no listing predates listings and cannot be
		// attributed to a series; it is dropped with the day.
		if o.ListingID == 0 {
			continue
		}
		k := key{o.ListingID, o.Field}
		day, ok := folded[k]
		if !ok {
			day = &models.FieldObservationDay{
				ScopeType: o.ScopeType, ScopeID: o.ScopeID,
				ListingID: o.ListingID, Field: o.Field, Day: dayStart,
				ProductGroupID: o.ProductGroupID, SiteKey: o.SiteKey,
				OpenValue: o.NumValue, Currency: o.Currency,
			}
			folded[k] = day
			order = append(order, k)
		}
		day.Samples++
		day.CloseValue = o.NumValue
		if o.TextValue != "" {
			day.TextValue = o.TextValue
		}
		if o.Currency != "" {
			day.Currency = o.Currency
		}
		if o.ProductGroupID != "" {
			day.ProductGroupID = o.ProductGroupID
		}
		if o.NumValue == nil {
			continue
		}
		if day.LowValue == nil || *o.NumValue < *day.LowValue {
			v := *o.NumValue
			day.LowValue = &v
		}
		if day.HighValue == nil || *o.NumValue > *day.HighValue {
			v := *o.NumValue
			day.HighValue = &v
		}
	}
	err = m.db.Transaction(func(tx *gorm.DB) error {
		for _, k := range order {
			day := folded[k]
			// A day may already exist if a previous pass was interrupted
			// after writing it but before deleting its raw rows; merging
			// keeps compaction idempotent.
			var existing []models.FieldObservationDay
			if err := tx.Where("listing_id = ? AND field = ? AND day = ?", day.ListingID, day.Field, day.Day).
				Limit(1).Find(&existing).Error; err != nil {
				return err
			}
			if len(existing) > 0 {
				e := existing[0]
				merged := mergeRolledDay(e, *day)
				if err := tx.Model(&models.FieldObservationDay{}).Where("id = ?", e.ID).
					Updates(map[string]interface{}{
						"open_value": merged.OpenValue, "close_value": merged.CloseValue,
						"low_value": merged.LowValue, "high_value": merged.HighValue,
						"text_value": merged.TextValue, "currency": merged.Currency,
						"samples": merged.Samples, "product_group_id": merged.ProductGroupID,
					}).Error; err != nil {
					return err
				}
				rolled++
				continue
			}
			if err := tx.Create(day).Error; err != nil {
				return err
			}
			rolled++
		}
		res := tx.Where("observed_at >= ? AND observed_at < ?", dayStart, dayEnd).
			Delete(&models.FieldObservation{})
		if res.Error != nil {
			return res.Error
		}
		deleted = int(res.RowsAffected)
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return rolled, deleted, nil
}

// mergeRolledDay folds a freshly computed day into one already stored,
// keeping the earlier open and the later close.
func mergeRolledDay(stored, fresh models.FieldObservationDay) models.FieldObservationDay {
	out := stored
	if out.OpenValue == nil {
		out.OpenValue = fresh.OpenValue
	}
	if fresh.CloseValue != nil {
		out.CloseValue = fresh.CloseValue
	}
	if fresh.LowValue != nil && (out.LowValue == nil || *fresh.LowValue < *out.LowValue) {
		out.LowValue = fresh.LowValue
	}
	if fresh.HighValue != nil && (out.HighValue == nil || *fresh.HighValue > *out.HighValue) {
		out.HighValue = fresh.HighValue
	}
	if fresh.TextValue != "" {
		out.TextValue = fresh.TextValue
	}
	if fresh.Currency != "" {
		out.Currency = fresh.Currency
	}
	if fresh.ProductGroupID != "" {
		out.ProductGroupID = fresh.ProductGroupID
	}
	out.Samples += fresh.Samples
	return out
}

// pruneRolledDays drops rolled-up days and change events older than the
// configured history horizon. Zero keeps them for good.
func (m *Manager) pruneRolledDays() {
	days := m.config.Grouping.HistoryRetentionDays
	if days <= 0 {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	m.db.Where("day < ?", cutoff).Delete(&models.FieldObservationDay{})
	m.db.Where("detected_at < ?", cutoff).Delete(&models.ListingChange{})
}
