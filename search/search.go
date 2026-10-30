// Package search queries a web search provider (a SearXNG instance, the Brave
// Search API or Serper's Google API) for /v1/search.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/resolver/crawler/config"
)

// MaxLimit caps the results of one query.
const MaxLimit = 50

// Query is one web search.
type Query struct {
	Query   string
	Limit   int    // results wanted (1..MaxLimit)
	Lang    string // e.g. "fa"
	Country string // e.g. "ir"
}

// Result is one search hit.
type Result struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Position    int    `json:"position"`
}

// Provider runs web searches.
type Provider interface {
	Name() string
	Search(ctx context.Context, q Query) ([]Result, error)
}

// Default API endpoints; search.base_url overrides them.
const (
	braveEndpoint  = "https://api.search.brave.com/res/v1/web/search"
	serperEndpoint = "https://google.serper.dev/search"
)

// New builds the configured provider; it returns nil when none is configured.
func New(cfg config.SearchConfig) (Provider, error) {
	if cfg.Provider == "" {
		return nil, nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.Proxy != "" {
		p, err := url.Parse(cfg.Proxy)
		if err != nil || p.Host == "" {
			return nil, fmt.Errorf("search.proxy: invalid URL")
		}
		transport.Proxy = http.ProxyURL(p)
	}
	c := client{http: &http.Client{Timeout: timeout, Transport: transport}, key: cfg.APIKey}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	switch cfg.Provider {
	case "searxng":
		if base == "" {
			return nil, fmt.Errorf("search.base_url is required for searxng")
		}
		return &searxng{client: c, base: base}, nil
	case "brave":
		if base == "" {
			base = braveEndpoint
		}
		return &brave{client: c, endpoint: base}, nil
	case "serper":
		if base == "" {
			base = serperEndpoint
		}
		return &serper{client: c, endpoint: base}, nil
	}
	return nil, fmt.Errorf("unknown search provider %q", cfg.Provider)
}

type client struct {
	http *http.Client
	key  string
}

// do sends req and decodes a JSON answer into out.
func (c client) do(req *http.Request, out interface{}) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mole-Search/1.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("search provider: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return fmt.Errorf("search provider: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return fmt.Errorf("search provider answered HTTP %d: %s", resp.StatusCode, snippet)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("search provider sent invalid JSON: %w", err)
	}
	return nil
}

// collector keeps unique http(s) results up to a limit.
type collector struct {
	limit int
	seen  map[string]bool
	out   []Result
}

func newCollector(limit int) *collector {
	return &collector{limit: limit, seen: map[string]bool{}}
}

// add reports whether the result was kept.
func (c *collector) add(rawURL, title, desc string) bool {
	if c.full() {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	s := u.String()
	if c.seen[s] {
		return false
	}
	c.seen[s] = true
	c.out = append(c.out, Result{URL: s, Title: plain(title), Description: plain(desc), Position: len(c.out) + 1})
	return true
}

func (c *collector) full() bool { return len(c.out) >= c.limit }

var tagRe = regexp.MustCompile(`<[^>]*>`)

// plain removes the highlighting markup some providers put in snippets.
func plain(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(s, ""))), " ")
}

// searxng queries a SearXNG instance's JSON API (format=json must be enabled
// in its settings.yml).
type searxng struct {
	client
	base string
}

func (s *searxng) Name() string { return "searxng" }

func (s *searxng) Search(ctx context.Context, q Query) ([]Result, error) {
	c := newCollector(q.Limit)
	lang := q.Lang
	if lang != "" && q.Country != "" {
		lang += "-" + strings.ToUpper(q.Country)
	}
	for page := 1; page <= 5 && !c.full(); page++ {
		v := url.Values{"q": {q.Query}, "format": {"json"}, "pageno": {strconv.Itoa(page)}}
		if lang != "" {
			v.Set("language", lang)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/search?"+v.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var body struct {
			Results []struct {
				URL     string `json:"url"`
				Title   string `json:"title"`
				Content string `json:"content"`
			} `json:"results"`
		}
		if err := s.do(req, &body); err != nil {
			return nil, err
		}
		added := 0
		for _, r := range body.Results {
			if c.add(r.URL, r.Title, r.Content) {
				added++
			}
		}
		if added == 0 {
			break
		}
	}
	return c.out, nil
}

// brave queries the Brave Search web API.
type brave struct {
	client
	endpoint string
}

func (b *brave) Name() string { return "brave" }

func (b *brave) Search(ctx context.Context, q Query) ([]Result, error) {
	c := newCollector(q.Limit)
	count := min(q.Limit, 20)
	// offset counts pages of count results; Brave accepts 0 to 9.
	for offset := 0; offset <= 9 && !c.full(); offset++ {
		v := url.Values{"q": {q.Query}, "count": {strconv.Itoa(count)}, "offset": {strconv.Itoa(offset)}}
		if q.Lang != "" {
			v.Set("search_lang", q.Lang)
		}
		if q.Country != "" {
			v.Set("country", strings.ToUpper(q.Country))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint+"?"+v.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Subscription-Token", b.key)
		var body struct {
			Web struct {
				Results []struct {
					URL         string `json:"url"`
					Title       string `json:"title"`
					Description string `json:"description"`
				} `json:"results"`
			} `json:"web"`
		}
		if err := b.do(req, &body); err != nil {
			return nil, err
		}
		for _, r := range body.Web.Results {
			c.add(r.URL, r.Title, r.Description)
		}
		if len(body.Web.Results) < count {
			break
		}
	}
	return c.out, nil
}

// serper queries Google results through serper.dev.
type serper struct {
	client
	endpoint string
}

func (s *serper) Name() string { return "serper" }

func (s *serper) Search(ctx context.Context, q Query) ([]Result, error) {
	payload := map[string]interface{}{"q": q.Query, "num": q.Limit}
	if q.Lang != "" {
		payload["hl"] = q.Lang
	}
	if q.Country != "" {
		payload["gl"] = strings.ToLower(q.Country)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", s.key)
	var body struct {
		Organic []struct {
			Link    string `json:"link"`
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	if err := s.do(req, &body); err != nil {
		return nil, err
	}
	c := newCollector(q.Limit)
	for _, r := range body.Organic {
		c.add(r.Link, r.Title, r.Snippet)
	}
	return c.out, nil
}
