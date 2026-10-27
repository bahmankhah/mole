package jobs

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/storage"
)

// JobFilter narrows job listings.
type JobFilter struct {
	OwnerRef   string
	ExternalID string
	ScheduleID string
	Status     string
	Cursor     string // opaque, from a previous page
	Limit      int
}

// ListJobs returns jobs newest first with cursor pagination.
func (m *Manager) ListJobs(f JobFilter) ([]models.CrawlJob, string, error) {
	limit := clampLimit(f.Limit, 50, 200)
	q := m.db.Model(&models.CrawlJob{})
	if f.OwnerRef != "" {
		q = q.Where("owner_ref = ?", f.OwnerRef)
	}
	if f.ExternalID != "" {
		q = q.Where("external_id = ?", f.ExternalID)
	}
	if f.ScheduleID != "" {
		q = q.Where("schedule_id = ?", f.ScheduleID)
	}
	if f.Status != "" {
		q = q.Where("status IN ?", strings.Split(f.Status, ","))
	}
	if f.Cursor != "" {
		ts, id, err := decodeJobCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		q = q.Where("(created_at < ?) OR (created_at = ? AND id < ?)", ts, ts, id)
	}
	var jobs []models.CrawlJob
	if err := q.Order("created_at DESC").Order("id DESC").Limit(limit + 1).Find(&jobs).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(jobs) > limit {
		jobs = jobs[:limit]
		last := jobs[len(jobs)-1]
		next = fmt.Sprintf("%d_%s", last.CreatedAt.UnixMicro(), last.ID)
	}
	return jobs, next, nil
}

func decodeJobCursor(c string) (time.Time, string, error) {
	parts := strings.SplitN(c, "_", 2)
	if len(parts) != 2 {
		return time.Time{}, "", invalidInput("invalid cursor")
	}
	us, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", invalidInput("invalid cursor")
	}
	return time.UnixMicro(us), parts[1], nil
}

