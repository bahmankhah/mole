package modules

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/netx"
)

// FetchResult holds the result of fetching a URL.
// Both HTTP and headless fetchers return this common type.
type FetchResult struct {
	URL           string // final URL after redirects
	StatusCode    int
	ContentType   string
	Body          []byte
	Header        http.Header
	Error         error
	FetchedWith   string // "http" or "browser"
	Screenshot    []byte // PNG, browser only
	ActionResults []byte // JSON array, browser only
	Truncated     bool   // body was cut at the size limit
	Duration      time.Duration
	ViaProxy      bool
}

// FetchOptions customizes a single fetch. Zero value is a plain GET.
type FetchOptions struct {
	Method      string // GET (default) or POST
	Headers     []models.HTTPHeader
	Body        string // POST payload
	UserAgent   string
	Timeout     time.Duration
	Proxy       *url.URL
	Fingerprint string // TLS fingerprint for HTTP fetches

	// Browser-only options.
	WaitSelector   string
	RenderWait     int
	Actions        []models.BrowserAction
	Screenshot     bool
	FullPage       bool
	BlockResources []string
	CookieDir      string
}

// Fetcher is the interface for fetching page content.
// Implementations can use plain HTTP or a headless browser.
type Fetcher interface {
	// Fetch retrieves the content at the given URL.
	Fetch(ctx context.Context, url string, opts FetchOptions) *FetchResult
}

// ---------------------------------------------------------------------------
// HTTPFetcher — HTTP client with browser TLS fingerprints, proxies and SSRF guard
// ---------------------------------------------------------------------------

var chromeUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
}

