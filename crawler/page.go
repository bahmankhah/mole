package crawler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"github.com/resolver/crawler/plugins"
	"github.com/resolver/crawler/storage"
	"gorm.io/gorm/clause"
)

// crawl fetches and processes a single frontier entry.
func (r *Run) crawl(workerID int, fu *models.FrontierURL) {
	startTime := time.Now()
	pageURL := fu.URL
	depth := fu.Depth
	log.Printf("[Run %s/%d] >>> %s %s depth=%d", short(r.job.ID), workerID, requestMethodLabel(fu.RequestMethod), pageURL, depth)

	result := r.fetch(fu)
	if r.stopping() {
		// The run is pausing/stopping and the request was cut short; keep
		// the URL pending instead of recording a bogus failure.
		r.frontier.Release(fu.ID)
		return
	}
	if result.Error != nil {
		// A browser error with real content (e.g. "SPA shell not rendered
		// after retries" yet a >1KB 200 body) is a soft warning.
		hasUsableBody := result.StatusCode == 200 && len(result.Body) > 1024
		if !hasUsableBody {
			log.Printf("[Run %s/%d] Failed to fetch %s: %v", short(r.job.ID), workerID, pageURL, result.Error)
			pi := r.describe(fu, result, nil, false)
			_, created := r.saveCrawledPage(fu, result, 0, result.Error.Error(), startTime, nil, pi)
			r.countPage(created, pi)
			r.frontier.MarkFailed(fu.ID, fu.RetryCount, r.rs.MaxRetries)
			return
		}
		log.Printf("[Run %s/%d] Soft error fetching %s (continuing with %d bytes): %v", short(r.job.ID), workerID, pageURL, len(result.Body), result.Error)
	}

	// Server errors and rate limits are retriable.
	if result.StatusCode == 429 || result.StatusCode == 502 || result.StatusCode == 503 {
		pi := r.describe(fu, result, nil, false)
		_, created := r.saveCrawledPage(fu, result, int64(len(result.Body)), fmt.Sprintf("HTTP %d", result.StatusCode), startTime, result.Body, pi)
		r.countPage(created, pi)
		r.frontier.MarkFailed(fu.ID, fu.RetryCount, r.rs.MaxRetries)
		return
	}

	contentType := result.ContentType
	isDocument := content.DocumentKind(contentType, result.URL, result.Body) != ""
	if !isDocument && !isProcessableContentType(contentType) {
		pi := r.describe(fu, result, nil, false)
		_, created := r.saveCrawledPage(fu, result, 0, "", startTime, nil, pi)
		r.countPage(created, pi)
		r.frontier.MarkCompleted(fu.ID)
		return
	}

	body := result.Body
	pi := r.describe(fu, result, body, isDocument)
	page, created := r.saveCrawledPage(fu, result, int64(len(body)), "", startTime, body, pi)
	r.countPage(created, pi)
	r.frontier.MarkCompleted(fu.ID)

	// Documents have no links or phrases to process; they are only converted.
	// Skipped pages still give their links, so the crawl reaches kept pages.
	if !isDocument {
		r.processContent(workerID, pageURL, body, contentType, result.StatusCode, depth, page, fu.AnchorText, pi.skipped)
	}
	if pi.skipped {
		return
	}
	// Analyze only when the row holds this fetch (not a content-duplicate of another URL
	// and not an older, better copy that was kept).
	if page != nil && page.ID > 0 && page.URLHash == fu.URLHash && page.DocHash == docHashOf(body) && r.rs.NeedsContent() {
		r.analyzePage(page, result, pi)
	}

	if created && page != nil {
		r.e.emit(r.snapshotJob(), "page.crawled", map[string]interface{}{
			"page_id":     page.ID,
			"url":         pageURL,
			"status_code": result.StatusCode,
			"title":       page.Title,
			"depth":       depth,
			"page_type":   pi.pageType,
			"tags":        pi.tags,
		})
	}

	if r.rs.AfterCrawlPlugin != "" && !isDocument {
		pluginID := r.rs.AfterCrawlPlugin
		// Bound concurrent plugin processes; when all slots are busy the
		// worker waits, which slows the crawl instead of piling up processes.
		select {
		case r.pluginSlots <- struct{}{}:
		case <-r.ctx.Done():
			return
		}
		r.sideWG.Add(1)
		go func() {
			defer func() { <-r.pluginSlots; r.sideWG.Done() }()
			r.runAfterCrawlScript(pluginID, pageURL, result.StatusCode, contentType, depth, body)
		}()
	}
}