func clampLimit(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// PageResult is a crawled page with its derived content.
type PageResult struct {
	models.CrawledPage
	Body    *string             `json:"body,omitempty"`
	Content *models.PageContent `json:"content,omitempty"`
}

// PageQuery selects pages of a job.
type PageQuery struct {
	JobID          string
	AfterID        uint // cursor: pages after this id in the chosen order
	Limit          int
	WithContent    bool
	WithBody       bool
	OnlySuccessful bool
	OnlyFailed     bool // non-2xx responses and fetch errors
	NewestFirst    bool // descending ids; the cursor then means "older than"
}

// ListPages returns pages in id order with an id cursor; next is 0 at the end.
func (m *Manager) ListPages(q PageQuery) ([]PageResult, uint, error) {
	limit := clampLimit(q.Limit, 50, 500)
	db := m.db.Where("crawl_job_id = ? AND is_archived = ?", q.JobID, false)
	order := "id ASC"
	switch {
	case q.NewestFirst && q.AfterID > 0:
		db = db.Where("id < ?", q.AfterID)
		order = "id DESC"
	case q.NewestFirst:
		order = "id DESC"
	default:
		db = db.Where("id > ?", q.AfterID)
	}
	if q.OnlySuccessful {
		db = db.Where("status_code BETWEEN 200 AND 299 AND error_message = ''")
	}
	if q.OnlyFailed {
		db = db.Where("(status_code NOT BETWEEN 200 AND 299 OR error_message <> '')")
	}
	if !q.WithBody {
		db = db.Omit("TextContent")
	}
	var pages []models.CrawledPage
	if err := db.Order(order).Limit(limit + 1).Find(&pages).Error; err != nil {
		return nil, 0, err
	}
	var next uint
	if len(pages) > limit {
		pages = pages[:limit]
		next = pages[len(pages)-1].ID
	}
	out := make([]PageResult, len(pages))
	ids := make([]uint, len(pages))
	for i, p := range pages {
		out[i] = PageResult{CrawledPage: p}
		ids[i] = p.ID
		if q.WithBody {
			out[i].Body = m.pageBody(&pages[i])
			out[i].TextContent = nil
		}
	}
	if q.WithContent && len(ids) > 0 {
		var contents []models.PageContent
		m.db.Where("page_id IN ?", ids).Find(&contents)
		byPage := make(map[uint]*models.PageContent, len(contents))
		for i := range contents {
			byPage[contents[i].PageID] = &contents[i]
		}
		for i := range out {
			out[i].Content = byPage[out[i].ID]
		}
	}
	return out, next, nil
}

// pageBody returns the stored body from MySQL or the object store.
func (m *Manager) pageBody(p *models.CrawledPage) *string {
	if p.TextContent != nil {
		return p.TextContent
	}
	if p.BodyKey == "" || m.engine.Store() == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := m.engine.Store().Get(ctx, p.BodyKey)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

// GetPageResult returns one page with body and content.
func (m *Manager) GetPageResult(jobID string, pageID uint) (*PageResult, error) {
	var p models.CrawledPage
	if err := m.db.Where("id = ? AND crawl_job_id = ? AND is_archived = ?", pageID, jobID, false).First(&p).Error; err != nil {
		return nil, notFound(err)
	}
	res := &PageResult{CrawledPage: p, Body: m.pageBody(&p)}
	res.TextContent = nil
	var c models.PageContent
	if err := m.db.Where("page_id = ?", p.ID).First(&c).Error; err == nil {
		res.Content = &c
	}
	return res, nil
}

// GetPageContent returns the derived content row of a page, if any.
func (m *Manager) GetPageContent(pageID uint) *models.PageContent {
	var c models.PageContent
	if err := m.db.Where("page_id = ?", pageID).First(&c).Error; err != nil {
		return nil
	}
	return &c
}

// GetScreenshot returns a page screenshot PNG.
func (m *Manager) GetScreenshot(jobID string, pageID uint) ([]byte, error) {
	c := m.GetPageContent(pageID)
	if c == nil || c.CrawlJobID != jobID || c.ScreenshotKey == "" || m.engine.Store() == nil {
		return nil, fmt.Errorf("%w", ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := m.engine.Store().Get(ctx, c.ScreenshotKey)
	if err == storage.ErrNotFound {
		return nil, fmt.Errorf("%w", ErrNotFound)
	}
	return b, err
}

// ListChanges returns change-detection results of a job with an id cursor.
func (m *Manager) ListChanges(jobID, changeType string, afterID uint, limit int, withDiff bool) ([]models.PageChange, uint, error) {
	limit = clampLimit(limit, 50, 500)
	q := m.db.Where("crawl_job_id = ? AND id > ?", jobID, afterID)
	if changeType != "" {
		q = q.Where("change_type = ?", changeType)
	}
	if !withDiff {
		q = q.Omit("Diff")
	}
	var out []models.PageChange
	if err := q.Order("id ASC").Limit(limit + 1).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	var next uint
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// ChangeCounts counts changes by type for a job.
func (m *Manager) ChangeCounts(jobID string) map[string]int64 {
	type row struct {
		ChangeType string
		N          int64
	}
	var rows []row
	m.db.Model(&models.PageChange{}).Select("change_type, COUNT(*) AS n").Where("crawl_job_id = ?", jobID).Group("change_type").Scan(&rows)
	out := map[string]int64{models.ChangeNew: 0, models.ChangeChanged: 0, models.ChangeRemoved: 0}
	for _, r := range rows {
		out[r.ChangeType] = r.N
	}
	return out
}

// Export formats.
const (
	ExportJSON  = "json"
	ExportJSONL = "jsonl"
	ExportCSV   = "csv"
)

// exportRecord is one page in an export.
type exportRecord struct {
	PageID       uint                   `json:"page_id"`
	URL          string                 `json:"url"`
	FinalURL     string                 `json:"final_url,omitempty"`
	StatusCode   int                    `json:"status_code"`
	Title        string                 `json:"title,omitempty"`
	ContentType  string                 `json:"content_type,omitempty"`
	Depth        int                    `json:"depth"`
	CrawledAt    time.Time              `json:"crawled_at"`
	Error        string                 `json:"error,omitempty"`
	Markdown     *string                `json:"markdown,omitempty"`
	Text         *string                `json:"text,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Links        []string               `json:"links,omitempty"`
	Extracted    json.RawMessage        `json:"extracted,omitempty"`
	LLMExtracted json.RawMessage        `json:"llm_extracted,omitempty"`
}

// Export streams a job's pages (with content) as JSON, JSON Lines or CSV.
// CSV starts with a UTF-8 BOM so Excel shows Persian text correctly; nested
// extraction results are flattened into "extracted.<field>" columns.
func (m *Manager) Export(jobID, format string, onlySuccessful bool, w io.Writer) error {
	if _, err := m.GetJob(jobID); err != nil {
		return notFound(err)
	}
	switch format {
	case ExportJSON, ExportJSONL, ExportCSV:
	default:
		return invalidInput("unknown export format %q (json, jsonl, csv)", format)
	}

	var records []exportRecord
	var after uint
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	first := true
	if format == ExportJSON {
		io.WriteString(w, "[")
	}
	for {
		pages, next, err := m.ListPages(PageQuery{JobID: jobID, AfterID: after, Limit: 500, WithContent: true, OnlySuccessful: onlySuccessful})
		if err != nil {
			return err
		}
		for _, p := range pages {
			rec := toExportRecord(p)
			switch format {
			case ExportJSONL:
				if err := enc.Encode(rec); err != nil {
					return err
				}
			case ExportJSON:
				if !first {
					io.WriteString(w, ",")
				}
				first = false
				b, _ := json.Marshal(rec)
				w.Write(b)
			case ExportCSV:
				records = append(records, rec)
			}
		}
		if next == 0 {
			break
		}
		after = next
	}
	if format == ExportJSON {
		io.WriteString(w, "]")
		return nil
	}
	if format == ExportCSV {
		return writeCSV(w, records)
	}
	return nil
}

func toExportRecord(p PageResult) exportRecord {
	rec := exportRecord{
		PageID: p.ID, URL: p.URL, FinalURL: p.FinalURL, StatusCode: p.StatusCode, Title: p.Title,
		ContentType: p.ContentType, Depth: p.Depth, CrawledAt: p.CrawledAt, Error: p.ErrorMessage,
	}
	if c := p.Content; c != nil {
		rec.Markdown, rec.Text, rec.Links = c.Markdown, c.Text, c.Links
		rec.Metadata = c.Metadata
		if len(c.Extracted) > 0 {
			rec.Extracted = json.RawMessage(c.Extracted)
		}
		if len(c.LLMExtracted) > 0 {
			rec.LLMExtracted = json.RawMessage(c.LLMExtracted)
		}
	}
	return rec
}

func writeCSV(w io.Writer, records []exportRecord) error {
	base := []string{"page_id", "url", "final_url", "status_code", "title", "content_type", "depth", "crawled_at", "error"}
	rows := make([]map[string]string, len(records))
	extraCols := map[string]bool{}
	hasMarkdown := false
	for i, r := range records {
		row := map[string]string{
			"page_id": strconv.FormatUint(uint64(r.PageID), 10), "url": r.URL, "final_url": r.FinalURL,
			"status_code": strconv.Itoa(r.StatusCode), "title": r.Title, "content_type": r.ContentType,
			"depth": strconv.Itoa(r.Depth), "crawled_at": r.CrawledAt.Format(time.RFC3339), "error": r.Error,
		}
		if r.Markdown != nil {
			row["markdown"] = *r.Markdown
			hasMarkdown = true
		}
		for _, src := range []struct {
			prefix string
			raw    json.RawMessage
		}{{"extracted", r.Extracted}, {"llm", r.LLMExtracted}} {
			if len(src.raw) == 0 {
				continue
			}
			var v interface{}
			if json.Unmarshal(src.raw, &v) == nil {
				flatten(src.prefix, v, row, extraCols)
			}
		}
		if t, ok := r.Metadata["description"].(string); ok {
			row["description"] = t
			extraCols["description"] = true
		}
		rows[i] = row
	}
	cols := append([]string{}, base...)
	extra := make([]string, 0, len(extraCols))
	for c := range extraCols {
		extra = append(extra, c)
	}
	sort.Strings(extra)
	cols = append(cols, extra...)
	if hasMarkdown {
		cols = append(cols, "markdown")
	}

	io.WriteString(w, "\xef\xbb\xbf") // UTF-8 BOM
	cw := csv.NewWriter(w)
	if err := cw.Write(cols); err != nil {
		return err
	}
	for _, row := range rows {
		line := make([]string, len(cols))
		for i, c := range cols {
			line[i] = row[c]
		}
		if err := cw.Write(line); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// flatten turns nested JSON into dotted CSV columns; arrays of scalars are
// joined with " | ", other arrays are kept as JSON.
func flatten(prefix string, v interface{}, row map[string]string, cols map[string]bool) {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, sub := range t {
			flatten(prefix+"."+k, sub, row, cols)
		}
		return
	case []interface{}:
		parts := make([]string, 0, len(t))
		scalar := true
		for _, item := range t {
			switch it := item.(type) {
			case map[string]interface{}, []interface{}:
				scalar = false
			default:
				parts = append(parts, fmt.Sprint(it))
			}
		}
		if scalar {
			row[prefix] = strings.Join(parts, " | ")
		} else {
			b, _ := json.Marshal(t)
			row[prefix] = string(b)
		}
	case nil:
		row[prefix] = ""
	case float64:
		row[prefix] = strconv.FormatFloat(t, 'f', -1, 64)
	default:
		row[prefix] = fmt.Sprint(t)
	}
	cols[prefix] = true
}
