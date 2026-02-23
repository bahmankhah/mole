package modules

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"crypto/tls"

	"github.com/resolver/crawler/config"
)

// FetchResult holds the result of fetching a URL.
// Both HTTP and headless fetchers return this common type.
type FetchResult struct {
	URL         string // final URL after redirects
	StatusCode  int
	ContentType string
	Body        []byte
	Error       error
}

// Fetcher is the interface for fetching page content.
// Implementations can use plain HTTP or a headless browser.
type Fetcher interface {
	// Fetch retrieves the content at the given URL.
	Fetch(ctx context.Context, url string) *FetchResult
}

// ---------------------------------------------------------------------------
// HTTPFetcher — standard HTTP client (existing behavior)
// ---------------------------------------------------------------------------

// chromeCipherSuites returns TLS cipher suites ordered to mimic Chrome.
var fetcherChromeCipherSuites = []uint16{
	tls.TLS_AES_128_GCM_SHA256,
	tls.TLS_AES_256_GCM_SHA384,
	tls.TLS_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
	tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_RSA_WITH_AES_256_CBC_SHA,
}

// fetcherUserAgents is a pool of recent browser User-Agents to rotate.
var fetcherUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
}

// HTTPFetcher fetches pages using a standard HTTP client.
type HTTPFetcher struct {
	client    *http.Client
	userAgent string
	mu        sync.Mutex
	rng       *rand.Rand
}

// NewHTTPFetcher creates a fetcher backed by a plain HTTP client.
func NewHTTPFetcher(cfg config.CrawlerConfig) *HTTPFetcher {
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		MaxConnsPerHost:       20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ForceAttemptHTTP2:     true,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
			MinVersion:         tls.VersionTLS12,
			MaxVersion:         tls.VersionTLS13,
			CipherSuites:       fetcherChromeCipherSuites,
			CurvePreferences:   []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
		},
		DisableCompression: false,
	}

	client := &http.Client{
		Timeout:   cfg.RequestTimeout,
		Transport: transport,
		Jar:       jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if len(via) > 0 {
				for key, vals := range via[0].Header {
					if _, exists := req.Header[key]; !exists {
						for _, v := range vals {
							req.Header.Add(key, v)
						}
					}
				}
			}
			return nil
		},
	}

	return &HTTPFetcher{
		client:    client,
		userAgent: cfg.UserAgent,
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Fetch performs an HTTP GET request and returns the result.
func (f *HTTPFetcher) Fetch(ctx context.Context, url string) *FetchResult {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return &FetchResult{URL: url, Error: fmt.Errorf("create request: %w", err)}
	}

	// Pick User-Agent
	ua := f.userAgent
	if ua == "" || strings.Contains(ua, "Chrome/131.0.0.0") {
		f.mu.Lock()
		ua = fetcherUserAgents[f.rng.Intn(len(fetcherUserAgents))]
		f.mu.Unlock()
	}

	// Set browser-like headers
	req.Header = http.Header{}
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="131", "Not_A Brand";v="24", "Google Chrome";v="131"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "max-age=0")

	resp, err := f.client.Do(req)
	if err != nil {
		return &FetchResult{URL: url, Error: fmt.Errorf("http request: %w", err)}
	}
	defer resp.Body.Close()

	// Handle gzip safety-net
	var bodyReader io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gzReader, gzErr := gzip.NewReader(resp.Body)
		if gzErr == nil {
			defer gzReader.Close()
			bodyReader = gzReader
		}
	}

	body, err := io.ReadAll(io.LimitReader(bodyReader, 10*1024*1024))
	if err != nil {
		return &FetchResult{
			URL:         url,
			StatusCode:  resp.StatusCode,
			ContentType: resp.Header.Get("Content-Type"),
			Error:       fmt.Errorf("read body: %w", err),
		}
	}

	return &FetchResult{
		URL:         resp.Request.URL.String(),
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
	}
}

// ---------------------------------------------------------------------------
// HeadlessFetcher — renders JS-heavy pages via a Python/Playwright subprocess
// ---------------------------------------------------------------------------

// headlessFetchResponse matches the JSON output of scripts/headless_fetch.py
type headlessFetchResponse struct {
	URL         string `json:"url"`
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type"`
	Body        string `json:"body"`
	ErrorMsg    string `json:"error"`
}