// countPage counts a newly created crawled_pages row (any status), matching
// how the counters are seeded from the database when a job resumes.
func (r *Run) countPage(created bool, pi *pageInfo) {
	if created {
		atomic.AddInt64(&r.crawled, 1)
		atomic.AddInt64(&r.uPages, 1)
		if !pi.skipped {
			atomic.AddInt64(&r.kept, 1)
		}
	}
}

// pageInfo is a fetched page's type, tags and whether it is kept.
type pageInfo struct {
	parsed   *content.Page // the parsed HTML of a 2xx HTML page, reused by the detectors
	detected string        // content.Classify's type; "document" for PDF/DOCX; "" otherwise
	signals  string
	pageType string // detected, or overridden by a tag rule
	tags     []string
	skipped  bool
}

// describe classifies a fetched page, tags it and decides whether it is kept.
// body is nil for errors and non-HTML responses: they are tagged by URL only.
func (r *Run) describe(fu *models.FrontierURL, res *modules.FetchResult, body []byte, isDocument bool) *pageInfo {
	pi := &pageInfo{}
	ok2xx := res.StatusCode >= 200 && res.StatusCode < 300
	finalURL := res.URL
	if finalURL == fu.URL {
		finalURL = ""
	}
	switch {
	case !ok2xx || len(body) == 0:
	case isDocument:
		pi.detected = models.PageTypeDocument
	case strings.Contains(strings.ToLower(res.ContentType), "html"):
		base := fu.URL
		if finalURL != "" {
			base = finalURL
		}
		if p, err := content.ParsePage(base, body); err == nil {
			c := p.ClassifyWith(r.templates)
			pi.parsed, pi.detected, pi.signals = p, c.Type, c.SignalString()
		}
	}
	pi.pageType, pi.tags = TagPage(r.rs.TagRules, fu.URL, finalURL, pi.detected)
	pi.skipped = !r.rs.Keeps(pi.tags)
	return pi
}

// saveTags replaces a page's rows in page_tags.
func (r *Run) saveTags(pageID uint, tags []string) {
	r.e.db.Where("page_id = ?", pageID).Delete(&models.PageTag{})
	if len(tags) == 0 {
		return
	}
	rows := make([]models.PageTag, len(tags))
	for i, t := range tags {
		rows[i] = models.PageTag{CrawlJobID: r.job.ID, PageID: pageID, Tag: t}
	}
	if err := r.e.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
		log.Printf("[Run %s] Failed to save tags of page %d: %v", short(r.job.ID), pageID, err)
	}
}

