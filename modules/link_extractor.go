package modules

import (
	"bytes"
	"log"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Pre-compiled regex for whitespace normalization (avoids re-compiling on every call)
var whitespaceRegex = regexp.MustCompile(`[\s]+`)

// Pre-compiled regex for finding bare URLs in text/JSON/scripts.
// Matches http:// and https:// URLs, stops at whitespace, quotes, angle brackets, or common delimiters.
var bareURLRegex = regexp.MustCompile(`https?://[^\s"'<>\[\]{}|\\` + "`" + `(),;]+`)

// HTMLLinkExtractor extracts links from HTML content
type HTMLLinkExtractor struct {
	urlCleaner *URLCleaner
}

// LinkWithAnchor holds a link URL along with its anchor text
type LinkWithAnchor struct {
	URL        string
	AnchorText string
}

// NewHTMLLinkExtractor creates a new link extractor
func NewHTMLLinkExtractor(urlCleaner *URLCleaner) *HTMLLinkExtractor {
	return &HTMLLinkExtractor{
		urlCleaner: urlCleaner,
	}
}

// Name returns the module name
func (e *HTMLLinkExtractor) Name() string {
	return "html_link_extractor"
}

// Initialize sets up the module
func (e *HTMLLinkExtractor) Initialize() error {
	log.Printf("[%s] Initialized", e.Name())
	return nil
}

// Shutdown gracefully stops the module
func (e *HTMLLinkExtractor) Shutdown() error {
	return nil
}

// ExtractLinks extracts all links from HTML content
func (e *HTMLLinkExtractor) ExtractLinks(baseURL string, content []byte) ([]string, error) {
	linksWithAnchors, err := e.ExtractLinksWithAnchors(baseURL, content)
	if err != nil {
		return nil, err
	}
	links := make([]string, 0, len(linksWithAnchors))
	for _, la := range linksWithAnchors {
		links = append(links, la.URL)
	}
	return links, nil
}

// ExtractLinksWithAnchors extracts all links from HTML content along with their anchor text
func (e *HTMLLinkExtractor) ExtractLinksWithAnchors(baseURL string, content []byte) ([]LinkWithAnchor, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}

	type linkEntry struct {
		url        string
		anchorText string
	}

	linkMap := make(map[string]linkEntry)

	// Extract href attributes from <a> tags with anchor text
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if exists && href != "" {
			resolved := e.resolveLink(baseURL, href)
			if resolved != "" {
				anchorText := strings.TrimSpace(s.Text())
				if existing, ok := linkMap[resolved]; ok {
					// Append anchor text if different
					if anchorText != "" && !strings.Contains(existing.anchorText, anchorText) {
						if existing.anchorText != "" {
							existing.anchorText += " | " + anchorText
						} else {
							existing.anchorText = anchorText
						}
						linkMap[resolved] = existing
					}
				} else {
					linkMap[resolved] = linkEntry{url: resolved, anchorText: anchorText}
				}
			}
		}
	})

	// Extract src from <img>, <script>, <iframe>
	doc.Find("img[src], script[src], iframe[src]").Each(func(_ int, s *goquery.Selection) {
		src, exists := s.Attr("src")
		if exists && src != "" {
			resolved := e.resolveLink(baseURL, src)
			if resolved != "" {
				if _, ok := linkMap[resolved]; !ok {
					linkMap[resolved] = linkEntry{url: resolved}
				}
			}
		}
	})

	// Extract href from <link> tags (CSS, etc.)
	doc.Find("link[href]").Each(func(_ int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if exists && href != "" {
			resolved := e.resolveLink(baseURL, href)
			if resolved != "" {
				if _, ok := linkMap[resolved]; !ok {
					linkMap[resolved] = linkEntry{url: resolved}
				}
			}
		}
	})

	// Extract action from <form> tags
	doc.Find("form[action]").Each(func(_ int, s *goquery.Selection) {
		action, exists := s.Attr("action")
		if exists && action != "" {
			resolved := e.resolveLink(baseURL, action)
			if resolved != "" {
				if _, ok := linkMap[resolved]; !ok {
					linkMap[resolved] = linkEntry{url: resolved}
				}
			}
		}
	})

	// Extract URLs from data-href, data-url, data-src, data-link attributes
	doc.Find("[data-href], [data-url], [data-src], [data-link]").Each(func(_ int, s *goquery.Selection) {
		for _, attr := range []string{"data-href", "data-url", "data-src", "data-link"} {
			val, exists := s.Attr(attr)
			if exists && val != "" {
				resolved := e.resolveLink(baseURL, val)
				if resolved != "" {
					if _, ok := linkMap[resolved]; !ok {
						linkMap[resolved] = linkEntry{url: resolved}
					}
				}
			}
		}
	})

	// Extract canonical/alternate URLs from <meta> and <link> tags
	doc.Find(`link[rel="canonical"], link[rel="alternate"]`).Each(func(_ int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if exists && href != "" {
			resolved := e.resolveLink(baseURL, href)
			if resolved != "" {
				if _, ok := linkMap[resolved]; !ok {
					linkMap[resolved] = linkEntry{url: resolved}
				}
			}
		}
	})

	// Extract URLs from meta refresh and og:url, og:image etc.
	doc.Find(`meta[content]`).Each(func(_ int, s *goquery.Selection) {
		content, _ := s.Attr("content")
		if content == "" {
			return
		}
		// meta http-equiv="refresh" content="0;url=..."
		if httpEquiv, _ := s.Attr("http-equiv"); strings.EqualFold(httpEquiv, "refresh") {
			if idx := strings.Index(strings.ToLower(content), "url="); idx != -1 {
				refreshURL := strings.TrimSpace(content[idx+4:])
				resolved := e.resolveLink(baseURL, refreshURL)
				if resolved != "" {
					if _, ok := linkMap[resolved]; !ok {
						linkMap[resolved] = linkEntry{url: resolved}
					}
				}
			}
		}
		// og:url, og:image, twitter:image, etc.
		prop, _ := s.Attr("property")
		name, _ := s.Attr("name")
		if strings.HasPrefix(prop, "og:") || strings.HasPrefix(name, "twitter:") {
			if strings.HasPrefix(content, "http://") || strings.HasPrefix(content, "https://") {
				resolved := e.resolveLink(baseURL, content)
				if resolved != "" {
					if _, ok := linkMap[resolved]; !ok {
						linkMap[resolved] = linkEntry{url: resolved}
					}
				}
			}
		}
	})

	// Extract URLs from srcset attributes (responsive images)
	doc.Find("[srcset]").Each(func(_ int, s *goquery.Selection) {
		srcset, exists := s.Attr("srcset")
		if !exists || srcset == "" {
			return
		}
		// srcset format: "url1 1x, url2 2x" or "url1 100w, url2 200w"
		for _, candidate := range strings.Split(srcset, ",") {
			parts := strings.Fields(strings.TrimSpace(candidate))
			if len(parts) >= 1 {
				resolved := e.resolveLink(baseURL, parts[0])
				if resolved != "" {
					if _, ok := linkMap[resolved]; !ok {
						linkMap[resolved] = linkEntry{url: resolved}
					}
				}
			}
		}
	})

	// Extract bare URLs from inline <script> and JSON-LD blocks
	// This catches URLs embedded in JavaScript objects, JSON-LD, Next.js data, etc.
	rawDoc, _ := goquery.NewDocumentFromReader(bytes.NewReader(content))
	if rawDoc != nil {
		rawDoc.Find("script").Each(func(_ int, s *goquery.Selection) {
			scriptText := s.Text()
			if len(scriptText) == 0 || len(scriptText) > 512*1024 {
				return // skip empty or very large scripts
			}
			matches := bareURLRegex.FindAllString(scriptText, 200) // cap at 200 per script block
			for _, raw := range matches {
				// Clean trailing punctuation that the regex might grab
				raw = strings.TrimRight(raw, ".,;:!?)]}")
				resolved := e.resolveLink(baseURL, raw)
				if resolved != "" {
					if _, ok := linkMap[resolved]; !ok {
						linkMap[resolved] = linkEntry{url: resolved}
					}
				}
			}
		})
	}

	// Convert map to slice
	result := make([]LinkWithAnchor, 0, len(linkMap))
	for _, entry := range linkMap {
		result = append(result, LinkWithAnchor{URL: entry.url, AnchorText: entry.anchorText})
	}

	return result, nil
}

