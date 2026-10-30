package jobs

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/resolver/crawler/crawler"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/search"
)

// SearchRequest is a /v1/search request.
type SearchRequest struct {
	Query    string              `json:"query"`
	Limit    int                 `json:"limit,omitempty"`    // default 5, at most search.MaxLimit
	Lang     string              `json:"lang,omitempty"`     // default search.default_lang
	Country  string              `json:"country,omitempty"`  // default search.default_country
	Scrape   bool                `json:"scrape,omitempty"`   // also scrape every result
	Settings *models.JobSettings `json:"settings,omitempty"` // scrape settings (formats, browser, extraction...)
	MaxAge   int                 `json:"max_age,omitempty"`  // accept cached scrapes this many seconds old
}

// SearchHit is one search result, with its scrape when requested.
type SearchHit struct {
	search.Result
	Scrape *crawler.ScrapeResult `json:"scrape,omitempty"`
	Error  string                `json:"error,omitempty"` // why the scrape failed
}

// SearchResponse is the answer to a /v1/search request.
type SearchResponse struct {
	Query      string          `json:"query"`
	Provider   string          `json:"provider"`
	Results    []SearchHit     `json:"results"`
	Usage      models.JobUsage `json:"usage"` // sum of the scrapes
	DurationMs int64           `json:"duration_ms"`
}

// searchScrapeConcurrency bounds the scrapes of one search.
const searchScrapeConcurrency = 5

// SearchEnabled reports whether a search provider is configured.
func (m *Manager) SearchEnabled() bool { return m.searcher != nil }

// SearchProvider returns the configured provider's name ("" when none).
func (m *Manager) SearchProvider() string {
	if m.searcher == nil {
		return ""
	}
	return m.searcher.Name()
}

// Search runs a web search and, with req.Scrape, scrapes every result.
func (m *Manager) Search(ctx context.Context, req SearchRequest, ownerRef string) (*SearchResponse, error) {
	if m.searcher == nil {
		return nil, invalidInput("web search is not configured on this engine (search.provider)")
	}
	q := strings.TrimSpace(req.Query)
	if q == "" || len([]rune(q)) > 400 {
		return nil, invalidInput("query must be 1 to 400 characters")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > search.MaxLimit {
		return nil, invalidInput("limit must be at most %d", search.MaxLimit)
	}
	lang, country := strings.TrimSpace(req.Lang), strings.TrimSpace(req.Country)
	if lang == "" {
		lang = m.config.Search.DefaultLang
	}
	if country == "" {
		country = m.config.Search.DefaultCountry
	}
	if len(lang) > 16 || len(country) > 8 {
		return nil, invalidInput("lang or country is too long")
	}
	if req.MaxAge < 0 {
		return nil, invalidInput("max_age must not be negative")
	}

	start := time.Now()
	results, err := m.searcher.Search(ctx, search.Query{Query: q, Limit: limit, Lang: lang, Country: country})
	if err != nil {
		return nil, err
	}
	out := &SearchResponse{Query: q, Provider: m.searcher.Name(), Results: make([]SearchHit, len(results))}
	for i, r := range results {
		out.Results[i].Result = r
	}
	if req.Scrape {
		m.scrapeHits(ctx, out, req, ownerRef)
	}
	out.DurationMs = time.Since(start).Milliseconds()
	return out, nil
}

func (m *Manager) scrapeHits(ctx context.Context, out *SearchResponse, req SearchRequest, ownerRef string) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, searchScrapeConcurrency)
	for i := range out.Results {
		wg.Add(1)
		go func(hit *SearchHit) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				hit.Error = ctx.Err().Error()
				return
			}
			defer func() { <-slots }()
			res, err := m.Scrape(ctx, ScrapeRequest{
				ScrapeRequest: crawler.ScrapeRequest{URL: hit.URL, Settings: req.Settings},
				MaxAge:        req.MaxAge,
			}, ownerRef)
			if err != nil {
				hit.Error = err.Error()
				return
			}
			hit.Scrape = res
		}(&out.Results[i])
	}
	wg.Wait()
	for _, hit := range out.Results {
		if hit.Scrape != nil {
			addUsage(&out.Usage, hit.Scrape.Usage)
		}
	}
}

func addUsage(dst *models.JobUsage, u models.JobUsage) {
	dst.Requests += u.Requests
	dst.Pages += u.Pages
	dst.Bytes += u.Bytes
	dst.BrowserMs += u.BrowserMs
	dst.LLMTokens += u.LLMTokens
	dst.Errors += u.Errors
	dst.Screenshots += u.Screenshots
	dst.ProxyRequests += u.ProxyRequests
	dst.DocumentPages += u.DocumentPages
}