func docHashOf(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// processContent runs phrase detection, link extraction, sitemap and
// robots.txt handling and semantic embedding.
// Phrase matching and embedding only run for 2xx responses so error pages are not indexed.
func (r *Run) processContent(workerID int, pageURL string, body []byte, contentType string, statusCode int, depth int, page *models.CrawledPage, anchorText string, skipped bool) {
	isSuccessful := statusCode >= 200 && statusCode < 300 && !skipped
	textContent := ""
	if isSuccessful {
		textContent = r.linkExtractor.ExtractTextContent(body)
	}

	if isSuccessful && page != nil && page.ID > 0 {
		if r.rs.EnableWordExtraction {
			res, err := r.e.wordExtractor.ExtractAndStore(textContent, r.job.ID, page.ID, pageURL, r.phraseIDMap)
			if err != nil {
				log.Printf("[Run %s/%d] Word extraction error for %s: %v", short(r.job.ID), workerID, pageURL, err)
			} else if res != nil && res.MatchesStored > 0 {
				atomic.AddInt64(&r.matches, int64(res.MatchesStored))
			}
		}
		if !r.rs.UseCrawlPhrasesOnly {
			for _, m := range r.phraseDetector.DetectPhrases(textContent) {
				r.savePhraseMatch(page.ID, pageURL, m.Phrase, m.Context, m.Occurrences, models.MatchTypeContent)
			}
			for _, m := range r.phraseDetector.DetectPhrasesInURL(pageURL) {
				r.savePhraseMatch(page.ID, pageURL, m.Phrase, m.Context, m.Occurrences, models.MatchTypeURL)
			}
			if anchorText != "" {
				for _, m := range r.phraseDetector.DetectPhrasesInAnchor(anchorText) {
					r.savePhraseMatch(page.ID, pageURL, m.Phrase, "Anchor: "+m.Context, m.Occurrences, models.MatchTypeAnchor)
				}
			}
		}
	}

	// Links found here would sit at depth+1, beyond the crawl's maximum depth:
	// queueing them would only fill the frontier with URLs the run discards.
	followLinks := depth < r.rs.MaxDepth

	// Link extraction still runs for non-2xx pages: navigation links on error pages are useful.
	switch {
	case strings.Contains(contentType, "text/html"):
		if followLinks {
			r.processHTML(pageURL, body, depth)
		}
		if isSuccessful && r.rs.EnableSemanticSearch && page != nil && page.ID > 0 {
			r.sideWG.Add(1)
			go func(p *models.CrawledPage, text string) {
				defer r.sideWG.Done()
				if err := r.e.semanticSearcher.EmbedAndStore(r.sideCtx, p, text); err != nil {
					log.Printf("[SemanticSearch] Failed to embed page %d (%s): %v", p.ID, p.URL, err)
				}
			}(page, textContent)
		}
	case strings.Contains(pageURL, "sitemap") && strings.Contains(contentType, "xml"):
		if followLinks {
			r.processSitemap(pageURL, body, depth)
		}
	case strings.Contains(pageURL, "robots.txt"):
		r.processRobots(pageURL, body, depth, followLinks)
	}
}

// inScope reports whether a link may be followed: it must be on the base
// domain of one of the job's start URLs, or on one of the extra
// scope_domains. Giving a crawl several start URLs is what lets a single
// crawl (or schedule) cover several shops and compare them.
func (r *Run) inScope(rawURL string) bool {
	domain := r.urlCleaner.ExtractBaseDomain(rawURL)
	if domain == "" {
		return false
	}
	return domain == r.job.Domain || r.seedDomains[domain] || r.rs.ScopeDomains[domain]
}

// processHTML extracts same-site links into the frontier.
func (r *Run) processHTML(pageURL string, body []byte, depth int) {
	links, err := r.linkExtractor.ExtractLinksWithAnchors(pageURL, body)
	if err != nil {
		log.Printf("[Run %s] Failed to extract links from %s: %v", short(r.job.ID), pageURL, err)
		return
	}
	added := 0
	for _, la := range links {
		if !r.inScope(la.URL) {
			continue
		}
		if err := r.frontier.AddURLWithAnchor(la.URL, depth+1, pageURL, la.AnchorText); err == nil {
			added++
		}
	}
	log.Printf("[Run %s] Added %d/%d links from %s", short(r.job.ID), added, len(links), pageURL)
}

func (r *Run) processSitemap(pageURL string, body []byte, depth int) {
	urls, sitemaps, err := r.sitemapParser.ParseSitemap(body)
	if err != nil {
		log.Printf("[Run %s] Failed to parse sitemap %s: %v", short(r.job.ID), pageURL, err)
		return
	}
	for _, u := range urls {
		r.frontier.AddURL(u, depth+1, pageURL)
	}
	for _, s := range sitemaps {
		r.frontier.AddURL(s, depth+1, pageURL)
	}
}

// processRobots stores the robots.txt rules and, when followLinks is set,
// queues the sitemaps it lists.
func (r *Run) processRobots(pageURL string, body []byte, depth int, followLinks bool) {
	result := r.robotsParser.ParseRobots(pageURL, body)
	if domain := r.urlCleaner.ExtractDomain(pageURL); domain != "" {
		r.robotRulesMu.Lock()
		r.robotRules[domain] = result
		r.robotRulesMu.Unlock()
	}
	if !followLinks {
		return
	}
	for _, sitemap := range result.Sitemaps {
		r.frontier.AddURL(sitemap, depth+1, pageURL)
	}
}

// isDisallowedByRobots applies path-prefix matching; a longer Allow wins over a Disallow.
func (r *Run) isDisallowedByRobots(rawURL string) bool {
	domain := r.urlCleaner.ExtractDomain(rawURL)
	if domain == "" {
		return false
	}
	r.robotRulesMu.RLock()
	rules := r.robotRules[domain]
	r.robotRulesMu.RUnlock()
	if rules == nil {
		return false
	}
	urlPath := extractPathFromURL(rawURL)
	if urlPath == "" {
		urlPath = "/"
	}
	longestDisallow := ""
	for _, p := range rules.DisallowedPaths {
		if p != "" && strings.HasPrefix(urlPath, p) && len(p) > len(longestDisallow) {
			longestDisallow = p
		}
	}
	if longestDisallow == "" {
		return false
	}
	for _, p := range rules.AllowedPaths {
		if p != "" && strings.HasPrefix(urlPath, p) && len(p) > len(longestDisallow) {
			return false
		}
	}
	return true
}

// isProcessableContentType checks if a Content-Type header indicates content
// worth reading and processing (HTML, XML, plain text, JSON).
func isProcessableContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	ct := strings.ToLower(contentType)
	for _, p := range []string{"text/html", "application/xhtml+xml", "application/xml", "text/xml", "text/plain",
		"application/json", "application/rss+xml", "application/atom+xml"} {
		if strings.Contains(ct, p) {
			return true
		}
	}
	return false
}

