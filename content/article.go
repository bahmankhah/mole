package content

import (
	"net/url"
	"strings"
)

// Article is the news story or blog post a page publishes.
type Article struct {
	Headline    string            `json:"headline,omitempty"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type,omitempty"` // schema.org type: NewsArticle, BlogPosting, …
	Authors     []string          `json:"authors,omitempty"`
	Publisher   string            `json:"publisher,omitempty"`
	PublishedAt string            `json:"published_at,omitempty"` // RFC3339; Jalali dates are converted
	ModifiedAt  string            `json:"modified_at,omitempty"`
	Section     string            `json:"section,omitempty"`
	Keywords    []string          `json:"keywords,omitempty"`
	Image       string            `json:"image,omitempty"`
	URL         string            `json:"url,omitempty"`
	Language    string            `json:"language,omitempty"`
	WordCount   int               `json:"word_count,omitempty"`
	Sources     map[string]string `json:"sources,omitempty"`
}

var articleTypes = []string{
	"Article", "NewsArticle", "BlogPosting", "Report", "ScholarlyArticle", "TechArticle", "LiveBlogPosting",
	"AnalysisNewsArticle", "OpinionNewsArticle", "ReportageNewsArticle", "BackgroundNewsArticle", "ReviewNewsArticle",
}

// detectArticle finds the article a page publishes, from schema.org data or
// OpenGraph article tags; page markup only fills gaps.
func detectArticle(pg *pageData) *Article {
	out := &Article{Sources: map[string]string{}}
	found := false
	for _, src := range pg.schemaSources() {
		if m := mainItem(findItems(src.items, ofTypes(articleTypes), nil), pg.base); m != nil {
			found = true
			mergeInto(out, articleFromSchema(m, pg.base), src.name, out.Sources)
		}
	}
	if strings.Contains(strings.ToLower(pg.metaFirst("og:type")), "article") {
		found = true
		mergeInto(out, articleFromOpenGraph(pg), SourceOpenGraph, out.Sources)
	}
	if !found {
		return nil
	}
	mergeInto(out, articleFromMeta(pg), SourceMeta, out.Sources)
	mergeInto(out, articleFromHTML(pg), SourceHTML, out.Sources)
	if out.Headline == "" {
		return nil
	}
	return out
}

// articleFromPage reads an article page that has no schema.org or OpenGraph
// article data from its meta tags and markup.
func articleFromPage(pg *pageData) *Article {
	out := &Article{Sources: map[string]string{}}
	mergeInto(out, articleFromMeta(pg), SourceMeta, out.Sources)
	mergeInto(out, articleFromHTML(pg), SourceHTML, out.Sources)
	if out.Headline == "" {
		return nil
	}
	return out
}

func articleFromSchema(m map[string]interface{}, base *url.URL) *Article {
	a := &Article{
		Headline:    cleanInline(schemaString(m["headline"])),
		Description: truncateRunes(cleanInline(schemaString(m["description"])), maxDescription),
		Publisher:   cleanInline(schemaName(m["publisher"])),
		PublishedAt: schemaDate(schemaString(m["datePublished"])),
		ModifiedAt:  schemaDate(schemaString(m["dateModified"])),
		Section:     cleanInline(schemaString(m["articleSection"])),
		Keywords:    schemaStrings(m["keywords"], true),
		URL:         resolveAny(base, schemaString(m["url"])),
		Language:    schemaString(m["inLanguage"]),
		WordCount:   int(schemaNumber(m["wordCount"])),
	}
	for _, t := range schemaList(m["@type"]) {
		if s := schemaEnum(schemaString(t)); hasAnyType(s, articleTypes) {
			a.Type = s
			break
		}
	}
	if a.Headline == "" {
		a.Headline = cleanInline(schemaString(m["name"]))
	}
	if a.PublishedAt == "" {
		a.PublishedAt = schemaDate(schemaString(m["dateCreated"]))
	}
	if a.URL == "" {
		a.URL = resolveAny(base, schemaRef(m["mainEntityOfPage"]))
	}
	for _, au := range schemaList(m["author"]) {
		if name := cleanInline(schemaName(au)); name != "" && !looksLikeURL(name) {
			a.Authors = appendUnique(a.Authors, name)
		}
	}
	if imgs := schemaImages(m["image"], base, 1); len(imgs) > 0 {
		a.Image = imgs[0]
	}
	return a
}

func articleFromOpenGraph(pg *pageData) *Article {
	a := &Article{
		Headline:    cleanInline(pg.metaFirst("og:title")),
		Description: truncateRunes(cleanInline(pg.metaFirst("og:description")), maxDescription),
		PublishedAt: schemaDate(pg.metaFirst("article:published_time")),
		ModifiedAt:  schemaDate(pg.metaFirst("article:modified_time", "og:updated_time")),
		Section:     pg.metaFirst("article:section"),
		Publisher:   pg.metaFirst("og:site_name"),
		Image:       resolveAny(pg.base, pg.metaFirst("og:image:secure_url", "og:image")),
		URL:         resolveAny(pg.base, pg.metaFirst("og:url")),
	}
	for _, tag := range pg.meta["article:tag"] {
		a.Keywords = appendUnique(a.Keywords, cleanInline(tag))
	}
	for _, au := range pg.meta["article:author"] {
		if !looksLikeURL(au) {
			a.Authors = appendUnique(a.Authors, cleanInline(au))
		}
	}
	return a
}

// articleFromMeta reads the classic meta tags many Persian news sites use.
func articleFromMeta(pg *pageData) *Article {
	a := &Article{
		Description: truncateRunes(cleanInline(pg.metaFirst("description")), maxDescription),
		PublishedAt: schemaDate(pg.metaFirst("datepublished", "pubdate", "publish-date", "date", "dc.date", "dcterms.created")),
		ModifiedAt:  schemaDate(pg.metaFirst("datemodified", "last-modified", "dcterms.modified")),
	}
	for _, au := range pg.meta["author"] {
		if !looksLikeURL(au) {
			a.Authors = appendUnique(a.Authors, cleanInline(au))
		}
	}
	a.Keywords = schemaStrings(pg.metaFirst("keywords", "news_keywords"), true)
	return a
}

func articleFromHTML(pg *pageData) *Article {
	a := &Article{
		Headline: strings.Join(strings.Fields(pg.doc.Find("article h1, h1").First().Text()), " "),
		Language: strings.TrimSpace(pg.doc.Find("html").AttrOr("lang", "")),
	}
	if t, ok := pg.doc.Find("article time[datetime], time[datetime]").First().Attr("datetime"); ok {
		a.PublishedAt = schemaDate(t)
	}
	if href, ok := pg.doc.Find(`link[rel="canonical"]`).Attr("href"); ok {
		a.URL = resolveAny(pg.base, href)
	}
	return a
}

func looksLikeURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "/")
}

func appendUnique(list []string, s string) []string {
	if s == "" {
		return list
	}
	for _, have := range list {
		if strings.EqualFold(have, s) {
			return list
		}
	}
	return append(list, s)
}
