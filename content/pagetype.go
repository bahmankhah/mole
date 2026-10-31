package content

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"golang.org/x/net/html"
)

// Page is a parsed HTML page shared by the classifier and the detectors, so
// the HTML is parsed once.
type Page struct {
	URL    string
	pg     *pageData
	layout *layout
}

// ParsePage parses an HTML page.
func ParsePage(pageURL string, body []byte) (*Page, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(pageURL)
	return &Page{URL: pageURL, pg: newPageData(root, base)}, nil
}

func (p *Page) lay() *layout {
	if p.layout == nil {
		p.layout = analyzeLayout(p.pg.root)
	}
	return p.layout
}

// Features are the page's template marks (class names, body classes, the
// framework route), for TemplateIndex.
func (p *Page) Features() []string { return p.lay().features }

// Classification is a page's type and what decided it.
type Classification struct {
	Type       string         `json:"type"`
	Confidence string         `json:"confidence"`        // high, medium or low
	Signals    []string       `json:"signals,omitempty"` // evidence for Type
	Scores     map[string]int `json:"scores,omitempty"`  // every type that scored
}

// PageTypeInfo describes a page type for UIs and API clients (GET /v1/info).
// Every page type except "other" is also a tag pages get automatically.
type PageTypeInfo struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Detector    string `json:"detector,omitempty"` // the format that extracts this type's data
}

// PageTypes lists the page types in the order UIs show them.
var PageTypes = []PageTypeInfo{
	{models.PageTypeProduct, "Product", "One product or classified ad: a shop's product page, a Divar ad.", models.FormatProduct},
	{models.PageTypeListing, "Listing", "Category, search, tag and archive pages that list many items.", ""},
	{models.PageTypeArticle, "Article", "News stories and blog posts.", models.FormatArticle},
	{models.PageTypeJobPosting, "Job posting", "One job ad.", models.FormatJobPosting},
	{models.PageTypeEvent, "Event", "One event: concert, conference, course.", models.FormatEvent},
	{models.PageTypeFAQ, "FAQ", "A page of questions and answers.", models.FormatFAQ},
	{models.PageTypeBusiness, "Business", "Contact, about and business profile pages.", models.FormatBusiness},
	{models.PageTypeHome, "Home", "The site's home page.", ""},
	{models.PageTypeDocument, "Document", "PDF and DOCX files.", ""},
}

// Confidence levels.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

const (
	minTypeScore  = 5 // below this the page is "other"
	highScore     = 9 // with a lead of highMargin: high confidence (a buy button and a price)
	highMargin    = 6
	mediumMargin  = 3
	maxSignalsOut = 8
)

// SignalString joins the signals for storage (crawled_pages.type_signals).
func (c Classification) SignalString() string {
	s := strings.Join(c.Signals, ",")
	if len(s) > 255 {
		s = s[:255]
	}
	return s
}

// Classify decides what kind of page this is from its HTML alone: schema.org
// data, OpenGraph tags, the page-kind marks shop and blog platforms put on
// their templates (WooCommerce, PrestaShop, Magento, OpenCart, Shopify,
// WordPress, Drupal, Next.js routes), the page layout (a buy button outside
// item cards, repeated product cards, long article text) and, weakly, words
// in the URL.
func (p *Page) Classify() Classification {
	s := p.score()
	return s.result()
}

func (p *Page) score() *scorer {
	s := &scorer{score: map[string]int{}, why: map[string][]string{}}
	p.urlSignals(s)
	p.schemaSignals(s)
	p.metaSignals(s)
	p.platformSignals(s)
	p.layoutSignals(s)
	return s
}

type scorer struct {
	score map[string]int
	why   map[string][]string
}

// add scores a type once per signal name.
func (s *scorer) add(pageType string, points int, signal string) {
	for _, w := range s.why[pageType] {
		if w == signal {
			return
		}
	}
	s.score[pageType] += points
	s.why[pageType] = append(s.why[pageType], signal)
}