// resolveLink resolves a relative link and validates it
func (e *HTMLLinkExtractor) resolveLink(baseURL, href string) string {
	href = strings.TrimSpace(href)

	// Skip empty, javascript, mailto, tel links
	if href == "" || strings.HasPrefix(href, "javascript:") ||
		strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "tel:") ||
		strings.HasPrefix(href, "data:") {
		return ""
	}

	// For fragment-only hrefs (start with #):
	// Allow SPA routes (e.g. #/articles/..., #/search?q=...) but skip plain
	// anchors (e.g. #top, #section-1) which are same-page references.
	if strings.HasPrefix(href, "#") {
		if !HasMeaningfulFragment(baseURL + href) {
			return ""
		}
		// SPA route — resolve as full URL by prepending base origin
		parsedBase, err := url.Parse(baseURL)
		if err != nil {
			return ""
		}
		fullURL := parsedBase.Scheme + "://" + parsedBase.Host + "/" + href
		cleaned, err := e.urlCleaner.ProcessURLKeepFragment(fullURL)
		if err != nil {
			return ""
		}
		return cleaned
	}

	// Resolve relative URL
	resolved, err := e.urlCleaner.ResolveURL(baseURL, href)
	if err != nil {
		return ""
	}

	// Only add HTTP(S) links
	parsedURL, err := url.Parse(resolved)
	if err != nil {
		return ""
	}

	if parsedURL.Scheme == "http" || parsedURL.Scheme == "https" {
		return resolved
	}
	return ""
}

