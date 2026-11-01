package jobs

import (
	"log"
	"time"

	"github.com/resolver/crawler/models"
	"gorm.io/gorm"
)

// MigrateListings finishes the move to listings that database.prepareListings
// began: it gives every listing carried over from product_group_members a
// real identity, attaches the recorded history to it, and creates listings
// for the offers that used to have history but no row at all.
//
// It is idempotent and cheap to call when there is nothing to do, which is
// how it is invoked on every boot.
func (m *Manager) MigrateListings() error {
	pending, err := m.listingsMigrationPending()
	if err != nil || !pending {
		return err
	}
	log.Println("[Migrate] Deriving listing identities and attaching recorded history…")
	if err := m.deriveLegacyListingKeys(); err != nil {
		return err
	}
	// Both of the next two steps find observations by the tuple the old
	// schema keyed them by, whose last column is a 2KB URL with no index.
	// Without help the database compares every observation against every
	// listing in the scope; with it, a catalogue-sized migration drops
	// from minutes to seconds. It is of no use afterwards, so it goes
	// again as soon as they are done.
	m.createLegacyAttachIndex()
	defer m.dropLegacyAttachIndex()
	if err := m.attachObservationsToListings(); err != nil {
		return err
	}
	if err := m.listingsForOrphanObservations(); err != nil {
		return err
	}
	if err := m.collapseDuplicateRunValues(); err != nil {
		return err
	}
	if err := m.backfillListingCounters(); err != nil {
		return err
	}
	log.Println("[Migrate] Listings migration complete")
	return nil
}

// listingsMigrationPending reports whether anything is left to migrate: a
// listing without a derived identity, or an observation not yet attached
// to one.
func (m *Manager) listingsMigrationPending() (bool, error) {
	var listings, orphans int64
	if err := m.db.Model(&models.Listing{}).Where("key_method = '' OR key_method IS NULL").Count(&listings).Error; err != nil {
		return false, err
	}
	if listings > 0 {
		return true, nil
	}
	err := m.db.Model(&models.FieldObservation{}).
		Where("(listing_id = 0 OR listing_id IS NULL) AND scope_type IN ?",
			[]string{models.ScopeSchedule, models.ScopeCrawl}).
		Count(&orphans).Error
	if err != nil {
		return false, err
	}
	return orphans > 0, nil
}

// legacyKeyBatch is how many listings one transaction derives keys for.
// Each derivation is a single-row UPDATE, and committing them one at a
// time costs a disk sync apiece — enough to turn a large catalogue into an
// hour of blocked startup. Batching makes it a handful of syncs.
const legacyKeyBatch = 1000

// deriveLegacyListingKeys replaces the provisional per-row identity with a
// real one derived from what a carried-over listing actually records: its
// site, its URL and its variant. That is the ListingByPath or ListingByURL
// key — the strong per-product keys need the page's own data, which the
// next crawl supplies, adopting and upgrading these in place.
//
// Two carried-over listings can derive the same key, because that is the
// case the move exists to fix: the same offer filed twice under two URLs
// that differ only by a rewritten slug. Those are merged, history and all.
func (m *Manager) deriveLegacyListingKeys() error {
	plans := map[groupScope]groupPlan{}
	derived, merged := 0, 0
	for {
		var legacy []models.Listing
		if err := m.db.Where("key_method = '' OR key_method IS NULL").
			Order("id ASC").Limit(legacyKeyBatch).Find(&legacy).Error; err != nil {
			return err
		}
		if len(legacy) == 0 {
			break
		}
		d, mg, err := m.deriveKeyBatch(legacy, plans)
		if err != nil {
			return err
		}
		derived += d
		merged += mg
		if d+mg == 0 {
			// Nothing could be done with this batch; stop rather than
			// read the same rows for ever.
			break
		}
	}
	if derived+merged > 0 {
		log.Printf("[Migrate] Derived %d listing identities, merged %d duplicates of the same offer", derived, merged)
	}
	return nil
}