func (s *scorer) result() Classification {
	types := make([]string, 0, len(s.score))
	for t := range s.score {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		if s.score[types[i]] != s.score[types[j]] {
			return s.score[types[i]] > s.score[types[j]]
		}
		return types[i] < types[j]
	})
	out := Classification{Type: models.PageTypeOther, Confidence: ConfidenceLow}
	if len(s.score) > 0 {
		out.Scores = s.score
	}
	if len(types) == 0 || s.score[types[0]] < minTypeScore {
		return out
	}
	best := s.score[types[0]]
	second := 0
	if len(types) > 1 {
		second = s.score[types[1]]
	}
	out.Type = types[0]
	out.Signals = s.why[types[0]]
	if len(out.Signals) > maxSignalsOut {
		out.Signals = out.Signals[:maxSignalsOut]
	}
	switch margin := best - second; {
	case best >= highScore && margin >= highMargin:
		out.Confidence = ConfidenceHigh
	case margin >= mediumMargin:
		out.Confidence = ConfidenceMedium
	}
	return out
}

// ── URL ──────────────────────────────────────────────────────────────────

var (
	urlProductSegs  = setOf("product", "products", "p", "item", "items", "dp", "goods", "kala", "کالا", "محصول", "book", "pd", "ad")
	urlListingSegs  = setOf("category", "categories", "cat", "collections", "collection", "tag", "tags", "brand", "brands", "productlist", "product-list", "product-category", "product_cat", "list", "lists", "archive", "archives", "search", "s", "دسته", "دسته-بندی", "برچسب", "shop", "store", "catalog", "catalogue", "browse", "browsejobs", "service", "section", "topic", "topics")
	urlArticleSegs  = setOf("blog", "blogs", "news", "article", "articles", "post", "posts", "mag", "magazine", "story", "stories", "مقاله", "مقالات", "اخبار", "خبر", "وبلاگ", "مجله")
	urlJobSegs      = setOf("job", "jobs", "careers", "career", "vacancy", "vacancies", "استخدام", "آگهی-استخدام")
	urlEventSegs    = setOf("event", "events", "رویداد", "رویدادها")
	urlFAQSegs      = setOf("faq", "faqs", "سوالات-متداول", "پرسش-های-متداول", "سوالات-رایج")
	urlBusinessSegs = setOf("contact", "contact-us", "contactus", "about", "about-us", "aboutus", "تماس-با-ما", "درباره-ما")
	langSegRe       = regexp.MustCompile(`^[a-z]{2}([-_][a-z]{2})?$`)
	pageSegRe       = regexp.MustCompile(`^(page|صفحه)$`)
	productSegRe    = regexp.MustCompile(`^(product|p|item)[-_]\d+$`)
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

func (p *Page) urlSignals(s *scorer) {
	u := p.pg.base
	if u == nil {
		return
	}
	var segs []string
	for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		seg = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(seg, ".html"), ".htm"), ".php")
		if seg = strings.ToLower(modules.NormalizePersian(seg)); seg != "" {
			segs = append(segs, seg)
		}
	}
	q := u.Query()
	if (len(segs) == 0 || (len(segs) == 1 && langSegRe.MatchString(segs[0]) && !urlListingSegs[segs[0]])) && len(q) == 0 {
		s.add(models.PageTypeHome, 20, "url:root")
		return
	}
	for _, k := range []string{"q", "s", "search", "query", "keyword", "term"} {
		if q.Get(k) != "" {
			s.add(models.PageTypeListing, 4, "url:search")
		}
	}
	if q.Get("page") != "" || q.Get("pageno") != "" {
		s.add(models.PageTypeListing, 3, "url:page")
	}
	if route := q.Get("route"); route != "" { // OpenCart
		switch {
		case route == "product/product":
			s.add(models.PageTypeProduct, 12, "opencart:route=product/product")
		case strings.HasPrefix(route, "product/category"), strings.HasPrefix(route, "product/search"), strings.HasPrefix(route, "product/manufacturer"), strings.HasPrefix(route, "product/special"):
			s.add(models.PageTypeListing, 12, "opencart:route="+route)
		case route == "common/home":
			s.add(models.PageTypeHome, 14, "opencart:route=common/home")
		}
	}
	for i, seg := range segs {
		last := i == len(segs)-1
		switch {
		case pageSegRe.MatchString(seg) && !last:
			s.add(models.PageTypeListing, 3, "url:/page/n")
		case productSegRe.MatchString(seg):
			s.add(models.PageTypeProduct, 3, "url:/"+seg[:strings.IndexAny(seg, "-_")]+"-id")
		case urlProductSegs[seg]:
			if last && strings.HasSuffix(seg, "s") {
				s.add(models.PageTypeListing, 3, "url:/"+seg)
			} else if !last {
				s.add(models.PageTypeProduct, 3, "url:/"+seg+"/")
			}
		case urlListingSegs[seg]:
			s.add(models.PageTypeListing, 3, "url:/"+seg+"/")
		case urlJobSegs[seg]:
			if last {
				s.add(models.PageTypeListing, 3, "url:/"+seg)
			} else {
				s.add(models.PageTypeJobPosting, 3, "url:/"+seg+"/")
			}
		case urlEventSegs[seg]:
			if last {
				s.add(models.PageTypeListing, 3, "url:/"+seg)
			} else {
				s.add(models.PageTypeEvent, 3, "url:/"+seg+"/")
			}
		case urlFAQSegs[seg]:
			s.add(models.PageTypeFAQ, 5, "url:/"+seg)
		case urlBusinessSegs[seg]:
			s.add(models.PageTypeBusiness, 5, "url:/"+seg)
		case urlArticleSegs[seg] || strings.HasSuffix(seg, "-articles") || strings.HasSuffix(seg, "-news"):
			if last {
				s.add(models.PageTypeListing, 2, "url:/"+seg)
			} else {
				s.add(models.PageTypeArticle, 2, "url:/"+seg+"/")
			}
		}
	}
}

