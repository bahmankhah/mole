// Package content turns fetched HTML into the outputs customers ask for:
// page metadata, main-content Markdown/text, links and structured data.
package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/JohannesKaufmann/html-to-markdown/plugin"
	"github.com/PuerkitoBio/goquery"
	readability "github.com/go-shiori/go-readability"
	"github.com/resolver/crawler/modules"
)

// Options selects what Analyze computes.
type Options struct {
	Markdown        bool
	HTML            bool // cleaned HTML of the (main) content
	Text            bool
	Links           bool
	Metadata        bool
	OnlyMainContent bool
}

// Result is the analysis of one HTML page.
type Result struct {
	Title       string
	Markdown    string
	HTML        string
	Text        string
	Links       []string
	Metadata    map[string]interface{}
	ContentHash string // hash of the normalized main text, stable across cosmetic changes
}

// noiseSelector matches elements never worth converting.
const noiseSelector = "script, style, noscript, template, svg, canvas, iframe, object, embed, link, meta"

// boilerplateSelector matches page chrome removed when readability finds no article.
const boilerplateSelector = "nav, header, footer, aside, form, [role=navigation], [role=banner], [role=contentinfo], .cookie, .cookies, #cookie-banner, .advertisement, .ads"

// Analyze parses body (HTML) fetched from pageURL.
func Analyze(pageURL string, body []byte, opts Options) (*Result, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(pageURL)
	if href, ok := doc.Find("base[href]").First().Attr("href"); ok && base != nil {
		if b, err := base.Parse(strings.TrimSpace(href)); err == nil {
			base = b
		}
	}

	res := &Result{Title: strings.TrimSpace(doc.Find("title").First().Text())}
	if opts.Metadata {
		res.Metadata = ExtractMetadata(doc, base)
	}
	if opts.Links {
		res.Links = ExtractLinks(doc, base)
	}

	mainHTML, mainText := mainContent(body, doc, base, opts.OnlyMainContent)
	res.ContentHash = HashText(mainText)
	if opts.Text {
		res.Text = mainText
	}
	if opts.HTML {
		res.HTML = mainHTML
	}
	if opts.Markdown {
		res.Markdown = ToMarkdown(mainHTML, base)
	}
	if res.Metadata != nil {
		res.Metadata["word_count"] = len(strings.Fields(mainText))
	}
	return res, nil
}

// mainContent returns the HTML and plain text of the page body, reduced to the
// main article when onlyMain is set.
func mainContent(body []byte, doc *goquery.Document, base *url.URL, onlyMain bool) (string, string) {
	if onlyMain {
		if art, err := readability.FromReader(bytes.NewReader(body), base); err == nil && len(strings.TrimSpace(art.TextContent)) > 200 {
			text := cleanText(art.TextContent)
			// Readability strips the headline from the content; put the page's
			// own <h1> (or the article title) back on top.
			t := strings.Join(strings.Fields(doc.Find("h1").First().Text()), " ")
			if t == "" {
				t = strings.TrimSpace(art.Title)
			}
			if t != "" && !strings.Contains(text, t) {
				return "<h1>" + html.EscapeString(t) + "</h1>" + art.Content, t + "\n\n" + text
			}
			return art.Content, text
		}
	}
	clone := goquery.CloneDocument(doc)
	clone.Find(noiseSelector).Remove()
	if onlyMain {
		clone.Find(boilerplateSelector).Remove()
	}
	sel := clone.Find("body")
	if sel.Length() == 0 {
		sel = clone.Selection
	}
	h, _ := sel.Html()
	return h, cleanText(sel.Text())
}

var (
	spaceRun   = regexp.MustCompile(`[ \t\x{00a0}]+`)
	newlineRun = regexp.MustCompile(`\n\s*\n+`)
)

func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = spaceRun.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = strings.Join(lines, "\n")
	return strings.TrimSpace(newlineRun.ReplaceAllString(s, "\n\n"))
}