// isTextualContentType reports whether the response body is worth storing when
// Save Text Content is enabled. Covers HTML/plain/XML plus JSON API payloads.
func isTextualContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	ct := strings.ToLower(contentType)
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json", "application/ld+json", "application/problem+json", "application/xml",
		"application/xhtml+xml", "application/javascript", "application/x-javascript",
		"application/rss+xml", "application/atom+xml":
		return true
	}
	return strings.HasSuffix(ct, "+json") || strings.HasSuffix(ct, "+xml")
}

// extractPathFromURL extracts just the path component from a URL string.
func extractPathFromURL(rawURL string) string {
	idx := strings.Index(rawURL, "://")
	if idx == -1 {
		return "/"
	}
	rest := rawURL[idx+3:]
	slashIdx := strings.Index(rest, "/")
	if slashIdx == -1 {
		return "/"
	}
	p := rest[slashIdx:]
	if q := strings.Index(p, "?"); q != -1 {
		p = p[:q]
	}
	if f := strings.Index(p, "#"); f != -1 {
		p = p[:f]
	}
	return p
}

// saveCrawledPage records a fetched page. It returns the page and whether a new row was created.
func (r *Run) saveCrawledPage(fu *models.FrontierURL, res *modules.FetchResult, contentLength int64, errorMsg string, startTime time.Time, body []byte, pi *pageInfo) (*models.CrawledPage, bool) {
	pageURL := fu.URL
	var normalizedURL string
	if modules.HasMeaningfulFragment(pageURL) {
		normalizedURL, _ = r.urlCleaner.ProcessURLKeepFragment(pageURL)
	} else {
		normalizedURL, _ = r.urlCleaner.ProcessURL(pageURL)
	}
	urlHash := fu.URLHash
	if urlHash == "" {
		urlHash = r.urlCleaner.HashURL(normalizedURL)
	}

	docHash := ""
	if len(body) > 0 {
		h := sha256.Sum256(body)
		docHash = hex.EncodeToString(h[:])
	}
	// Unrendered AngularJS shells all hash the same; never let them block real pages via content dedup.
	isSPAShell := res.FetchedWith == "browser" && len(body) > 0 &&
		bytes.Contains(body, []byte("ng-app=")) && !bytes.Contains(body, []byte("ng-scope"))
	jobID := r.job.ID

	if docHash != "" && r.rs.SkipContentDuplicates && !isSPAShell {
		var existing models.CrawledPage
		if err := r.e.db.Omit("TextContent").Where("crawl_job_id = ? AND doc_hash = ? AND is_archived = ?", jobID, docHash, false).
			First(&existing).Error; err == nil {
			log.Printf("[Run %s] Skipping duplicate content (doc_hash=%s) for %s", short(jobID), docHash[:12], pageURL)
			return &existing, false
		}
	}

	finalURL := res.URL
	if finalURL == pageURL {
		finalURL = ""
	}
	page := &models.CrawledPage{
		CrawlJobID:    jobID,
		URL:           pageURL,
		Domain:        r.urlCleaner.ExtractBaseDomain(pageURL),
		URLHash:       urlHash,
		DocHash:       docHash,
		NormalizedURL: normalizedURL,
		StatusCode:    res.StatusCode,
		ContentType:   res.ContentType,
		ContentLength: contentLength,
		Depth:         fu.Depth,
		ParentURL:     fu.ParentURL,
		CrawledAt:     time.Now(),
		ResponseTime:  time.Since(startTime).Milliseconds(),
		ErrorMessage:  errorMsg,
		FetchedWith:   res.FetchedWith,
		FinalURL:      finalURL,
		DetectedType:  pi.detected,
		PageType:      pi.pageType,
		TypeSignals:   pi.signals,
		Tags:          models.StringSlice(pi.tags),
		Skipped:       pi.skipped,
	}
	if pi.skipped {
		page.SkipReason = models.SkipReasonKeepTags
	}
	if len(body) > 0 && strings.Contains(res.ContentType, "text/html") {
		page.Title = r.linkExtractor.ExtractTitle(body)
	}
	if r.rs.SaveTextContent && !pi.skipped && len(body) > 0 && isTextualContentType(res.ContentType) {
		if r.e.cfg.Storage.OffloadBodies && r.e.store != nil {
			key := storage.BodyKey(jobID, urlHash)
			if err := r.e.store.Put(r.sideCtx, key, body, res.ContentType); err == nil {
				page.BodyKey = key
			} else {
				log.Printf("[Run %s] Failed to offload body of %s: %v", short(jobID), pageURL, err)
			}
		}
		if page.BodyKey == "" {
			raw := string(body)
			page.TextContent = &raw
		}
	}

	result := r.e.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "crawl_job_id"}, {Name: "url_hash"}},
		DoNothing: true,
	}).Create(page)
	if result.Error != nil {
		log.Printf("[Run %s] Failed to save page %s: %v", short(jobID), pageURL, result.Error)
		return page, false
	}
	if result.RowsAffected > 0 {
		r.saveTags(page.ID, pi.tags)
		return page, true
	}

	// The row exists: replace it when the new fetch is better (bigger, or a success after an error).
	var existing models.CrawledPage
	if err := r.e.db.Omit("TextContent").Where("crawl_job_id = ? AND url_hash = ?", jobID, urlHash).First(&existing).Error; err != nil {
		return page, false
	}
	if contentLength > existing.ContentLength || (errorMsg == "" && existing.ErrorMessage != "") {
		updates := map[string]interface{}{
			"status_code":    res.StatusCode,
			"content_type":   res.ContentType,
			"content_length": contentLength,
			"doc_hash":       docHash,
			"response_time":  page.ResponseTime,
			"error_message":  errorMsg,
			"crawled_at":     page.CrawledAt,
			"fetched_with":   page.FetchedWith,
			"final_url":      page.FinalURL,
			"detected_type":  page.DetectedType,
			"page_type":      page.PageType,
			"type_signals":   page.TypeSignals,
			"tags":           page.Tags,
			"skipped":        page.Skipped,
			"skip_reason":    page.SkipReason,
		}
		// A kept page stays kept: its content row may already exist.
		if !existing.Skipped {
			updates["skipped"], updates["skip_reason"] = false, ""
			page.Skipped, page.SkipReason = false, ""
		} else if !page.Skipped {
			atomic.AddInt64(&r.kept, 1)
		}
		if page.Title != "" {
			updates["title"] = page.Title
		}
		if page.TextContent != nil {
			updates["text_content"] = *page.TextContent
		}
		if page.BodyKey != "" {
			updates["body_key"] = page.BodyKey
		}
		r.e.db.Model(&existing).Updates(updates)
		r.saveTags(existing.ID, pi.tags)
		existing.DocHash = docHash
		existing.Title = page.Title
		existing.DetectedType, existing.PageType, existing.Tags = page.DetectedType, page.PageType, page.Tags
		existing.Skipped, existing.SkipReason = page.Skipped, page.SkipReason
	}
	return &existing, false
}

