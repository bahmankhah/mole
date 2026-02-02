package modules

import (
	"bytes"
	"log"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// HTMLLinkExtractor extracts links from HTML content
type HTMLLinkExtractor struct {
	urlCleaner *URLCleaner
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
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}

	linkSet := make(map[string]struct{})

	// Extract href attributes from <a> tags
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if exists && href != "" {
			e.addLink(baseURL, href, linkSet)
		}
	})

	// Extract src from <img>, <script>, <iframe>
	doc.Find("img[src], script[src], iframe[src]").Each(func(_ int, s *goquery.Selection) {
		src, exists := s.Attr("src")
		if exists && src != "" {
			e.addLink(baseURL, src, linkSet)
		}
	})

	// Extract href from <link> tags (CSS, etc.)
	doc.Find("link[href]").Each(func(_ int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if exists && href != "" {
			e.addLink(baseURL, href, linkSet)
		}
	})

	// Extract action from <form> tags
	doc.Find("form[action]").Each(func(_ int, s *goquery.Selection) {
		action, exists := s.Attr("action")
		if exists && action != "" {
			e.addLink(baseURL, action, linkSet)
		}
	})

	// Convert set to slice
	links := make([]string, 0, len(linkSet))
	for link := range linkSet {
		links = append(links, link)
	}

	return links, nil
}

// addLink resolves and adds a link to the set
func (e *HTMLLinkExtractor) addLink(baseURL, href string, linkSet map[string]struct{}) {
	href = strings.TrimSpace(href)

	// Skip empty, javascript, mailto, tel links
	if href == "" || strings.HasPrefix(href, "javascript:") ||
		strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "tel:") ||
		strings.HasPrefix(href, "data:") || strings.HasPrefix(href, "#") {
		return
	}

	// Resolve relative URL
	resolved, err := e.urlCleaner.ResolveURL(baseURL, href)
	if err != nil {
		return
	}

	// Only add HTTP(S) links
	parsedURL, err := url.Parse(resolved)
	if err != nil {
		return
	}

	if parsedURL.Scheme == "http" || parsedURL.Scheme == "https" {
		linkSet[resolved] = struct{}{}
	}
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
	space := regexp.MustCompile(`[\s]+`)
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