// ── schema.org ───────────────────────────────────────────────────────────

var (
	// itemForSaleTypes are products plus the things classified ads sell or rent.
	itemForSaleTypes = append(append([]string{}, productTypes...), "Apartment", "House", "SingleFamilyResidence",
		"Residence", "Accommodation", "Room", "Suite", "RealEstateListing")
	jobTypes        = []string{"JobPosting"}
	localBizTypes   = businessTypesExcept("Organization", "Corporation", "OnlineStore", "OnlineBusiness", "NewsMediaOrganization")
	listPageTypes   = []string{"CollectionPage", "SearchResultsPage"}
	itemListTypes   = []string{"ItemList", "OfferCatalog"}
	aboutPageTypes  = []string{"AboutPage", "ContactPage"}
	itemPageTypes   = []string{"ItemPage"}
	faqPageTypes    = []string{"FAQPage"}
	profilePageType = []string{"ProfilePage"}
)

func businessTypesExcept(drop ...string) []string {
	skip := setOf(drop...)
	var out []string
	for _, t := range businessTypes {
		if !skip[t] {
			out = append(out, t)
		}
	}
	return out
}

func (p *Page) schemaSignals(s *scorer) {
	base := p.pg.base
	for _, src := range p.pg.schemaSources() {
		if len(src.items) == 0 {
			continue
		}
		tag := src.name
		for _, k := range []struct {
			types     []string
			pageType  string
			points    int
			signal    string
			listPoint int
		}{
			{itemForSaleTypes, models.PageTypeProduct, 10, "Product", 6},
			{articleTypes, models.PageTypeArticle, 8, "Article", 5},
			{jobTypes, models.PageTypeJobPosting, 12, "JobPosting", 6},
			{eventTypes, models.PageTypeEvent, 12, "Event", 6},
		} {
			if m := mainItem(findItems(src.items, ofTypes(k.types), nil), base); m != nil {
				// A lone item whose url is another page of the site is a
				// listing with one result, not that item's page.
				if p.pointsElsewhere(m) {
					s.add(models.PageTypeListing, 6, tag+":"+k.signal+"→elsewhere")
				} else {
					s.add(k.pageType, k.points, tag+":"+k.signal)
				}
			} else if n := len(allItems(src.items, ofTypes(k.types))); n >= 2 {
				points := k.listPoint
				if n >= 4 {
					points += 4
				}
				s.add(models.PageTypeListing, points, fmt.Sprintf("%s:%s×%d", tag, k.signal, n))
			}
		}
		present := typesPresent(src.items)
		has := func(types []string) bool {
			for _, t := range types {
				if present[strings.ToLower(t)] {
					return true
				}
			}
			return false
		}
		if has(listPageTypes) {
			s.add(models.PageTypeListing, 8, tag+":CollectionPage")
		}
		if has(itemListTypes) {
			s.add(models.PageTypeListing, 3, tag+":ItemList")
		}
		if has(aboutPageTypes) {
			s.add(models.PageTypeBusiness, 8, tag+":ContactPage")
		}
		if has(itemPageTypes) {
			s.add(models.PageTypeProduct, 4, tag+":ItemPage")
		}
		if has(faqPageTypes) {
			s.add(models.PageTypeFAQ, 5, tag+":FAQPage")
		}
		if has(profilePageType) {
			s.add(models.PageTypeOther, 5, tag+":ProfilePage")
		}
		for _, m := range findItems(src.items, ofTypes(localBizTypes), businessSkipKeys) {
			if sameDocument(base, resolveAny(base, schemaString(m["url"]))) || sameDocument(base, resolveAny(base, schemaString(m["@id"]))) {
				s.add(models.PageTypeBusiness, 6, tag+":LocalBusiness")
			}
		}
	}
}