// HashText returns a hash of text after Persian/whitespace normalization, so
// insignificant differences do not register as changes.
func HashText(text string) string {
	norm := strings.Join(strings.Fields(modules.ToLatinDigits(modules.NormalizePersian(text))), " ")
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// unwrapSelector matches inline elements with no Markdown equivalent. The
// converter drops the whitespace between them ("<span>a</span> <span>b</span>"
// becomes "ab"), so they are replaced by their children first.
const unwrapSelector = "span, font, time, small, abbr, label, data, mark, cite, u, ins, bdi, bdo, q, big, center"

// ToMarkdown converts HTML to GitHub-flavored Markdown with absolute links.
func ToMarkdown(htmlStr string, base *url.URL) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
	if err != nil {
		return ""
	}
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		if abs := resolveAny(base, s.AttrOr("href", "")); abs != "" {
			s.SetAttr("href", abs)
		}
	})
	doc.Find("img[src]").Each(func(_ int, s *goquery.Selection) {
		if abs := resolveAny(base, s.AttrOr("src", "")); abs != "" {
			s.SetAttr("src", abs)
		}
	})
	// Unwrap innermost elements first so nested matches are all removed.
	unwrap := doc.Find(unwrapSelector)
	for i := unwrap.Length() - 1; i >= 0; i-- {
		el := unwrap.Eq(i)
		el.ReplaceWithSelection(el.Contents())
	}
	body, err := doc.Find("body").Html()
	if err != nil {
		return ""
	}
	conv := md.NewConverter("", true, nil)
	conv.Use(plugin.GitHubFlavored())
	out, err := conv.ConvertString(body)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(newlineRun.ReplaceAllString(out, "\n\n"))
}

// ExtractLinks returns the unique absolute http(s) links on the page.
func ExtractLinks(doc *goquery.Document, base *url.URL) []string {
	seen := map[string]bool{}
	var out []string
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		abs := resolve(base, href)
		if abs == "" || seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	})
	return out
}

func resolve(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// ExtractMetadata collects title, description, language, canonical URL,
// OpenGraph/Twitter tags, dates and JSON-LD.
func ExtractMetadata(doc *goquery.Document, base *url.URL) map[string]interface{} {
	m := map[string]interface{}{}
	set := func(k, v string) {
		v = strings.TrimSpace(v)
		if v != "" {
			if _, exists := m[k]; !exists {
				m[k] = v
			}
		}
	}
	set("title", doc.Find("title").First().Text())
	set("language", doc.Find("html").AttrOr("lang", ""))
	set("direction", doc.Find("html").AttrOr("dir", ""))
	if href, ok := doc.Find(`link[rel="canonical"]`).Attr("href"); ok {
		set("canonical", resolve(base, href))
	}
	doc.Find(`link[rel~="icon"]`).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		set("favicon", resolve(base, s.AttrOr("href", "")))
		return false
	})

	og := map[string]string{}
	doc.Find("meta").Each(func(_ int, s *goquery.Selection) {
		key := strings.ToLower(strings.TrimSpace(s.AttrOr("property", s.AttrOr("name", s.AttrOr("itemprop", "")))))
		val := s.AttrOr("content", "")
		if key == "" || strings.TrimSpace(val) == "" {
			return
		}
		switch {
		case key == "description", key == "keywords", key == "author", key == "robots", key == "generator":
			set(key, val)
		case strings.HasPrefix(key, "og:"), strings.HasPrefix(key, "twitter:"):
			if _, ok := og[key]; !ok {
				og[key] = strings.TrimSpace(val)
			}
		case key == "article:published_time", key == "datepublished", key == "date", key == "pubdate":
			set("published_time", val)
		case key == "article:modified_time", key == "datemodified", key == "last-modified":
			set("modified_time", val)
		}
	})
	if len(og) > 0 {
		m["open_graph"] = og
		set("description", og["og:description"])
		set("image", og["og:image"])
		set("site_name", og["og:site_name"])
	}
	if t, ok := doc.Find("time[datetime]").First().Attr("datetime"); ok {
		set("published_time", t)
	}
	if v, ok := m["published_time"].(string); ok {
		if t, ok := modules.ParseDateAny(v); ok {
			m["published_at"] = t.UTC().Format("2006-01-02T15:04:05Z")
		}
	}

	var ld []interface{}
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		raw := strings.TrimSpace(s.Text())
		if raw == "" || len(raw) > 200000 {
			return
		}
		var v interface{}
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			ld = append(ld, v)
		}
	})
	if len(ld) > 0 {
		m["json_ld"] = ld
	}
	return m
}
