package jobs

import (
	"log"

	"github.com/resolver/crawler/models"
)

// onGroupJobEnded regroups the scope a finished crawl belongs to: its
// schedule (so history survives reruns), or the crawl itself. Called from
// onRunEnded, and cheap to call on every completion — a crawl that does
// not group its products returns at once.
func (m *Manager) onGroupJobEnded(job *models.CrawlJob) {
	scopeType, scopeID, grouped := ScopeForJob(job)
	if !grouped || !m.config.Grouping.Enabled {
		return
	}
	m.loops.Add(1)
	go func() {
		defer m.loops.Done()
		// This is the pass that produces the run's final table, so it
		// waits out a pass already running (one started while the crawl
		// was still going, say) instead of leaving the last pages out.
		var res RegroupResult
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			res, err = m.Regroup(scopeType, scopeID, RegroupOptions{})
			if err != nil || !res.Locked {
				break
			}
		}
		switch {
		case err != nil:
			log.Printf("[Grouping] Scope %s/%s: regroup failed: %v", scopeType, scopeID, err)
		case res.Reason != "":
			log.Printf("[Grouping] Scope %s/%s: nothing grouped — %s", scopeType, scopeID, res.Reason)
		default:
			log.Printf("[Grouping] Scope %s/%s: %d listings in %d groups (%d new, %d merged) from %d pages, %d values recorded, %d changed, %d moved URL, %d retired",
				scopeType, scopeID, res.Candidates, res.Groups, res.Created, res.Merged,
				res.ProductPages, res.Observations, res.Changes, res.URLChanges, res.Retired)
			m.emitListingChanges(job)
		}
	}()
}

// maxListingChangeEvents bounds how many per-change events one run emits,
// so a catalogue-wide price move cannot flood the outbox. The run's own
// counts are on the job.changes event regardless.
const maxListingChangeEvents = 500

// emitListingChanges sends one event per tracked value that moved in this
// run. It is called only from the final pass of a finished crawl: the
// change rows are written once, by whichever pass first recorded the new
// value, so reading them back by crawl job emits each exactly once.
func (m *Manager) emitListingChanges(job *models.CrawlJob) {
	if job.Webhook == nil || !wantsEvent(job.Webhook, EventListingChanged) {
		return
	}
	var changes []models.ListingChange
	if err := m.db.Where("crawl_job_id = ?", job.ID).
		Order("id ASC").Limit(maxListingChangeEvents).Find(&changes).Error; err != nil {
		log.Printf("[Grouping] Could not read listing changes of job %s: %v", job.ID, err)
		return
	}
	for _, c := range changes {
		m.emit(job, EventListingChanged, map[string]interface{}{
			"listing_id": c.ListingID, "product_group_id": c.ProductGroupID,
			"field": c.Field, "site_key": c.SiteKey, "title": c.Title, "url": c.URL,
			"old_num": c.OldNum, "new_num": c.NewNum,
			"old_text": c.OldText, "new_text": c.NewText,
			"currency": c.Currency, "direction": c.Direction(), "delta": c.Delta(),
			"detected_at": c.DetectedAt,
		})
	}
}
