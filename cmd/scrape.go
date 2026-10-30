package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/resolver/crawler/app"
	"github.com/resolver/crawler/crawler"
	"github.com/resolver/crawler/models"
	"github.com/spf13/cobra"
)

// fetchFlags are the network/browser options shared by scrape, map and crawl.
type fetchFlags struct {
	browser      bool
	fallback     bool
	waitSelector string
	renderWait   int
	proxy        []string
	proxyPool    string
	fingerprint  string
	userAgent    string
	headers      []string
	timeout      int
	settingsFile string
}

func (f *fetchFlags) register(c *cobra.Command) {
	fl := c.Flags()
	fl.BoolVar(&f.browser, "browser", false, "render with the headless browser")
	fl.BoolVar(&f.fallback, "fallback", false, "retry in the browser when HTTP gets a challenge page or an empty SPA shell")
	fl.StringVar(&f.waitSelector, "wait-for", "", "CSS selector the browser waits for")
	fl.IntVar(&f.renderWait, "render-wait", 0, "extra seconds the browser waits after network idle")
	fl.StringArrayVar(&f.proxy, "proxy", nil, "proxy URL (http, https, socks5); repeatable")
	fl.StringVar(&f.proxyPool, "proxy-pool", "", `proxy pool name, "auto" or "direct"`)
	fl.StringVar(&f.fingerprint, "fingerprint", "", "TLS fingerprint: chrome, firefox, safari, edge, ios, go")
	fl.StringVar(&f.userAgent, "user-agent", "", "User-Agent header")
	fl.StringArrayVarP(&f.headers, "header", "H", nil, `extra header "Name: value"; repeatable`)
	fl.IntVar(&f.timeout, "timeout", 0, "request timeout in seconds")
	fl.StringVar(&f.settingsFile, "settings", "", "JSON file with job settings (same fields as the API); flags override it")
}