// pointsElsewhere reports whether an item's url is another page of the site.
//
// It is "elsewhere" when an item card on this page links to it: the page
// lists the item rather than being its page. (Comparing with the page URL
// is not enough: sites change slugs, so a product's own url often differs.)
func (p *Page) pointsElsewhere(m map[string]interface{}) bool {
	ref := resolveAny(p.pg.base, schemaString(m["url"]))
	if ref == "" {
		return false
	}
	if sameDocument(p.pg.base, ref) {
		return false
	}
	target, err := url.Parse(ref)
	if err != nil || p.pg.base == nil {
		return false
	}
	l := p.lay()
	if l.cardLinks[linkKey(ref)] || l.cardLinks[strings.ToLower(strings.TrimSuffix(target.Path, "/"))] {
		return true
	}
	// Without cards (one or two results), an item URL sharing no path segment
	// with the page is another page; a product's own URL keeps its id even
	// when the slug changes ("/v/slug/galPXm5K" vs "/v/slug-/galPXm5K").
	if !strings.EqualFold(strings.TrimPrefix(target.Host, "www."), strings.TrimPrefix(p.pg.base.Host, "www.")) {
		return false
	}
	own := pathSegments(p.pg.base.Path)
	theirs := pathSegments(target.Path)
	if len(own) == 0 || len(theirs) == 0 {
		return false
	}
	for seg := range theirs {
		if own[seg] {
			return false
		}
	}
	return true
}

// pathSegments returns a path's segments longer than two characters.
func pathSegments(path string) map[string]bool {
	out := map[string]bool{}
	for _, seg := range strings.Split(strings.ToLower(path), "/") {
		if len([]rune(seg)) > 2 {
			out[seg] = true
		}
	}
	return out
}

// allItems collects the objects matching pred anywhere, lists included,
// without descending into matches.
func allItems(items []interface{}, pred func(map[string]interface{}) bool) []map[string]interface{} {
	var out []map[string]interface{}
	var walk func(v interface{}, depth int)
	walk = func(v interface{}, depth int) {
		if depth > 2*maxItemDepth {
			return
		}
		switch t := v.(type) {
		case map[string]interface{}:
			if pred(t) {
				out = append(out, t)
				return
			}
			for _, c := range t {
				walk(c, depth+1)
			}
		case []interface{}:
			for _, c := range t {
				walk(c, depth+1)
			}
		}
	}
	for _, it := range items {
		walk(it, 0)
	}
	return out
}

// typesPresent lists every @type in the items, lower-cased.
func typesPresent(items []interface{}) map[string]bool {
	out := map[string]bool{}
	var walk func(v interface{}, depth int)
	walk = func(v interface{}, depth int) {
		if depth > 2*maxItemDepth {
			return
		}
		switch t := v.(type) {
		case map[string]interface{}:
			for _, typ := range schemaList(t["@type"]) {
				if s, ok := typ.(string); ok {
					out[strings.ToLower(schemaEnum(s))] = true
				}
			}
			for k, c := range t {
				if k != "@type" {
					walk(c, depth+1)
				}
			}
		case []interface{}:
			for _, c := range t {
				walk(c, depth+1)
			}
		}
	}
	for _, it := range items {
		walk(it, 0)
	}
	return out
}

