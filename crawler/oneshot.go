package crawler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/resolver/crawler/content"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
)

// ScrapeRequest asks for one URL to be fetched and converted synchronously.
// Settings reuses the job settings (formats, browser, extraction, proxies...).
type ScrapeRequest struct {
	URL      string              `json:"url"`
	Settings *models.JobSettings `json:"settings,omitempty"`
}

// ScrapeResult is the response of a synchronous scrape.
type ScrapeResult struct {
	URL           string                 `json:"url"`
	FinalURL      string                 `json:"final_url"`
	StatusCode    int                    `json:"status_code"`
	ContentType   string                 `json:"content_type"`
	FetchedWith   string                 `json:"fetched_with"`
	Title         string                 `json:"title,omitempty"`
	Markdown      string                 `json:"markdown,omitempty"`
	HTML          string                 `json:"html,omitempty"`
	Text          string                 `json:"text,omitempty"`
	Links         []string               `json:"links,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	Extracted     map[string]interface{} `json:"extracted,omitempty"`
	LLMExtracted  json.RawMessage        `json:"llm_extracted,omitempty"`
	Screenshot    string                 `json:"screenshot,omitempty"` // base64 PNG
	ActionResults json.RawMessage        `json:"action_results,omitempty"`
	ContentHash   string                 `json:"content_hash,omitempty"`
	Error         string                 `json:"error,omitempty"`
	ExtractError  string                 `json:"extract_error,omitempty"`
	Usage         models.JobUsage        `json:"usage"`
	DurationMs    int64                  `json:"duration_ms"`
}

type oneShotMeter struct{ u models.JobUsage }

func (m *oneShotMeter) meter(res *modules.FetchResult) {
	atomic.AddInt64(&m.u.Requests, 1)
	atomic.AddInt64(&m.u.Bytes, int64(len(res.Body)))
	if res.FetchedWith == "browser" {
		atomic.AddInt64(&m.u.BrowserMs, res.Duration.Milliseconds())
	}
	if res.ViaProxy {
		atomic.AddInt64(&m.u.ProxyRequests, 1)
	}
	if res.Error != nil {
		atomic.AddInt64(&m.u.Errors, 1)
	}
}

func normalizeTarget(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid url %q", raw)
	}
	return u.String(), nil
}

// Scrape fetches one URL and returns the requested formats. Without explicit
// formats it returns Markdown and metadata.
func (e *Engine) Scrape(ctx context.Context, req ScrapeRequest) (*ScrapeResult, error) {
	target, err := normalizeTarget(req.URL)
	if err != nil {
		return nil, err
	}
	rs, err := BuildRunSettings(e.cfg, req.Settings, nil, e.proxies)
	if err != nil {
		return nil, err
	}
	if len(rs.Formats) == 0 {
		rs.Formats = map[string]bool{models.FormatMarkdown: true, models.FormatMetadata: true}
	}
	if err := e.guard.CheckURL(ctx, target); err != nil {
		return nil, err
	}

	start := time.Now()
	m := &oneShotMeter{}
	jar, _ := cookiejar.New(nil)
	httpF := modules.NewHTTPFetcher(e.transports, e.guard, jar, e.cfg.Security.MaxBodyBytes)
	method, body := "", ""
	if req.Settings != nil && req.Settings.RequestMethod != nil && strings.EqualFold(*req.Settings.RequestMethod, "POST") {
		method = "POST"
		if req.Settings.RequestBody != nil {
			body = *req.Settings.RequestBody
		}
	}
	res := e.fetchWith(ctx, rs, httpF, target, method, body, "scrape", m.meter)

	out := &ScrapeResult{
		URL:         target,
		FinalURL:    res.URL,
		StatusCode:  res.StatusCode,
		ContentType: res.ContentType,
		FetchedWith: res.FetchedWith,
	}
	if res.Error != nil {
		out.Error = res.Error.Error()
	}
	if len(res.Screenshot) > 0 {
		out.Screenshot = base64.StdEncoding.EncodeToString(res.Screenshot)
		m.u.Screenshots++
	}
	if len(res.ActionResults) > 0 && string(res.ActionResults) != "null" {
		out.ActionResults = res.ActionResults
	}

	ct := strings.ToLower(res.ContentType)
	switch {
	case len(res.Body) == 0:
	case strings.Contains(ct, "html") || ct == "":
		f := rs.Formats
		analysis, aerr := content.Analyze(out.FinalURL, res.Body, content.Options{
			Markdown:        f[models.FormatMarkdown] || rs.LLMPrompt != "",
			HTML:            f[models.FormatHTML],
			Text:            f[models.FormatText],
			Links:           f[models.FormatLinks],
			Metadata:        f[models.FormatMetadata],
			OnlyMainContent: rs.OnlyMainContent,
		})
		if aerr != nil {
			out.ExtractError = "analyze: " + aerr.Error()
			break
		}
		out.Title = analysis.Title
		out.ContentHash = analysis.ContentHash
		if f[models.FormatMarkdown] {
			out.Markdown = analysis.Markdown
		}
		out.HTML, out.Text, out.Links = analysis.HTML, analysis.Text, analysis.Links
		if f[models.FormatMetadata] {
			analysis.Metadata["status_code"] = res.StatusCode
			analysis.Metadata["source_url"] = target
			out.Metadata = analysis.Metadata
		}
		if len(rs.ExtractFields) > 0 {
			if ex, err := content.Extract(out.FinalURL, res.Body, rs.ExtractFields); err != nil {
				out.ExtractError = appendErr(out.ExtractError, "extract: "+err.Error())
			} else {
				out.Extracted = ex
			}
		}
		if rs.LLMPrompt != "" {
			llmOut, tokens, err := e.llm.Extract(ctx, rs.LLMPrompt, rs.LLMSchema, out.FinalURL, analysis.Markdown)
			m.u.LLMTokens += int64(tokens)
			if err != nil {
				out.ExtractError = appendErr(out.ExtractError, "llm: "+err.Error())
			} else {
				out.LLMExtracted = llmOut
			}
		}
	default:
		// JSON, XML and text responses are returned as text.
		out.Text = string(res.Body)
		out.ContentHash = content.HashText(out.Text)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		m.u.Pages = 1
	}
	out.Usage = m.u
	out.DurationMs = time.Since(start).Milliseconds()
	return out, nil
}

// MapRequest asks for a fast list of a site's URLs without crawling it.
type MapRequest struct {
	URL               string              `json:"url"`
	Search            string              `json:"search,omitempty"`             // keep URLs containing this text
	Limit             int                 `json:"limit,omitempty"`              // default 5000, max 50000
	IncludeSubdomains bool                `json:"include_subdomains,omitempty"` // also keep links to other subdomains
	IgnoreSitemap     bool                `json:"ignore_sitemap,omitempty"`
	Settings          *models.JobSettings `json:"settings,omitempty"` // network/browser options
}

// MapResult lists discovered URLs.
type MapResult struct {
	URL         string          `json:"url"`
	URLs        []string        `json:"urls"`
	Total       int             `json:"total"`
	Truncated   bool            `json:"truncated"`
	Sitemaps    []string        `json:"sitemaps"`
	FromLinks   int             `json:"from_links"`
	FromSitemap int             `json:"from_sitemap"`
	Usage       models.JobUsage `json:"usage"`
	DurationMs  int64           `json:"duration_ms"`
}

// Map discovers URLs of a site from robots.txt, sitemaps and the start page's links.
func (e *Engine) Map(ctx context.Context, req MapRequest) (*MapResult, error) {
	target, err := normalizeTarget(req.URL)
	if err != nil {
		return nil, err
	}
	if err := e.guard.CheckURL(ctx, target); err != nil {
		return nil, err
	}
	rs, err := BuildRunSettings(e.cfg, req.Settings, nil, e.proxies)
	if err != nil {
		return nil, err
	}
	rs.Actions, rs.Screenshot = nil, false
	limit := req.Limit
	if limit <= 0 {
		limit = 5000
	}
	if limit > 50000 {
		limit = 50000
	}
	start := time.Now()
	m := &oneShotMeter{}
	jar, _ := cookiejar.New(nil)
	httpF := modules.NewHTTPFetcher(e.transports, e.guard, jar, e.cfg.Security.MaxBodyBytes)
	cleaner := modules.NewURLCleaner()
	base, _ := url.Parse(target)
	origin := base.Scheme + "://" + base.Host
	baseDomain := cleaner.ExtractBaseDomain(target)
	search := strings.ToLower(strings.TrimSpace(req.Search))

	seen := map[string]bool{}
	var urls []string
	out := &MapResult{URL: target}
	add := func(raw string) bool {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return false
		}
		if req.IncludeSubdomains {
			if cleaner.ExtractBaseDomain(u.String()) != baseDomain {
				return false
			}
		} else if !strings.EqualFold(u.Hostname(), base.Hostname()) {
			return false
		}
		u.Fragment = ""
		s := u.String()
		if seen[s] || (search != "" && !strings.Contains(strings.ToLower(s), search)) {
			return false
		}
		seen[s] = true
		if len(urls) >= limit {
			out.Truncated = true
			return false
		}
		urls = append(urls, s)
		return true
	}
	get := func(u string) []byte {
		if ctx.Err() != nil {
			return nil
		}
		res := e.fetchWith(ctx, rs, httpF, u, "", "", "map", m.meter)
		if res.Error != nil || res.StatusCode < 200 || res.StatusCode >= 300 {
			return nil
		}
		return maybeGunzip(res.Body)
	}

	if !req.IgnoreSitemap {
		queue := []string{origin + "/sitemap.xml", origin + "/sitemap_index.xml"}
		if robots := get(origin + "/robots.txt"); robots != nil {
			queue = append(modules.NewRobotsParser(cleaner).ParseRobots(origin+"/robots.txt", robots).Sitemaps, queue...)
		}
		parser := modules.NewSitemapParser(cleaner)
		visited := map[string]bool{}
		for len(queue) > 0 && len(visited) < 50 && !out.Truncated && ctx.Err() == nil {
			sm := queue[0]
			queue = queue[1:]
			if visited[sm] {
				continue
			}
			visited[sm] = true
			body := get(sm)
			if body == nil {
				continue
			}
			pageURLs, nested, err := parser.ParseSitemap(body)
			if err != nil {
				continue
			}
			out.Sitemaps = append(out.Sitemaps, sm)
			for _, u := range pageURLs {
				if add(u) {
					out.FromSitemap++
				}
			}
			queue = append(queue, nested...)
		}
	}

	add(target)
	if body := get(target); body != nil {
		if links, err := modules.NewHTMLLinkExtractor(cleaner).ExtractLinksWithAnchors(target, body); err == nil {
			for _, l := range links {
				if add(l.URL) {
					out.FromLinks++
				}
			}
		}
	}

	sort.Strings(urls)
	out.URLs = urls
	out.Total = len(urls)
	out.Usage = m.u
	out.DurationMs = time.Since(start).Milliseconds()
	return out, nil
}

func maybeGunzip(b []byte) []byte {
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return b
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, 50<<20))
	if err != nil {
		return b
	}
	return out
}