// savePhraseMatch records a phrase match.
func (r *Run) savePhraseMatch(pageID uint, pageURL, phrase, matchContext string, occurrences int, matchType models.MatchType) {
	var searchPhraseID *uint
	if id, ok := r.phraseIDMap[strings.ToLower(phrase)]; ok {
		searchPhraseID = &id
	}
	m := &models.PhraseMatch{
		CrawlJobID:     r.job.ID,
		PageID:         pageID,
		SearchPhraseID: searchPhraseID,
		URL:            pageURL,
		Phrase:         phrase,
		MatchType:      matchType,
		Context:        matchContext,
		Occurrences:    occurrences,
		FoundAt:        time.Now(),
	}
	if err := r.e.db.Create(m).Error; err == nil {
		atomic.AddInt64(&r.matches, 1)
	}
}

// analyzePage produces the PageContent row: formats, extraction, screenshot
// and change detection.
func (r *Run) analyzePage(page *models.CrawledPage, res *modules.FetchResult, pi *pageInfo) {
	pc := models.PageContent{PageID: page.ID, CrawlJobID: r.job.ID, URLHash: page.URLHash, CreatedAt: time.Now()}
	isHTML := strings.Contains(strings.ToLower(res.ContentType), "html")
	ok2xx := res.StatusCode >= 200 && res.StatusCode < 300
	docKind := content.DocumentKind(res.ContentType, res.URL, res.Body)
	var markdown string

	if docKind != "" && ok2xx {
		if r.rs.convertsDocuments() {
			markdown = r.analyzeDocument(&pc, page, docKind, res)
		}
	} else if isHTML && ok2xx {
		f := r.rs.Formats
		wantMarkdown := f[models.FormatMarkdown] || r.rs.ChangeDetection || r.rs.LLMPrompt != ""
		analysis, err := content.Analyze(page.URL, res.Body, content.Options{
			Markdown:        wantMarkdown,
			HTML:            f[models.FormatHTML],
			Text:            f[models.FormatText],
			Links:           f[models.FormatLinks],
			Metadata:        f[models.FormatMetadata],
			OnlyMainContent: r.rs.OnlyMainContent,
		})
		if err != nil {
			pc.ExtractError = "analyze: " + err.Error()
		} else {
			markdown = analysis.Markdown
			pc.ContentHash = analysis.ContentHash
			if f[models.FormatMarkdown] || r.rs.ChangeDetection {
				pc.Markdown = &markdown
			}
			if f[models.FormatHTML] {
				pc.HTML = &analysis.HTML
			}
			if f[models.FormatText] {
				pc.Text = &analysis.Text
			}
			if f[models.FormatLinks] {
				pc.Links = analysis.Links
			}
			if f[models.FormatMetadata] {
				analysis.Metadata["status_code"] = res.StatusCode
				analysis.Metadata["source_url"] = page.URL
				pc.Metadata = analysis.Metadata
			}
		}

		if r.rs.ExtractsURL(page.URL) {
			if len(r.rs.Detectors) > 0 {
				// Each page runs the detector of its type (see content.Page.Detect).
				parsed := pi.parsed
				var err error
				if parsed == nil {
					parsed, err = content.ParsePage(page.URL, res.Body)
				}
				if err != nil {
					pc.ExtractError = appendErr(pc.ExtractError, "detect: "+err.Error())
				} else if err := parsed.Detect(r.rs.Detectors, pi.pageType).Store(&pc.DetectedData); err != nil {
					pc.ExtractError = appendErr(pc.ExtractError, "detect: "+err.Error())
				}
			}
			if len(r.rs.ExtractFields) > 0 {
				if out, err := content.Extract(page.URL, res.Body, r.rs.ExtractFields); err != nil {
					pc.ExtractError = appendErr(pc.ExtractError, "extract: "+err.Error())
				} else if b, err := json.Marshal(out); err == nil {
					pc.Extracted = models.JSONRaw(b)
				}
			}
			if r.rs.LLMPrompt != "" && markdown != "" {
				r.llmExtract(&pc, page.URL, markdown)
			}
		}
	} else if ok2xx {
		pc.ContentHash = content.HashText(string(res.Body))
	}

	if len(res.Screenshot) > 0 && r.e.store != nil {
		key := storage.ScreenshotKey(r.job.ID, page.ID)
		if err := r.e.store.Put(r.sideCtx, key, res.Screenshot, "image/png"); err != nil {
			pc.ExtractError = appendErr(pc.ExtractError, "screenshot: "+err.Error())
		} else {
			pc.ScreenshotKey = key
			atomic.AddInt64(&r.uScreenshots, 1)
		}
	}
	if len(res.ActionResults) > 0 && string(res.ActionResults) != "[]" && string(res.ActionResults) != "null" {
		pc.ActionResults = models.JSONRaw(res.ActionResults)
	}

	if err := r.e.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "page_id"}}, UpdateAll: true}).Create(&pc).Error; err != nil {
		log.Printf("[Run %s] Failed to save content for page %d: %v", short(r.job.ID), page.ID, err)
		return
	}
	if r.baseJobID != "" && pc.ContentHash != "" {
		r.detectChange(page, &pc, markdown)
	}
}