// ExtractTitle extracts the page title
func (e *HTMLLinkExtractor) ExtractTitle(content []byte) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(content))
	if err != nil {
		return ""
	}

	title := doc.Find("title").First().Text()
	return strings.TrimSpace(title)
}

// ExtractTextContent extracts text content from HTML
func (e *HTMLLinkExtractor) ExtractTextContent(content []byte) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(content))
	if err != nil {
		return string(content)
	}

	// Remove script and style elements
	doc.Find("script, style, noscript").Remove()

	// Get text content
	text := doc.Text()

	// Clean up whitespace
	space := whitespaceRegex
	text = space.ReplaceAllString(text, " ")

	return strings.TrimSpace(text)
}

// SitemapParser parses sitemap.xml and sitemap index files
type SitemapParser struct {
	urlCleaner *URLCleaner
}

// NewSitemapParser creates a new sitemap parser
func NewSitemapParser(urlCleaner *URLCleaner) *SitemapParser {
	return &SitemapParser{
		urlCleaner: urlCleaner,
	}
}

// Name returns the module name
func (s *SitemapParser) Name() string {
	return "sitemap_parser"
}

// Initialize sets up the module
func (s *SitemapParser) Initialize() error {
	log.Printf("[%s] Initialized", s.Name())
	return nil
}

// Shutdown gracefully stops the module
func (s *SitemapParser) Shutdown() error {
	return nil
}

// ParseSitemap extracts URLs from a sitemap XML
func (s *SitemapParser) ParseSitemap(content []byte) ([]string, []string, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(content))
	if err != nil {
		return nil, nil, err
	}

	urls := []string{}
	sitemaps := []string{}

	// Extract URLs from <url><loc> tags
	doc.Find("url loc").Each(func(_ int, sel *goquery.Selection) {
		loc := strings.TrimSpace(sel.Text())
		if loc != "" {
			if cleaned, err := s.urlCleaner.ProcessURL(loc); err == nil {
				urls = append(urls, cleaned)
			}
		}
	})

	// Extract sitemap references from <sitemap><loc> tags (sitemap index)
	doc.Find("sitemap loc").Each(func(_ int, sel *goquery.Selection) {
		loc := strings.TrimSpace(sel.Text())
		if loc != "" {
			if cleaned, err := s.urlCleaner.ProcessURL(loc); err == nil {
				sitemaps = append(sitemaps, cleaned)
			}
		}
	})

	return urls, sitemaps, nil
}

// RobotsParser parses robots.txt
type RobotsParser struct {
	urlCleaner *URLCleaner
}

// NewRobotsParser creates a new robots.txt parser
func NewRobotsParser(urlCleaner *URLCleaner) *RobotsParser {
	return &RobotsParser{
		urlCleaner: urlCleaner,
	}
}

// Name returns the module name
func (r *RobotsParser) Name() string {
	return "robots_parser"
}

// Initialize sets up the module
func (r *RobotsParser) Initialize() error {
	log.Printf("[%s] Initialized", r.Name())
	return nil
}

// Shutdown gracefully stops the module
func (r *RobotsParser) Shutdown() error {
	return nil
}

// RobotsResult contains parsed robots.txt data
type RobotsResult struct {
	Sitemaps        []string
	AllowedPaths    []string
	DisallowedPaths []string
	CrawlDelay      int
}

// ParseRobots parses robots.txt content
func (r *RobotsParser) ParseRobots(baseURL string, content []byte) *RobotsResult {
	result := &RobotsResult{
		Sitemaps:        []string{},
		AllowedPaths:    []string{},
		DisallowedPaths: []string{},
	}

	lines := strings.Split(string(content), "\n")
	currentUserAgent := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		directive := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])

		switch directive {
		case "user-agent":
			currentUserAgent = strings.ToLower(value)
		case "sitemap":
			if cleaned, err := r.urlCleaner.ResolveURL(baseURL, value); err == nil {
				result.Sitemaps = append(result.Sitemaps, cleaned)
			}
		case "allow":
			if currentUserAgent == "*" || currentUserAgent == "" {
				result.AllowedPaths = append(result.AllowedPaths, value)
			}
		case "disallow":
			if currentUserAgent == "*" || currentUserAgent == "" {
				result.DisallowedPaths = append(result.DisallowedPaths, value)
			}
		}
	}

	return result
}