// deriveKeyBatch keys one batch in a single transaction. The clash lookup
// runs against the database rather than a map, so a listing that collides
// with one keyed in an earlier batch is merged just the same — lowest id
// first, so which row survives does not depend on where the batches fell.
func (m *Manager) deriveKeyBatch(rows []models.Listing, plans map[groupScope]groupPlan) (derived, merged int, err error) {
	err = m.db.Transaction(func(tx *gorm.DB) error {
		for i := range rows {
			l := &rows[i]
			parts := listingKeyFor(l.SiteKey, l.URL, "", "", "", "", l.VariantKey)
			hash := listingHash(parts.key)
			var clash []models.Listing
			if err := tx.Where("scope_type = ? AND scope_id = ? AND listing_hash = ? AND id <> ?",
				l.ScopeType, l.ScopeID, hash, l.ID).Limit(1).Find(&clash).Error; err != nil {
				return err
			}
			if len(clash) > 0 {
				if err := m.mergeListings(tx, &clash[0], l, plans); err != nil {
					return err
				}
				merged++
				continue
			}
			if err := tx.Model(&models.Listing{}).Where("id = ?", l.ID).Updates(map[string]interface{}{
				"listing_key": parts.key, "listing_hash": hash, "key_method": parts.method,
			}).Error; err != nil {
				return err
			}
			derived++
		}
		return nil
	})
	return derived, merged, err
}

// planFor, cached: a merge needs the scope's primary field to refresh a
// group summary, and reading the schedule back for every merge is wasteful.
func (m *Manager) cachedPlan(scope groupScope, plans map[groupScope]groupPlan) (groupPlan, bool) {
	if p, ok := plans[scope]; ok {
		return p, p.scope.ID != ""
	}
	p, err := m.planFor(scope)
	if err != nil {
		plans[scope] = groupPlan{}
		return groupPlan{}, false
	}
	plans[scope] = p
	return p, true
}

// mergeListings folds dupe into keep: its history moves across, the human
// decisions made on either are preserved, and its URL joins the trail of
// URLs the offer has lived at.
func (m *Manager) mergeListings(tx *gorm.DB, keep, dupe *models.Listing, plans map[groupScope]groupPlan) error {
	for _, model := range []interface{}{
		&models.FieldObservation{}, &models.FieldObservationDay{}, &models.ListingChange{},
	} {
		if err := tx.Model(model).Where("listing_id = ?", dupe.ID).
			Update("listing_id", keep.ID).Error; err != nil {
			return err
		}
	}
	updates := map[string]interface{}{}
	if dupe.Pinned && !keep.Pinned {
		keep.Pinned, updates["pinned"] = true, true
	}
	if dupe.Excluded && !keep.Excluded {
		keep.Excluded, updates["excluded"] = true, true
	}
	if !dupe.Stale && keep.Stale {
		keep.Stale, updates["stale"] = false, false
	}
	if dupe.LastSeenAt.After(keep.LastSeenAt) {
		keep.LastSeenAt, updates["last_seen_at"] = dupe.LastSeenAt, dupe.LastSeenAt
		// The offer was last seen at the other row's URL, so that is where
		// it lives now and the kept row's URL is the older one.
		if dupe.URL != keep.URL {
			trail := append([]string(keep.PreviousURLs), keep.URL)
			updates["url"], updates["previous_urls"] = dupe.URL, models.StringSlice(trail)
			keep.URL, keep.PreviousURLs = dupe.URL, trail
		}
	} else if dupe.URL != keep.URL {
		trail := append([]string(keep.PreviousURLs), dupe.URL)
		updates["previous_urls"] = models.StringSlice(trail)
		keep.PreviousURLs = trail
	}
	if keep.ProductGroupID == "" && dupe.ProductGroupID != "" {
		keep.ProductGroupID, updates["product_group_id"] = dupe.ProductGroupID, dupe.ProductGroupID
	}
	if len(updates) > 0 {
		if err := tx.Model(&models.Listing{}).Where("id = ?", keep.ID).Updates(updates).Error; err != nil {
			return err
		}
	}
	if err := tx.Delete(&models.Listing{}, "id = ?", dupe.ID).Error; err != nil {
		return err
	}
	// The group lost a row, so its summary and count are now wrong.
	if dupe.ProductGroupID == "" {
		return nil
	}
	plan, ok := m.cachedPlan(groupScope{Type: dupe.ScopeType, ID: dupe.ScopeID}, plans)
	if !ok {
		return nil // the next matching pass refreshes it
	}
	return m.refreshProductGroupStats(tx, dupe.ProductGroupID, plan.primaryField())
}

