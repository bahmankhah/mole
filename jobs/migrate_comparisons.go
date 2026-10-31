package jobs

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/resolver/crawler/models"
	"gorm.io/gorm"
)

// A "comparison" used to be an entity of its own: a list of sites, each
// crawled as its own job, with its own cron and its own matching settings.
// It is now just a crawl — or a schedule — given several start URLs, which
// is the same thing with one fewer concept and with everything schedules
// already have (skip-if-running, webhooks, run history, change detection).
//
// MigrateComparisons converts each saved comparison into a schedule and
// moves its matched products, members and recorded history over to it, so
// nothing anyone collected is lost. Comparisons that had no cron become
// disabled schedules: their table and history stay readable, and pressing
// "Run now" starts them again.
//
// The crawls a comparison already ran are kept as ordinary crawls and are
// NOT attached to the new schedule: they were one run per site, which is
// not what a run of the new schedule is, and attaching them would make the
// next matching pass read one site's old crawl as if it were the whole
// round.
//
// It is safe to run on every start: a converted comparison is deleted, so
// the second run finds nothing to do.
func (m *Manager) MigrateComparisons() error {
	if !m.db.Migrator().HasTable("job_groups") {
		return nil
	}
	var groups []legacyComparison
	if err := m.db.Find(&groups).Error; err != nil {
		return fmt.Errorf("read comparisons: %w", err)
	}
	if len(groups) == 0 {
		return nil
	}
	log.Printf("[Migrate] Converting %d comparison(s) into schedules with several start URLs", len(groups))
	failed := 0
	for i := range groups {
		// One comparison that cannot be converted must not hold up the
		// rest: it is left untouched for the next start to retry.
		if err := m.migrateComparison(&groups[i]); err != nil {
			failed++
			log.Printf("[Migrate] Comparison %q (%s) was left as it is: %v", groups[i].Name, groups[i].ID, err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d comparison(s) could not be converted", failed, len(groups))
	}
	return nil
}

// legacyComparison is the job_groups row, read without the model that
// described it (the model is gone; the table outlives it until the last
// row is converted).
type legacyComparison struct {
	ID             string
	Name           string
	OwnerRef       string
	ExternalID     string
	Sites          []byte
	Settings       []byte
	Cron           string
	Timezone       string
	Enabled        bool
	MatchMode      string
	MatchThreshold float64
	GroupByVariant bool
	CompareFields  []byte
}

func (legacyComparison) TableName() string { return "job_groups" }

// legacyComparisonSite is one site of a comparison.
type legacyComparisonSite struct {
	ID        string          `json:"id"`
	Label     string          `json:"label"`
	TargetURL string          `json:"target_url"`
	SeedURLs  []string        `json:"seed_urls"`
	MaxDepth  *int            `json:"max_depth"`
	Settings  json.RawMessage `json:"settings"`
	Disabled  bool            `json:"disabled"`
}

func (m *Manager) migrateComparison(g *legacyComparison) error {
	var sites []legacyComparisonSite
	if len(g.Sites) > 0 {
		if err := json.Unmarshal(g.Sites, &sites); err != nil {
			return fmt.Errorf("unreadable site list: %w", err)
		}
	}
	template, siteDomains, err := m.templateFromComparison(g, sites)
	if err != nil {
		return err
	}
	cron, enabled := strings.TrimSpace(g.Cron), g.Enabled
	if cron == "" {
		// A one-off comparison becomes a schedule that does not fire until
		// its owner says so; its table and history are there to read.
		cron, enabled = "0 3 * * *", false
	}
	timezone := g.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	schedule := &models.Schedule{
		Name:       g.Name,
		OwnerRef:   g.OwnerRef,
		ExternalID: g.ExternalID,
		Cron:       cron,
		Timezone:   timezone,
		Enabled:    enabled,
		// A comparison never diffed pages between runs; leave that off so
		// the first run after the move does not suddenly report every page
		// as new.
		ChangeDetection: false,
		SkipIfRunning:   true,
		Template:        template,
	}
	if enabled {
		if next, err := NextRun(cron, timezone, time.Now()); err == nil {
			schedule.NextRunAt = &next
		} else {
			log.Printf("[Migrate] Comparison %q had an unusable cron %q; its schedule starts disabled", g.Name, cron)
			schedule.Enabled, schedule.NextRunAt = false, nil
		}
	}

	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(schedule).Error; err != nil {
			return err
		}
		// GORM leaves out false booleans that have a column default.
		if err := tx.Model(schedule).Updates(map[string]interface{}{
			"enabled": schedule.Enabled, "change_detection": false, "skip_if_running": true,
		}).Error; err != nil {
			return err
		}
		// The comparison's own site ids become the sites' domains, which
		// is how a multi-site crawl tells its sites apart.
		for siteID, domain := range siteDomains {
			for _, table := range []string{"product_group_members", "field_observations"} {
				if err := tx.Table(table).
					Where("scope_type = ? AND scope_id = ? AND site_key = ?", legacyComparisonScope, g.ID, siteID).
					Updates(map[string]interface{}{"scope_type": models.ScopeSchedule, "scope_id": schedule.ID, "site_key": domain, "site_label": domain}).
					Error; err != nil {
					return err
				}
			}
		}
		// Anything still on the old scope (a site deleted from the list
		// before the move) keeps its own key and moves across too, so its
		// history is not orphaned.
		for _, table := range []string{"product_group_members", "field_observations", "product_groups"} {
			if err := tx.Table(table).
				Where("scope_type = ? AND scope_id = ?", legacyComparisonScope, g.ID).
				Updates(map[string]interface{}{"scope_type": models.ScopeSchedule, "scope_id": schedule.ID}).
				Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&models.CrawlJob{}).Where("group_id = ?", g.ID).
			Updates(map[string]interface{}{"group_id": nil}).Error; err != nil {
			return err
		}
		if err := tx.Table("job_groups").Where("id = ?", g.ID).Delete(nil).Error; err != nil {
			return err
		}
		log.Printf("[Migrate] Comparison %q is now schedule %s with %d start URL(s)%s",
			g.Name, schedule.ID, 1+len(template.SeedURLs), map[bool]string{true: "", false: " (disabled)"}[schedule.Enabled])
		return nil
	})
}