// HeadlessFetcher renders pages using a headless Chromium browser.
type HeadlessFetcher struct {
	scriptPath   string
	timeoutSec   int
	waitSelector string
	userAgent    string
	pythonCmd    string // cached python executable path
}

// NewHeadlessFetcher creates a fetcher that delegates to the headless_fetch.py script.
func NewHeadlessFetcher(cfg config.CrawlerConfig) *HeadlessFetcher {
	scriptPath := cfg.HeadlessScriptPath
	if scriptPath == "" {
		// Auto-detect: look relative to the working directory
		candidates := []string{
			"scripts/headless_fetch.py",
			filepath.Join(execDir(), "scripts", "headless_fetch.py"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				scriptPath = c
				break
			}
		}
		if scriptPath == "" {
			scriptPath = "scripts/headless_fetch.py" // fallback
		}
	}

	timeoutSec := int(cfg.RequestTimeout.Seconds())
	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	hf := &HeadlessFetcher{
		scriptPath:   scriptPath,
		timeoutSec:   timeoutSec,
		waitSelector: cfg.HeadlessWaitSelector,
		userAgent:    cfg.UserAgent,
		pythonCmd:    FindPython(cfg.PythonPath),
	}

	log.Printf("[HeadlessFetcher] Initialized: script=%s, python=%s, timeout=%ds, waitSelector=%q",
		hf.scriptPath, hf.pythonCmd, hf.timeoutSec, hf.waitSelector)

	return hf
}

// Fetch renders a URL with headless Chromium and returns the result.
func (f *HeadlessFetcher) Fetch(ctx context.Context, url string) *FetchResult {
	args := []string{
		f.scriptPath,
		url,
		"--timeout", fmt.Sprintf("%d", f.timeoutSec),
	}
	if f.waitSelector != "" {
		args = append(args, "--wait-selector", f.waitSelector)
	}
	if f.userAgent != "" {
		args = append(args, "--user-agent", f.userAgent)
	}

	// Add extra context timeout buffer (script timeout + 15s for browser startup/teardown)
	cmdTimeout := time.Duration(f.timeoutSec+15) * time.Second
	cmdCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, f.pythonCmd, args...)
	cmd.Stderr = os.Stderr // let script errors go to the main stderr for debugging

	log.Printf("[HeadlessFetcher] Fetching: %s", url)
	output, err := cmd.Output()
	if err != nil {
		return &FetchResult{URL: url, Error: fmt.Errorf("headless script: %w", err)}
	}

	var resp headlessFetchResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return &FetchResult{URL: url, Error: fmt.Errorf("parse headless output: %w (raw=%d bytes)", err, len(output))}
	}

	if resp.ErrorMsg != "" {
		return &FetchResult{
			URL:         resp.URL,
			StatusCode:  resp.StatusCode,
			ContentType: resp.ContentType,
			Body:        []byte(resp.Body),
			Error:       fmt.Errorf("headless: %s", resp.ErrorMsg),
		}
	}

	return &FetchResult{
		URL:         resp.URL,
		StatusCode:  resp.StatusCode,
		ContentType: resp.ContentType,
		Body:        []byte(resp.Body),
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// FindPython returns the path to a usable python3 executable.
// It checks, in order: explicit config, project venv, then system PATH.
func FindPython(configPath string) string {
	// 1. Explicit config
	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			return configPath
		}
	}

	// 2. Project venv (scripts/.venv/bin/python3)
	venvCandidates := []string{
		"scripts/.venv/bin/python3",
		filepath.Join(execDir(), "scripts", ".venv", "bin", "python3"),
	}
	if runtime.GOOS == "windows" {
		venvCandidates = []string{
			"scripts\\.venv\\Scripts\\python.exe",
			filepath.Join(execDir(), "scripts", ".venv", "Scripts", "python.exe"),
		}
	}
	for _, c := range venvCandidates {
		if _, err := os.Stat(c); err == nil {
			abs, _ := filepath.Abs(c)
			log.Printf("[Python] Using venv python: %s", abs)
			return abs
		}
	}

	// 3. System PATH
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

// execDir returns the directory of the running executable.
func execDir() string {
	ex, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(ex)
}