// analyzeDocument converts a PDF or DOCX page into PageContent and returns
// its Markdown (for change detection).
func (r *Run) analyzeDocument(pc *models.PageContent, page *models.CrawledPage, kind string, res *modules.FetchResult) string {
	doc, err := content.ConvertDocument(r.sideCtx, kind, res.Body, r.e.documentOptions())
	if err != nil {
		pc.ExtractError = "document: " + err.Error()
		if res.Truncated {
			pc.ExtractError += " (the file is larger than security.max_body_bytes and was cut off)"
		}
		return ""
	}
	atomic.AddInt64(&r.uDocPages, int64(doc.Pages))
	pc.ContentHash = doc.ContentHash
	f := r.rs.Formats
	if f[models.FormatMarkdown] || r.rs.ChangeDetection {
		pc.Markdown = &doc.Markdown
	}
	if f[models.FormatText] {
		pc.Text = &doc.Text
	}
	if f[models.FormatMetadata] {
		doc.Metadata["status_code"] = res.StatusCode
		doc.Metadata["source_url"] = page.URL
		pc.Metadata = doc.Metadata
	}
	if doc.Title != "" {
		title := doc.Title
		if len([]rune(title)) > 500 {
			title = string([]rune(title)[:500])
		}
		r.e.db.Model(&models.CrawledPage{}).Where("id = ?", page.ID).Update("title", title)
	}
	if r.rs.ExtractsURL(page.URL) && r.rs.LLMPrompt != "" && doc.Markdown != "" {
		r.llmExtract(pc, page.URL, doc.Markdown)
	}
	return doc.Markdown
}