// legacyComparisonScope is the scope_type rows of a comparison carry.
const legacyComparisonScope = "comparison"

// templateFromComparison turns a comparison's sites and settings into the
// job template of one crawl that covers them all, and reports which domain
// each of its site ids becomes.
func (m *Manager) templateFromComparison(g *legacyComparison, sites []legacyComparisonSite) (models.JobTemplate, map[string]string, error) {
	var settings models.JobSettings
	if len(g.Settings) > 0 {
		if err := json.Unmarshal(g.Settings, &settings); err != nil {
			return models.JobTemplate{}, nil, fmt.Errorf("unreadable settings: %w", err)
		}
	}
	var urls []string
	var withOwnSettings []string
	domains := map[string]string{}
	seenDomain := map[string]string{}
	for _, site := range sites {
		if strings.TrimSpace(site.TargetURL) == "" {
			continue
		}
		full, domain, err := ParseTarget(site.TargetURL)
		if err != nil {
			log.Printf("[Migrate] Comparison %q: site %q has an unusable URL and is left out: %v", g.Name, site.TargetURL, err)
			continue
		}
		domains[site.ID] = domain
		if other, dup := seenDomain[domain]; dup {
			log.Printf("[Migrate] Comparison %q: sites %q and %q are both on %s, so their histories now share one column",
				g.Name, other, site.Label, domain)
		}
		seenDomain[domain] = site.Label
		if len(site.Settings) > 0 && string(site.Settings) != "null" {
			withOwnSettings = append(withOwnSettings, site.Label)
		}
		if site.Disabled {
			continue // kept out of the crawl, but its history still moves across
		}
		urls = append(urls, full)
		urls = append(urls, site.SeedURLs...)
		if site.MaxDepth != nil && settings.MaxDepth == nil {
			depth := *site.MaxDepth
			settings.MaxDepth = &depth
		}
	}
	if len(withOwnSettings) > 0 {
		log.Printf("[Migrate] Comparison %q: the per-site settings of %s are dropped — one crawl has one set of settings. "+
			"Give such a site a schedule of its own if it needs different settings.", g.Name, strings.Join(withOwnSettings, ", "))
	}
	if len(urls) == 0 {
		return models.JobTemplate{}, nil, fmt.Errorf("no usable site URL")
	}

	on := true
	settings.GroupProducts = &on
	byVariant := g.GroupByVariant
	settings.GroupByVariant = &byVariant
	if g.MatchThreshold > 0 {
		threshold := g.MatchThreshold
		settings.MatchThreshold = &threshold
	}
	if mode := strings.TrimSpace(g.MatchMode); mode != "" {
		settings.MatchMode = &mode
	}
	if len(g.CompareFields) > 0 {
		var fields []string
		if err := json.Unmarshal(g.CompareFields, &fields); err == nil && len(fields) > 0 {
			settings.CompareFields = fields
		}
	}
	return models.JobTemplate{
		Kind:      models.JobKindCrawl,
		TargetURL: urls[0],
		SeedURLs:  urls[1:],
		Settings:  &settings,
	}, domains, nil
}