// ── meta tags ────────────────────────────────────────────────────────────

func (p *Page) metaSignals(s *scorer) {
	og := strings.ToLower(strings.TrimSpace(p.pg.metaFirst("og:type")))
	switch {
	case strings.Contains(og, "product"):
		// Some sites (Divar) put og:type=product on every page.
		s.add(models.PageTypeProduct, 6, "og:type="+og)
	case og == "article":
		s.add(models.PageTypeArticle, 5, "og:type=article")
	case og == "profile":
		s.add(models.PageTypeOther, 4, "og:type=profile")
	}
	if p.pg.metaFirst("product:price:amount", "og:price:amount") != "" {
		s.add(models.PageTypeProduct, 4, "meta:product:price")
	}
	if p.pg.metaFirst("article:published_time") != "" && !strings.Contains(og, "product") {
		s.add(models.PageTypeArticle, 3, "meta:article:published_time")
	}
}

// ── platform template marks ──────────────────────────────────────────────

// pageKind is one platform mark: a body class, body id or script value and
// the page type it means.
type pageKind struct {
	mark     string
	pageType string
	points   int
}

var (
	// WordPress and WooCommerce body classes; only trusted on WordPress sites
	// since names like "home" and "archive" are common elsewhere.
	wordpressClasses = []pageKind{
		{"single-product", models.PageTypeProduct, 14},
		{"tax-product_cat", models.PageTypeListing, 12}, {"tax-product_tag", models.PageTypeListing, 12},
		{"tax-product_brand", models.PageTypeListing, 12}, {"post-type-archive-product", models.PageTypeListing, 12},
		{"woocommerce-shop", models.PageTypeListing, 12},
		{"single-job_listing", models.PageTypeJobPosting, 12}, {"post-type-archive-job_listing", models.PageTypeListing, 12},
		{"single-tribe_events", models.PageTypeEvent, 12}, {"post-type-archive-tribe_events", models.PageTypeListing, 12},
		{"single-post", models.PageTypeArticle, 12},
		{"home", models.PageTypeHome, 16},
		{"blog", models.PageTypeListing, 10}, {"archive", models.PageTypeListing, 10}, {"category", models.PageTypeListing, 10},
		{"tag", models.PageTypeListing, 10}, {"search", models.PageTypeListing, 10}, {"search-results", models.PageTypeListing, 10},
		{"author", models.PageTypeListing, 8}, {"date", models.PageTypeListing, 8},
		{"page", models.PageTypeOther, 6},
	}
	// Body classes unique enough to trust anywhere.
	platformClasses = []pageKind{
		{"catalog-product-view", models.PageTypeProduct, 14}, // Magento
		{"catalog-category-view", models.PageTypeListing, 12},
		{"catalogsearch-result-index", models.PageTypeListing, 12},
		{"cms-index-index", models.PageTypeHome, 16},
		{"template-product", models.PageTypeProduct, 14}, // Shopify
		{"template-collection", models.PageTypeListing, 12}, {"template-search", models.PageTypeListing, 12},
		{"template-list-collections", models.PageTypeListing, 12},
		{"template-article", models.PageTypeArticle, 12}, {"template-blog", models.PageTypeListing, 12},
		{"template-index", models.PageTypeHome, 16},
		{"page-product", models.PageTypeProduct, 12}, // PrestaShop 1.7+
		{"page-category", models.PageTypeListing, 12}, {"page-search", models.PageTypeListing, 12},
		{"page-manufacturer", models.PageTypeListing, 12}, {"page-prices-drop", models.PageTypeListing, 12},
		{"page-new-products", models.PageTypeListing, 12}, {"page-best-sales", models.PageTypeListing, 12},
		{"page-index", models.PageTypeHome, 16},
		{"common-home", models.PageTypeHome, 16},               // OpenCart
		{"page-node-type-article", models.PageTypeArticle, 12}, // Drupal
		{"node-type-article", models.PageTypeArticle, 12},
		{"page-node-type-product", models.PageTypeProduct, 12},
		{"path-frontpage", models.PageTypeHome, 16},
		{"page-taxonomy-term", models.PageTypeListing, 10},
	}
	// OpenCart puts the route and an id in the body class: "product-product-42".
	platformClassPrefixes = []pageKind{
		{"product-product", models.PageTypeProduct, 14}, {"product-category", models.PageTypeListing, 12},
		{"product-search", models.PageTypeListing, 12}, {"product-special", models.PageTypeListing, 12},
		{"product-manufacturer", models.PageTypeListing, 12},
	}
	// PrestaShop body ids and page_name values.
	prestashopPages = map[string]pageKind{
		"product": {"", models.PageTypeProduct, 14}, "category": {"", models.PageTypeListing, 12},
		"search": {"", models.PageTypeListing, 12}, "manufacturer": {"", models.PageTypeListing, 12},
		"supplier": {"", models.PageTypeListing, 12}, "prices-drop": {"", models.PageTypeListing, 12},
		"new-products": {"", models.PageTypeListing, 12}, "best-sales": {"", models.PageTypeListing, 12},
		"index": {"", models.PageTypeHome, 16}, "cms": {"", models.PageTypeOther, 6},
	}
	// Shopify's analytics pageType.
	shopifyPages = map[string]string{
		"product": models.PageTypeProduct, "collection": models.PageTypeListing, "searchresults": models.PageTypeListing,
		"article": models.PageTypeArticle, "blog": models.PageTypeListing, "home": models.PageTypeHome,
	}
	// Generic body class words used by custom themes.
	genericClassHints = []struct {
		substr   string
		pageType string
	}{
		{"product-page", models.PageTypeProduct}, {"product-detail", models.PageTypeProduct}, {"productpage", models.PageTypeProduct},
		{"product-single", models.PageTypeProduct}, {"single-product", models.PageTypeProduct},
		{"category-page", models.PageTypeListing}, {"product-list", models.PageTypeListing}, {"catalog-page", models.PageTypeListing},
		{"search-page", models.PageTypeListing}, {"listing-page", models.PageTypeListing},
		{"article-page", models.PageTypeArticle}, {"post-page", models.PageTypeArticle}, {"news-page", models.PageTypeArticle},
		{"single-post", models.PageTypeArticle}, {"blog-post", models.PageTypeArticle},
	}
)

