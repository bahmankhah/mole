package jobs

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/resolver/crawler/crawler"
	"github.com/resolver/crawler/models"
	"gorm.io/gorm"
)

// TagCount is how many of a job's pages carry a tag.
type TagCount struct {
	Tag     string `json:"tag"`
	Kept    int64  `json:"kept"`
	Skipped int64  `json:"skipped"`
}

// TagSummary counts a job's pages by tag and in total.
type TagSummary struct {
	Tags    []TagCount `json:"tags"` // most kept pages first
	Kept    int64      `json:"kept"`
	Skipped int64      `json:"skipped"`
}

// TagCounts counts a job's pages per tag, kept and skipped apart.
func (m *Manager) TagCounts(jobID string) (*TagSummary, error) {
	if _, err := m.GetJob(jobID); err != nil {
		return nil, notFound(err)
	}
	type row struct {
		Tag     string
		Skipped bool
		N       int64
	}
	var rows []row
	err := m.db.Raw(`SELECT pt.tag AS tag, cp.skipped AS skipped, COUNT(*) AS n
		FROM page_tags pt JOIN crawled_pages cp ON cp.id = pt.page_id
		WHERE pt.crawl_job_id = ? AND cp.is_archived = ?
		GROUP BY pt.tag, cp.skipped`, jobID, false).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byTag := map[string]*TagCount{}
	for _, r := range rows {
		c := byTag[r.Tag]
		if c == nil {
			c = &TagCount{Tag: r.Tag}
			byTag[r.Tag] = c
		}
		if r.Skipped {
			c.Skipped += r.N
		} else {
			c.Kept += r.N
		}
	}
	out := &TagSummary{Tags: []TagCount{}}
	for _, c := range byTag {
		out.Tags = append(out.Tags, *c)
	}
	sort.Slice(out.Tags, func(i, j int) bool {
		a, b := out.Tags[i], out.Tags[j]
		if a.Kept != b.Kept {
			return a.Kept > b.Kept
		}
		if a.Skipped != b.Skipped {
			return a.Skipped > b.Skipped
		}
		return a.Tag < b.Tag
	})
	var totals []row
	m.db.Model(&models.CrawledPage{}).Select("skipped, COUNT(*) AS n").
		Where("crawl_job_id = ? AND is_archived = ?", jobID, false).Group("skipped").Scan(&totals)
	for _, t := range totals {
		if t.Skipped {
			out.Skipped = t.N
		} else {
			out.Kept = t.N
		}
	}
	return out, nil
}

// SetTagRules replaces a job's tag rules and re-tags the pages it already
// crawled, without fetching them again. Whether a page was kept does not
// change: skipped pages have no content to add. It returns how many pages
// got different tags.
func (m *Manager) SetTagRules(jobID string, rules []models.TagRule) (int, error) {
	job, err := m.GetJob(jobID)
	if err != nil {
		return 0, notFound(err)
	}
	if job.Status == models.JobStatusRunning {
		return 0, invalidState("pause the job before changing its tag rules")
	}
	if _, err := crawler.CompileTagRules(rules); err != nil {
		return 0, invalidInput("%v", err)
	}
	settings := models.JobSettings{}
	if job.Settings != nil {
		settings = *job.Settings
	}
	for i := range rules {
		rules[i].Tag = models.NormalizeTag(rules[i].Tag)
	}
	settings.TagRules = rules
	if err := m.ValidateSettings(&settings, job.Limits, true); err != nil {
		return 0, err
	}
	var value interface{}
	if !settings.IsEmpty() {
		raw, err := json.Marshal(settings)
		if err != nil {
			return 0, fmt.Errorf("encode settings: %w", err)
		}
		value = string(raw)
	}
	if err := m.db.Model(&models.CrawlJob{}).Where("id = ?", jobID).Update("settings", value).Error; err != nil {
		return 0, err
	}
	return m.retag(jobID, rules)
}

// retag recomputes the type and tags of a job's pages from their URL and
// detected type.
func (m *Manager) retag(jobID string, rules []models.TagRule) (int, error) {
	compiled, err := crawler.CompileTagRules(rules)
	if err != nil {
		return 0, invalidInput("%v", err)
	}
	changed := 0
	var after uint
	for {
		var rows []models.CrawledPage
		if err := m.db.Select("id, url, final_url, detected_type, page_type, tags").
			Where("crawl_job_id = ? AND is_archived = ? AND id > ?", jobID, false, after).
			Order("id").Limit(500).Find(&rows).Error; err != nil {
			return changed, err
		}
		if len(rows) == 0 {
			return changed, nil
		}
		err := m.db.Transaction(func(tx *gorm.DB) error {
			for _, r := range rows {
				pageType, tags := crawler.TagPage(compiled, r.URL, r.FinalURL, r.DetectedType)
				if pageType == r.PageType && sameTags(tags, r.Tags) {
					continue
				}
				changed++
				if err := tx.Model(&models.CrawledPage{}).Where("id = ?", r.ID).
					Updates(map[string]interface{}{"page_type": pageType, "tags": models.StringSlice(tags)}).Error; err != nil {
					return err
				}
				if err := tx.Where("page_id = ?", r.ID).Delete(&models.PageTag{}).Error; err != nil {
					return err
				}
				if len(tags) > 0 {
					list := make([]models.PageTag, len(tags))
					for i, t := range tags {
						list[i] = models.PageTag{CrawlJobID: jobID, PageID: r.ID, Tag: t}
					}
					if err := tx.Create(&list).Error; err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return changed, err
		}
		after = rows[len(rows)-1].ID
	}
}

func sameTags(a []string, b models.StringSlice) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, []string(b))
}