var fingerprintUserAgents = map[string][]string{
	netx.FingerprintFirefox: {
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0",
		"Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0",
	},
	netx.FingerprintSafari: {"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15"},
	netx.FingerprintIOS:    {"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1"},
	netx.FingerprintEdge:   {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0"},
}

// HTTPFetcher fetches pages over HTTP(S). Create one per job so each job keeps
// its own cookie jar; transports (and their connection pools) are shared.
type HTTPFetcher struct {
	transports *netx.TransportPool
	guard      *netx.Guard
	jar        http.CookieJar
	maxBody    int64
	mu         sync.Mutex
	rng        *rand.Rand
}

// NewHTTPFetcher creates a fetcher. jar may be nil.
func NewHTTPFetcher(transports *netx.TransportPool, guard *netx.Guard, jar http.CookieJar, maxBody int64) *HTTPFetcher {
	if maxBody <= 0 {
		maxBody = 10 << 20
	}
	return &HTTPFetcher{
		transports: transports,
		guard:      guard,
		jar:        jar,
		maxBody:    maxBody,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// pickUserAgent keeps the User-Agent consistent with the TLS fingerprint: a
// Firefox ClientHello with a Chrome UA is itself a bot signal.
func (f *HTTPFetcher) pickUserAgent(configured, fingerprint string) string {
	if configured != "" && !strings.Contains(configured, "Chrome/131.0.0.0") {
		return configured
	}
	pool := chromeUserAgents
	if list, ok := fingerprintUserAgents[fingerprint]; ok {
		pool = list
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return pool[f.rng.Intn(len(pool))]
}

// Fetch performs an HTTP request and returns the result.
func (f *HTTPFetcher) Fetch(ctx context.Context, rawURL string, opts FetchOptions) *FetchResult {
	start := time.Now()
	res := f.fetch(ctx, rawURL, opts)
	res.FetchedWith = "http"
	res.Duration = time.Since(start)
	res.ViaProxy = opts.Proxy != nil
	return res
}

func (f *HTTPFetcher) fetch(ctx context.Context, rawURL string, opts FetchOptions) *FetchResult {
	if err := f.guard.CheckURL(ctx, rawURL); err != nil {
		return &FetchResult{URL: rawURL, Error: err}
	}
	method := strings.ToUpper(strings.TrimSpace(opts.Method))
	if method == "" {
		method = http.MethodGet
	}
	var bodyReader io.Reader
	if method == http.MethodPost && opts.Body != "" {
		bodyReader = strings.NewReader(opts.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return &FetchResult{URL: rawURL, Error: fmt.Errorf("create request: %w", err)}
	}

	ua := f.pickUserAgent(opts.UserAgent, opts.Fingerprint)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,fa;q=0.8")
	req.Header.Set("Cache-Control", "max-age=0")
	if method == http.MethodPost {
		req.Header.Set("Accept", "*/*")
	} else {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
		req.Header.Set("Upgrade-Insecure-Requests", "1")
		req.Header.Set("Sec-Fetch-Site", "none")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-User", "?1")
		req.Header.Set("Sec-Fetch-Dest", "document")
		if strings.Contains(ua, "Chrome/") {
			req.Header.Set("Sec-Ch-Ua", `"Chromium";v="131", "Not_A Brand";v="24", "Google Chrome";v="131"`)
			req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
			req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
		}
	}
	for _, h := range opts.Headers {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			continue
		}
		req.Header.Set(name, h.Value)
	}
	if method == http.MethodPost && opts.Body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", inferPOSTContentType(opts.Body))
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{
		Transport: f.transports.Get(opts.Fingerprint, opts.Proxy),
		Jar:       f.jar,
		Timeout:   timeout,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			// Redirects are untrusted input too: re-check the destination.
			if err := f.guard.CheckURL(next.Context(), next.URL.String()); err != nil {
				return err
			}
			for key, vals := range via[0].Header {
				if _, exists := next.Header[key]; !exists {
					for _, v := range vals {
						next.Header.Add(key, v)
					}
				}
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return &FetchResult{URL: rawURL, Error: fmt.Errorf("http request: %w", err)}
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		if gz, gzErr := gzip.NewReader(resp.Body); gzErr == nil {
			defer gz.Close()
			reader = gz
		}
	}
	body, err := io.ReadAll(io.LimitReader(reader, f.maxBody+1))
	res := &FetchResult{
		URL:         resp.Request.URL.String(),
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Header:      resp.Header,
	}
	if err != nil {
		res.Error = fmt.Errorf("read body: %w", err)
		return res
	}
	if int64(len(body)) > f.maxBody {
		body = body[:f.maxBody]
		res.Truncated = true
	}
	res.Body = body
	return res
}

// inferPOSTContentType picks a Content-Type from the payload shape.
func inferPOSTContentType(body string) string {
	trim := strings.TrimSpace(body)
	if strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[") {
		return "application/json"
	}
	if strings.Contains(trim, "=") {
		return "application/x-www-form-urlencoded"
	}
	return "application/json"
}

// ---------------------------------------------------------------------------
// Challenge / SPA-shell detection (mirrors plugins/headless_fetch.py)
// ---------------------------------------------------------------------------

var (
	appRootRe     = regexp.MustCompile(`(?s)<app-root[^>]*>(.*?)</app-root>`)
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	styleBlockRe  = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	scriptBlockRe = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	templateExpRe = regexp.MustCompile(`\{\{[^}]+\}\}`)
)

// LooksBlocked reports whether a response looks like a CDN/WAF challenge or
// block page (ArvanCloud, Cloudflare, openresty...) rather than real content.
func LooksBlocked(status int, body []byte) bool {
	if status == 403 || status == 503 || status == 429 {
		return true
	}
	low := strings.ToLower(string(body))
	if len(body) < 1500 {
		for _, sig := range []string{"service temporarily unavailable", "service unavailable", "just a moment",
			"checking your browser", "arvan", "ddos protection", "access denied", "enable javascript and cookies"} {
			if strings.Contains(low, sig) {
				return true
			}
		}
	}
	if strings.Contains(low, "service temporarily unavailable") && strings.Contains(low, "openresty") {
		return true
	}
	if len(body) < 500 && strings.Contains(low, "<title>") {
		for _, sig := range []string{"503", "502", "403", "error", "unavailable", "blocked", "denied"} {
			if strings.Contains(low, sig) {
				return true
			}
		}
	}
	return false
}

// LooksLikeSPAShell reports whether HTML is a JavaScript app that has not rendered.
func LooksLikeSPAShell(body []byte) bool {
	s := string(body)
	if strings.Contains(s, "ng-app=") {
		return !strings.Contains(s, "ng-scope")
	}
	if strings.Contains(s, "<app-root") {
		if m := appRootRe.FindStringSubmatch(s); m != nil {
			inner := strings.TrimSpace(htmlCommentRe.ReplaceAllString(m[1], ""))
			return len(inner) < 50
		}
		return false
	}
	if strings.Contains(s, `<div id="root"></div>`) || strings.Contains(s, `<div id="app"></div>`) || strings.Contains(s, `<div id="__next"></div>`) {
		return true
	}
	if strings.Contains(s, "{{") && len(s) < 50000 {
		stripped := scriptBlockRe.ReplaceAllString(styleBlockRe.ReplaceAllString(s, ""), "")
		return len(templateExpRe.FindAllString(stripped, -1)) > 10
	}
	return false
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// FindPython returns the path to a usable python3 executable.
// It checks, in order: explicit config, project venv, then system PATH.
func FindPython(configPath string) string {
	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			return configPath
		}
	}
	venvCandidates := pluginPathCandidates(".venv", "bin", "python3")
	if runtime.GOOS == "windows" {
		venvCandidates = pluginPathCandidates(".venv", "Scripts", "python.exe")
	}
	for _, c := range venvCandidates {
		if _, err := os.Stat(c); err == nil {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	names := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		names = []string{"python", "python3", "py"}
	}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return "python3" // fallback, will fail at runtime with a clear error
}

// FindPluginScript returns the path of a script under plugins/ (or the legacy scripts/).
func FindPluginScript(name, configured string) string {
	if configured != "" {
		return configured
	}
	for _, c := range pluginPathCandidates(name) {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return filepath.Join("plugins", name)
}

// looksLikeBrowserUA returns true if the User-Agent string resembles a real
// browser (Chrome, Firefox, Safari, Edge). Bot-like UAs such as
// "ResolveCrawler/1.0" return false.
func looksLikeBrowserUA(ua string) bool {
	ua = strings.ToLower(ua)
	for _, token := range []string{"mozilla/", "chrome/", "firefox/", "safari/", "edg/", "opera/"} {
		if strings.Contains(ua, token) {
			return true
		}
	}
	return false
}

// execDir returns the directory of the running executable.
func execDir() string {
	ex, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(ex)
}

// pluginPathCandidates returns plugins/<elem...> and the legacy scripts/<elem...>
// paths, both relative to the working directory and the executable directory.
func pluginPathCandidates(elem ...string) []string {
	var out []string
	for _, root := range []string{"plugins", "scripts"} {
		rel := append([]string{root}, elem...)
		out = append(out, filepath.Join(rel...))
		out = append(out, filepath.Join(append([]string{execDir()}, rel...)...))
	}
	return out
}