func (p *Page) platformSignals(s *scorer) {
	l := p.lay()
	classes := setOf(l.bodyClasses...)
	if l.wordpress {
		for _, k := range wordpressClasses {
			if classes[k.mark] {
				s.add(k.pageType, k.points, "wordpress:body."+k.mark)
			}
		}
	}
	for _, k := range platformClasses {
		if classes[k.mark] {
			s.add(k.pageType, k.points, "body."+k.mark)
		}
	}
	for _, k := range platformClassPrefixes {
		for c := range classes {
			if strings.HasPrefix(c, k.mark+"-") || c == k.mark {
				s.add(k.pageType, k.points, "body."+k.mark)
			}
		}
	}
	if k, ok := prestashopPages[l.bodyID]; ok && (l.prestashop || classes["page-"+l.bodyID] || hasPrefixClass(classes, "product-id-")) {
		s.add(k.pageType, k.points, "prestashop:body#"+l.bodyID)
	}
	if name := l.scriptPage["prestashop"]; name != "" {
		if k, ok := prestashopPages[name]; ok {
			s.add(k.pageType, k.points, "prestashop:page_name="+name)
		}
	}
	if t, ok := shopifyPages[l.scriptPage["shopify"]]; ok {
		points := 14
		if t == models.PageTypeHome {
			points = 16
		}
		s.add(t, points, "shopify:pageType="+l.scriptPage["shopify"])
	}
	if route := l.scriptPage["next"]; route != "" {
		if t := routeType(route); t != "" {
			points := 6
			if t == models.PageTypeHome {
				points = 16
			}
			s.add(t, points, "next:route="+route)
		}
	}
	for _, h := range genericClassHints {
		for c := range classes {
			if strings.Contains(c, h.substr) && !(l.wordpress && c == "single-product") {
				s.add(h.pageType, 5, "body-class~"+h.substr)
				break
			}
		}
	}
}