func (r *Run) llmExtract(pc *models.PageContent, pageURL, markdown string) {
	if l := r.rs.Limits.MaxLLMTokens; l > 0 && atomic.LoadInt64(&r.uLLMTokens) >= int64(l) {
		pc.ExtractError = appendErr(pc.ExtractError, "llm: token limit reached")
		return
	}
	ctx, cancel := context.WithTimeout(r.sideCtx, 3*time.Minute)
	defer cancel()
	out, tokens, err := r.e.llm.Extract(ctx, r.rs.LLMPrompt, r.rs.LLMSchema, pageURL, markdown)
	atomic.AddInt64(&r.uLLMTokens, int64(tokens))
	if err != nil {
		pc.ExtractError = appendErr(pc.ExtractError, "llm: "+err.Error())
		return
	}
	pc.LLMExtracted = models.JSONRaw(out)
}

func appendErr(existing, msg string) string {
	if existing == "" {
		return msg
	}
	return existing + "; " + msg
}

// detectChange compares a page with the same URL in the baseline job: its
// main text and, when both runs have them, the extracted fields and what the
// detectors found (product, article, …).
func (r *Run) detectChange(page *models.CrawledPage, pc *models.PageContent, markdown string) {
	var prev models.PageContent
	err := r.e.db.Select(append([]string{"content_hash", "markdown", "extracted"}, models.DetectedColumns...)).
		Where("crawl_job_id = ? AND url_hash = ?", r.baseJobID, pc.URLHash).First(&prev).Error
	change := models.PageChange{
		CrawlJobID: r.job.ID,
		BaseJobID:  r.baseJobID,
		PageID:     &page.ID,
		URL:        page.URL,
		URLHash:    pc.URLHash,
		DetectedAt: time.Now(),
	}
	var fields []models.FieldChange
	switch {
	case err != nil:
		change.ChangeType = models.ChangeNew
		atomic.AddInt64(&r.changesNew, 1)
	default:
		fields = content.DiffExtracted(prev.Extracted, pc.Extracted)
		cur := pc.DetectedData.Fields()
		for i, old := range prev.DetectedData.Fields() {
			fields = append(fields, content.DiffDetected(old.Format, old.Value, cur[i].Value)...)
		}
		textChanged := prev.ContentHash != pc.ContentHash
		if !textChanged && len(fields) == 0 {
			return
		}
		change.ChangeType = models.ChangeChanged
		if textChanged {
			old := ""
			if prev.Markdown != nil {
				old = *prev.Markdown
			}
			d := modules.LineDiff(old, markdown, 2, 400)
			change.Added, change.Removed = d.Added, d.Removed
			change.Diff = &d.Unified
		}
		if len(fields) > 0 {
			if b, err := json.Marshal(fields); err == nil {
				change.Fields = models.JSONRaw(b)
			}
		}
		atomic.AddInt64(&r.changesChanged, 1)
	}
	if err := r.e.db.Create(&change).Error; err != nil {
		log.Printf("[Run %s] Failed to save change for %s: %v", short(r.job.ID), page.URL, err)
		return
	}
	data := map[string]interface{}{
		"page_id": page.ID, "url": page.URL, "change_type": change.ChangeType,
		"added": change.Added, "removed": change.Removed, "base_job_id": r.baseJobID,
	}
	if len(fields) > 0 {
		data["fields"] = fields
	}
	r.e.emit(r.snapshotJob(), "page.changed", data)
}

