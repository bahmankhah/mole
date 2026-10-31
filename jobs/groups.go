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
			log.Printf("[Grouping] Scope %s/%s: %d products in %d groups (%d new, %d merged) from %d pages, %d values recorded, %d retired",
				scopeType, scopeID, res.Candidates, res.Groups, res.Created, res.Merged, res.ProductPages, res.Observations, res.Retired)
		}
	}()
}