func (f *fetchFlags) settings() (*models.JobSettings, error) {
	s := &models.JobSettings{}
	if f.settingsFile != "" {
		raw, err := os.ReadFile(f.settingsFile)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, s); err != nil {
			return nil, fmt.Errorf("%s: %w", f.settingsFile, err)
		}
	}
	if f.browser {
		s.UseHeadlessBrowser = boolPtr(true)
	}
	if f.fallback {
		s.HeadlessFallback = boolPtr(true)
	}
	if f.waitSelector != "" {
		s.HeadlessWaitSelector = &f.waitSelector
	}
	if f.renderWait > 0 {
		s.HeadlessRenderWait = &f.renderWait
	}
	if len(f.proxy) > 0 {
		s.Proxies = f.proxy
	}
	if f.proxyPool != "" {
		s.ProxyPool = &f.proxyPool
	}
	if f.fingerprint != "" {
		s.TLSFingerprint = &f.fingerprint
	}
	if f.userAgent != "" {
		s.UserAgent = &f.userAgent
	}
	if f.timeout > 0 {
		s.RequestTimeoutSec = &f.timeout
	}
	for _, h := range f.headers {
		name, value, ok := strings.Cut(h, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid header %q (want \"Name: value\")", h)
		}
		s.RequestHeaders = append(s.RequestHeaders, models.HTTPHeader{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	return s, nil
}

func boolPtr(b bool) *bool { return &b }

// openEngineOnly builds the engine without a database (scrape and map).
func openEngineOnly() (*app.App, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return app.New(cfg, app.Options{})
}

func newScrapeCmd() *cobra.Command {
	var ff fetchFlags
	var formats []string
	var mainOnly bool
	var screenshot, extractFile, llmPrompt, llmSchemaFile, actionsFile, outFile string
	var fullPage bool
	c := &cobra.Command{
		Use:   "scrape <url>",
		Short: "Fetch one page and print it as Markdown, text, HTML, links, metadata, product data or extracted data",
		Example: `  mole scrape https://example.com
  mole scrape digikala.com/product/dkp-123 --browser --main-only -f markdown,metadata
  mole scrape https://shop.ir/p/1 -f product
  mole scrape https://shop.ir/p/1 --extract fields.json -o json
  mole scrape https://site.ir --browser --screenshot shot.png --full-page
  mole scrape https://news.ir/a/1 --llm-prompt "title, author and publish date"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := ff.settings()
			if err != nil {
				return err
			}
			if len(formats) > 0 {
				s.Formats = splitCSV(formats)
			}
			if mainOnly {
				s.OnlyMainContent = boolPtr(true)
			}
			if screenshot != "" {
				s.Screenshot = boolPtr(true)
				s.UseHeadlessBrowser = boolPtr(true)
				if fullPage {
					s.FullPageScreenshot = boolPtr(true)
				}
			}
			if extractFile != "" {
				if err := readJSONFile(extractFile, &s.ExtractFields); err != nil {
					return err
				}
			}
			if actionsFile != "" {
				if err := readJSONFile(actionsFile, &s.BrowserActions); err != nil {
					return err
				}
				s.UseHeadlessBrowser = boolPtr(true)
			}
			if llmPrompt != "" {
				s.LLMPrompt = &llmPrompt
			}
			if llmSchemaFile != "" {
				raw, err := os.ReadFile(llmSchemaFile)
				if err != nil {
					return err
				}
				schema := string(raw)
				s.LLMSchema = &schema
			}

			a, err := openEngineOnly()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			res, err := a.Engine.Scrape(ctx, crawler.ScrapeRequest{URL: args[0], Settings: s})
			if err != nil {
				return err
			}
			if screenshot != "" && res.Screenshot != "" {
				png, err := base64.StdEncoding.DecodeString(res.Screenshot)
				if err == nil {
					err = os.WriteFile(screenshot, png, 0o644)
				}
				if err != nil {
					return fmt.Errorf("write screenshot: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Screenshot saved to %s\n", screenshot)
				res.Screenshot = ""
			}
			return writeScrapeResult(res, outFile)
		},
	}
	ff.register(c)
	fl := c.Flags()
	fl.StringSliceVarP(&formats, "formats", "f", nil, "outputs: markdown, html, text, links, metadata, product (default markdown,metadata)")
	fl.BoolVar(&mainOnly, "main-only", false, "keep only the main content (drop navigation, footers, ads)")
	fl.StringVar(&screenshot, "screenshot", "", "save a PNG screenshot to this file (uses the browser)")
	fl.BoolVar(&fullPage, "full-page", false, "screenshot the full page instead of the viewport")
	fl.StringVar(&extractFile, "extract", "", "JSON file with extraction fields (CSS/XPath schema)")
	fl.StringVar(&actionsFile, "actions", "", "JSON file with browser actions (click, scroll, type...)")
	fl.StringVar(&llmPrompt, "llm-prompt", "", "extract data with the configured LLM")
	fl.StringVar(&llmSchemaFile, "llm-schema", "", "JSON schema file for --llm-prompt output")
	fl.StringVar(&outFile, "out", "", "write the result to a file instead of stdout")
	return c
}

func splitCSV(in []string) []string {
	var out []string
	for _, v := range in {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func readJSONFile(path string, dst interface{}) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// writeScrapeResult prints JSON with -o json; otherwise the most useful text
// (Markdown, then text) with a short header on stderr.
func writeScrapeResult(res *crawler.ScrapeResult, outFile string) error {
	out := os.Stdout
	if outFile != "" {
		f, err := os.Create(outFile)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	if jsonOutput() {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(res)
	}
	fmt.Fprintf(os.Stderr, "%d %s  %s  via %s  %dms\n", res.StatusCode, res.FinalURL, res.ContentType, res.FetchedWith, res.DurationMs)
	if res.Error != "" {
		fmt.Fprintln(os.Stderr, "error:", res.Error)
	}
	if res.ExtractError != "" {
		fmt.Fprintln(os.Stderr, "extraction:", res.ExtractError)
	}
	switch {
	case !res.Detected.IsEmpty() || res.Extracted != nil || len(res.LLMExtracted) > 0:
		data := map[string]interface{}{}
		if b, err := json.Marshal(res.Detected); err == nil { // product, article, … as top-level keys
			_ = json.Unmarshal(b, &data)
		}
		if res.Extracted != nil {
			data["extracted"] = res.Extracted
		}
		if len(res.LLMExtracted) > 0 {
			data["llm_extracted"] = res.LLMExtracted
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(data)
	case res.Markdown != "":
		_, err := fmt.Fprintln(out, res.Markdown)
		return err
	case res.Text != "":
		_, err := fmt.Fprintln(out, res.Text)
		return err
	case res.HTML != "":
		_, err := fmt.Fprintln(out, res.HTML)
		return err
	case len(res.Links) > 0:
		_, err := fmt.Fprintln(out, strings.Join(res.Links, "\n"))
		return err
	case res.Metadata != nil:
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(res.Metadata)
	}
	return nil
}

func newMapCmd() *cobra.Command {
	var ff fetchFlags
	var search string
	var limit int
	var subdomains, noSitemap bool
	c := &cobra.Command{
		Use:   "map <url>",
		Short: "List a site's URLs from robots.txt, sitemaps and the start page (no full crawl)",
		Example: `  mole map https://example.com
  mole map digikala.com --search /product/ --limit 1000
  mole map example.ir --subdomains -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := ff.settings()
			if err != nil {
				return err
			}
			a, err := openEngineOnly()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			res, err := a.Engine.Map(ctx, crawler.MapRequest{URL: args[0], Search: search, Limit: limit,
				IncludeSubdomains: subdomains, IgnoreSitemap: noSitemap, Settings: s})
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(res)
			}
			for _, u := range res.URLs {
				fmt.Println(u)
			}
			note := ""
			if res.Truncated {
				note = " (limit reached)"
			}
			fmt.Fprintf(os.Stderr, "%d URLs%s — %d from %d sitemaps, %d from links, %dms\n",
				res.Total, note, res.FromSitemap, len(res.Sitemaps), res.FromLinks, res.DurationMs)
			return nil
		},
	}
	ff.register(c)
	c.Flags().StringVar(&search, "search", "", "only URLs containing this text")
	c.Flags().IntVar(&limit, "limit", 5000, "maximum URLs (max 50000)")
	c.Flags().BoolVar(&subdomains, "subdomains", false, "include other subdomains of the site")
	c.Flags().BoolVar(&noSitemap, "no-sitemap", false, "skip robots.txt and sitemaps")
	return c
}