// detectRemovedPages records baseline pages that this complete crawl no longer found.
func (r *Run) detectRemovedPages() {
	type row struct {
		URLHash string
		URL     string
	}
	var rows []row
	err := r.e.db.Raw(`
		SELECT pc.url_hash AS url_hash, cp.url AS url
		FROM page_contents pc
		JOIN crawled_pages cp ON cp.id = pc.page_id
		WHERE pc.crawl_job_id = ?
		  AND NOT EXISTS (SELECT 1 FROM crawled_pages cur WHERE cur.crawl_job_id = ? AND cur.url_hash = pc.url_hash)
		LIMIT 10000`, r.baseJobID, r.job.ID).Scan(&rows).Error
	if err != nil {
		log.Printf("[Run %s] Removed-page detection failed: %v", short(r.job.ID), err)
		return
	}
	now := time.Now()
	for _, rw := range rows {
		r.e.db.Create(&models.PageChange{
			CrawlJobID: r.job.ID, BaseJobID: r.baseJobID, URL: rw.URL, URLHash: rw.URLHash,
			ChangeType: models.ChangeRemoved, DetectedAt: now,
		})
	}
}

// afterCrawlPayload is the JSON structure passed to after_crawl.py via stdin.
type afterCrawlPayload struct {
	URL         string `json:"url"`
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type"`
	Depth       int    `json:"depth"`
	Body        string `json:"body"`
	JobID       string `json:"job_id"`
}

// runAfterCrawlScript executes the selected plugin's after_crawl script with
// the page as JSON on stdin. Errors are counted and logged and never stop the crawl.
func (r *Run) runAfterCrawlScript(pluginID, pageURL string, statusCode int, contentType string, depth int, body []byte) {
	err := r.afterCrawl(pluginID, pageURL, statusCode, contentType, depth, body)
	atomic.AddInt64(&r.pluginRuns, 1)
	if err != nil {
		atomic.AddInt64(&r.pluginFailures, 1)
		r.pluginErrMu.Lock()
		r.pluginLastErr = pageURL + ": " + err.Error()
		r.pluginErrMu.Unlock()
		log.Printf("[AfterCrawl] %s: %v", pageURL, err)
	}
}

func (r *Run) afterCrawl(pluginID, pageURL string, statusCode int, contentType string, depth int, body []byte) error {
	p, err := plugins.Get(pluginID)
	if err != nil {
		return err
	}
	input, err := json.Marshal(afterCrawlPayload{URL: pageURL, StatusCode: statusCode, ContentType: contentType,
		Depth: depth, Body: string(body), JobID: r.job.ID})
	if err != nil {
		return err
	}
	timeout := r.e.cfg.Plugins.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.sideCtx, timeout)
	defer cancel()
	cmd, err := p.Command(ctx, modules.FindPython(r.rs.PythonPath), plugins.HookAfterCrawl, r.job.ID)
	if err != nil {
		return err
	}
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	switch {
	case err != nil && ctx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("timed out after %s: %s", timeout, lastLine(output))
	case err != nil:
		return fmt.Errorf("%v: %s", err, lastLine(output))
	case output != "":
		log.Printf("[AfterCrawl] %s => %s", pageURL, output)
	}
	return nil
}

// lastLine returns the last non-empty line of plugin output (usually the error).
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if len(l) > 500 {
				l = l[:500]
			}
			return l
		}
	}
	return ""
}