func hasPrefixClass(classes map[string]bool, prefix string) bool {
	for c := range classes {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// routeType reads a framework route template such as "/product/[id]" or
// "/blog/[slug]".
func routeType(route string) string {
	r := strings.ToLower(route)
	if r == "/" || r == "/index" || r == "/[locale]" || r == "/[lang]" {
		return models.PageTypeHome
	}
	param := strings.Contains(r, "[")
	words := strings.FieldsFunc(r, func(c rune) bool { return c == '/' || c == '-' || c == '_' || c == '[' || c == ']' || c == '.' })
	for _, w := range words {
		switch {
		case w == "product" || w == "products" || w == "item" || w == "p" || w == "pdp":
			if param {
				return models.PageTypeProduct
			}
		case w == "category" || w == "categories" || w == "collection" || w == "collections" || w == "search" || w == "list" || w == "brand" || w == "tag" || w == "plp" || w == "archive":
			return models.PageTypeListing
		case w == "blog" || w == "news" || w == "article" || w == "articles" || w == "post" || w == "posts" || w == "mag":
			if param {
				return models.PageTypeArticle
			}
			return models.PageTypeListing
		case w == "job" || w == "jobs":
			if param {
				return models.PageTypeJobPosting
			}
			return models.PageTypeListing
		case w == "event" || w == "events":
			if param {
				return models.PageTypeEvent
			}
			return models.PageTypeListing
		}
	}
	return ""
}

// ── layout ───────────────────────────────────────────────────────────────

func (p *Page) layoutSignals(s *scorer) {
	l := p.lay()
	if l.buyButtons > 0 {
		s.add(models.PageTypeProduct, 7, "layout:buy_button")
	}
	if l.contactSeller {
		s.add(models.PageTypeProduct, 5, "layout:contact_seller")
	}
	if l.notifyStock {
		s.add(models.PageTypeProduct, 6, "layout:notify_when_available")
	}
	if l.quantityInput {
		s.add(models.PageTypeProduct, 2, "layout:quantity")
	}
	if n := len(l.prices); n > 0 && n <= 6 && l.productCards < 30 {
		s.add(models.PageTypeProduct, 2, "layout:price")
	}
	switch c := l.productCards; {
	case c >= 16:
		s.add(models.PageTypeListing, 8, fmt.Sprintf("layout:product_cards×%d", c))
	case c >= 8:
		s.add(models.PageTypeListing, 6, fmt.Sprintf("layout:product_cards×%d", c))
	case c >= minCardGroup && l.buyButtons == 0:
		s.add(models.PageTypeListing, 3, fmt.Sprintf("layout:product_cards×%d", c))
	}
	switch c := l.postCards; {
	case c >= 12:
		s.add(models.PageTypeListing, 7, fmt.Sprintf("layout:post_cards×%d", c))
	case c >= 6:
		s.add(models.PageTypeListing, 5, fmt.Sprintf("layout:post_cards×%d", c))
	}
	if l.pagination {
		s.add(models.PageTypeListing, 3, "layout:pagination")
	}
	if l.noResults {
		s.add(models.PageTypeListing, 5, "layout:no_results")
	}
	if l.cardButtons >= 4 && l.buyButtons == 0 {
		s.add(models.PageTypeListing, 2, "layout:card_buttons")
	}
	// Long text: product descriptions can be long too, so less on shop pages.
	words := l.paragraphWords
	div := 1
	if l.buyButtons > 0 {
		div = 2
	}
	switch {
	case words >= 600:
		s.add(models.PageTypeArticle, 6/div, fmt.Sprintf("layout:text×%dw", words))
	case words >= 300:
		s.add(models.PageTypeArticle, 4/div, fmt.Sprintf("layout:text×%dw", words))
	}
	if l.articleWords >= 150 {
		s.add(models.PageTypeArticle, 2, "layout:<article>")
	}
	if l.hasTime && words >= 150 {
		s.add(models.PageTypeArticle, 2, "layout:date")
	}
	if l.hasAuthor && words >= 150 {
		s.add(models.PageTypeArticle, 1, "layout:author")
	}
}
