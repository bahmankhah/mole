package crawler

import (
	"context"
	"log"
	"strings"

	"github.com/resolver/crawler/modules"
)

// fetchWith downloads rawURL using rs's strategy: rate limiting, proxy
// selection, HTTP or browser, and the browser fallback for blocked or
// unrendered pages. meter is called for every request actually sent.
func (e *Engine) fetchWith(ctx context.Context, rs *RunSettings, httpF *modules.HTTPFetcher, rawURL, method, body, stickyScope string, meter func(*modules.FetchResult)) *modules.FetchResult {
	host := hostOf(rawURL)
	if err := e.hostLimiter.Wait(ctx, host, rs.HostRateLimit); err != nil {
		return &modules.FetchResult{URL: rawURL, Error: err}
	}
	sticky := ""
	if rs.StickyProxy {
		sticky = stickyScope + "|" + host
	}
	opts := modules.FetchOptions{
		Method:         method,
		Headers:        rs.Headers,
		Body:           body,
		UserAgent:      rs.UserAgent,
		Timeout:        rs.RequestTimeout,
		Proxy:          e.proxies.Select(host, rs.ProxyPool, rs.Proxies, sticky),
		Fingerprint:    rs.Fingerprint,
		WaitSelector:   rs.HeadlessWaitSelector,
		RenderWait:     rs.HeadlessRenderWait,
		Actions:        rs.Actions,
		Screenshot:     rs.Screenshot,
		FullPage:       rs.FullPage,
		BlockResources: rs.BlockResources,
		CookieDir:      e.cookieDir,
	}
	isPost := strings.EqualFold(method, "POST") || body != ""

	var res *modules.FetchResult
	if rs.UseHeadlessBrowser && !isPost {
		// The browser only navigates with GET; POST requests always use HTTP.
		res = e.browsers.Fetch(ctx, rawURL, opts)
		meter(res)
	} else {
		res = httpF.Fetch(ctx, rawURL, opts)
		meter(res)
		if rs.HeadlessFallback && !isPost && ctx.Err() == nil && needsBrowser(res) {
			log.Printf("[Fetch] %s looks blocked or unrendered over HTTP (status %d); retrying in the browser", rawURL, res.StatusCode)
			br := e.browsers.Fetch(ctx, rawURL, opts)
			meter(br)
			if br.Error == nil || len(br.Body) > len(res.Body) {
				res = br
			}
		}
	}
	if res.StatusCode == 429 || res.StatusCode == 503 {
		e.hostLimiter.Penalize(host, retryAfter(res))
	}
	return res
}