// legacyAttachIndex covers the tuple the pre-listing schema filed
// observations by. Column prefixes keep it inside the index-length limit;
// they only widen the candidate set, and the query still compares the
// columns in full.
const legacyAttachIndex = "CREATE INDEX idx_fo_legacy_attach ON field_observations " +
	"(scope_type, scope_id, site_key(100), variant_key(100), url(190))"

func (m *Manager) createLegacyAttachIndex() {
	// An index left behind by an interrupted run is fine: the create fails
	// and the existing one does the same job.
	if err := m.db.Exec(legacyAttachIndex).Error; err != nil {
		log.Printf("[Migrate] Could not build the temporary attach index (the migration will be slower): %v", err)
	}
}

func (m *Manager) dropLegacyAttachIndex() {
	m.db.Exec("DROP INDEX idx_fo_legacy_attach ON field_observations")
}

// attachObservationsToListings points every recorded value at the listing
// it belongs to, matching on the tuple the old schema keyed by — which is
// exact for rows written before the move, since that tuple is precisely
// how those observations were filed.
func (m *Manager) attachObservationsToListings() error {
	res := m.db.Exec(`
		UPDATE field_observations fo
		JOIN listings l
		  ON l.scope_type = fo.scope_type AND l.scope_id = fo.scope_id
		 AND l.site_key = fo.site_key AND l.variant_key = fo.variant_key AND l.url = fo.url
		SET fo.listing_id = l.id
		WHERE fo.listing_id = 0 OR fo.listing_id IS NULL`)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[Migrate] Attached %d recorded values to their listing", res.RowsAffected)
	}
	return nil
}

// orphanBatch bounds how many offers one pass adopts, so a large backlog
// is worked through over several boots rather than in one transaction.
const orphanBatch = 2000

// listingsForOrphanObservations creates a listing for each offer that has
// recorded history but never had a row: under the old schema a product
// nothing matched was given observations and no member, so its history was
// kept but could not be tracked, pinned or charted. Those offers become
// listings with no group — exactly what the dashboard now offers for
// assignment by hand, with their history already attached.
func (m *Manager) listingsForOrphanObservations() error {
	for pass := 0; pass < 20; pass++ {
		var orphans []struct {
			ScopeType  string
			ScopeID    string
			SiteKey    string
			SiteLabel  string
			VariantKey string
			URL        string
			Title      string
			CrawlJobID string
			PageID     uint
			FirstAt    time.Time
			LastAt     time.Time
			Runs       int
		}
		err := m.db.Model(&models.FieldObservation{}).
			Select(`scope_type, scope_id, site_key, MAX(site_label) AS site_label, variant_key, url,
			        MAX(title) AS title, MAX(crawl_job_id) AS crawl_job_id, MAX(page_id) AS page_id,
			        MIN(observed_at) AS first_at, MAX(observed_at) AS last_at,
			        COUNT(DISTINCT crawl_job_id) AS runs`).
			Where("(listing_id = 0 OR listing_id IS NULL) AND scope_type IN ?",
				[]string{models.ScopeSchedule, models.ScopeCrawl}).
			Group("scope_type, scope_id, site_key, variant_key, url").
			Limit(orphanBatch).Scan(&orphans).Error
		if err != nil {
			return err
		}
		if len(orphans) == 0 {
			return nil
		}
		// One transaction for the batch: a row at a time would cost a
		// disk sync per offer, which is what made the first cut of this
		// migration unusably slow on a real catalogue.
		err = m.db.Transaction(func(tx *gorm.DB) error {
			for _, o := range orphans {
				parts := listingKeyFor(o.SiteKey, o.URL, "", "", "", "", o.VariantKey)
				hash := listingHash(parts.key)
				var found []models.Listing
				if err := tx.Where("scope_type = ? AND scope_id = ? AND listing_hash = ?",
					o.ScopeType, o.ScopeID, hash).Limit(1).Find(&found).Error; err != nil {
					return err
				}
				id := uint(0)
				if len(found) > 0 {
					id = found[0].ID
				} else {
					l := models.Listing{
						ScopeType: o.ScopeType, ScopeID: o.ScopeID,
						ListingKey: parts.key, ListingHash: hash, KeyMethod: parts.method,
						SiteKey: o.SiteKey, SiteLabel: o.SiteLabel, VariantKey: o.VariantKey,
						Title: o.Title, URL: o.URL,
						CrawlJobID: o.CrawlJobID, PageID: o.PageID, Runs: o.Runs,
						FirstSeenAt: o.FirstAt, LastSeenAt: o.LastAt,
						// Not seen by a completed pass under the new schema
						// yet, so it is marked stale until one confirms it.
						Stale: true,
					}
					if err := tx.Create(&l).Error; err != nil {
						return err
					}
					id = l.ID
				}
				if err := tx.Model(&models.FieldObservation{}).
					Where("scope_type = ? AND scope_id = ? AND site_key = ? AND variant_key = ? AND url = ? AND (listing_id = 0 OR listing_id IS NULL)",
						o.ScopeType, o.ScopeID, o.SiteKey, o.VariantKey, o.URL).
					Update("listing_id", id).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		log.Printf("[Migrate] Adopted %d offers that had history but no listing", len(orphans))
	}
	return nil
}

// collapseDuplicateRunValues keeps one recorded value per listing per
// field per run.
//
// The old schema filed a value per *page*, so a shop reached by three URLs
// recorded three values in one run and the table folded them when it drew
// the cell. Those three pages are now one listing, and a chart that plots
// them as three points would invent movement inside a single run. The one
// kept is the cheapest — what the cell showed then and what it shows now —
// and for a text field the most recent.
//
// Going forward recordObservations writes one row per (listing, run,
// field), so this only ever has the carried-over history to clean up.
func (m *Manager) collapseDuplicateRunValues() error {
	// A self-join over (listing, run, field) compares every row against
	// every other and turns a modest catalogue into minutes of work. One
	// ranked pass sorts instead of pairing: numeric rows first, cheapest
	// first, newest first among texts — so rank 1 is the row to keep and
	// everything above it goes.
	res := m.db.Exec(`
		DELETE FROM field_observations
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY listing_id, crawl_job_id, field
					ORDER BY (num_value IS NULL), num_value ASC, id DESC
				) AS rn
				FROM field_observations
				WHERE listing_id > 0
			) ranked
			WHERE ranked.rn > 1
		)`)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[Migrate] Collapsed %d same-run duplicate values into one per listing", res.RowsAffected)
	}
	return nil
}

// backfillListingCounters fills in what a listing carries of its own —
// its run count, when it was first and last seen, and its title — from the
// history now attached to it. A member row carried none of these.
func (m *Manager) backfillListingCounters() error {
	res := m.db.Exec(`
		UPDATE listings l
		JOIN (SELECT listing_id,
		             COUNT(DISTINCT crawl_job_id) AS runs,
		             MIN(observed_at) AS first_at,
		             MAX(observed_at) AS last_at,
		             MAX(title) AS title
		      FROM field_observations WHERE listing_id > 0 GROUP BY listing_id) t
		  ON l.id = t.listing_id
		SET l.runs = t.runs,
		    l.title = CASE WHEN l.title IS NULL OR l.title = '' THEN t.title ELSE l.title END,
		    l.first_seen_at = CASE WHEN l.first_seen_at IS NULL OR l.first_seen_at < '1971-01-01'
		                           THEN t.first_at ELSE l.first_seen_at END,
		    l.last_seen_at = CASE WHEN l.last_seen_at IS NULL OR l.last_seen_at < '1971-01-01'
		                          THEN t.last_at ELSE l.last_seen_at END
		WHERE l.runs = 0`)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[Migrate] Filled in run counts, titles and timestamps for %d listings", res.RowsAffected)
	}
	return nil
}
