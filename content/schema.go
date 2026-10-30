package content

import (
	"net/url"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/resolver/crawler/modules"
	"golang.org/x/net/html"
)

// Shared reading of the structured data pages publish for search engines and
// social networks: schema.org items (JSON-LD, microdata, RDFa) and meta tags.
// The detectors (product, article, business, job posting, event, FAQ) build on it.

// Sources of detected values, from the most to the least reliable.
const (
	SourceJSONLD    = "json_ld"
	SourceMicrodata = "microdata"
	SourceRDFa      = "rdfa"
	SourceOpenGraph = "opengraph"
	SourceMeta      = "meta" // other meta tags (author, dates, Facebook business tags)
	SourceHTML      = "html" // page markup (h1, <time>, lang)
)

const maxItemDepth = 8

// pageData is a page parsed once for all detectors.
type pageData struct {
	root      *html.Node
	doc       *goquery.Document
	base      *url.URL
	jsonLD    []interface{}
	microdata []interface{}
	rdfa      []interface{}
	meta      map[string][]string // lower-cased property/name → contents, in page order
}

func newPageData(root *html.Node, base *url.URL) *pageData {
	pg := &pageData{
		root:      root,
		doc:       goquery.NewDocumentFromNode(root),
		base:      base,
		jsonLD:    jsonLDBlocks(root),
		microdata: structuredItems(root, base, microdataSyntax),
		rdfa:      structuredItems(root, base, rdfaSyntax),
		meta:      map[string][]string{},
	}
	pg.doc.Find("meta").Each(func(_ int, s *goquery.Selection) {
		key := strings.ToLower(strings.TrimSpace(s.AttrOr("property", s.AttrOr("name", ""))))
		val := strings.TrimSpace(s.AttrOr("content", ""))
		if key != "" && val != "" {
			pg.meta[key] = append(pg.meta[key], val)
		}
	})
	return pg
}

// schemaSource is one syntax's top-level schema.org items.
type schemaSource struct {
	name  string
	items []interface{}
}

func (pg *pageData) schemaSources() []schemaSource {
	return []schemaSource{{SourceJSONLD, pg.jsonLD}, {SourceMicrodata, pg.microdata}, {SourceRDFa, pg.rdfa}}
}

// metaFirst returns the first non-empty value among the meta keys.
func (pg *pageData) metaFirst(keys ...string) string {
	for _, k := range keys {
		if v := pg.meta[k]; len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// listTypes hold other things (related products, category listings, search
// results) whose items are not what the page is about.
var listTypes = []string{"ItemList", "OfferCatalog", "CollectionPage", "SearchResultsPage"}

// findItems collects the objects matching pred. It does not descend into
// matches (their nested objects belong to them), list types, or skipKeys.
func findItems(items []interface{}, pred func(map[string]interface{}) bool, skipKeys map[string]bool) []map[string]interface{} {
	var out []map[string]interface{}
	var walk func(v interface{}, depth int)
	walk = func(v interface{}, depth int) {
		if depth > 2*maxItemDepth {
			return
		}
		switch t := v.(type) {
		case map[string]interface{}:
			if hasAnyType(t["@type"], listTypes) {
				return
			}
			if pred(t) {
				out = append(out, t)
				return
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				if !skipKeys[k] {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k], depth+1)
			}
		case []interface{}:
			for _, item := range t {
				walk(item, depth+1)
			}
		}
	}
	for _, item := range items {
		walk(item, 0)
	}
	return out
}

// ofTypes matches objects whose @type is one of types.
func ofTypes(types []string) func(map[string]interface{}) bool {
	return func(m map[string]interface{}) bool { return hasAnyType(m["@type"], types) }
}

func hasAnyType(v interface{}, types []string) bool {
	for _, t := range types {
		if hasJSONLDType(v, t) {
			return true
		}
	}
	return false
}

// mainItem picks the item a page is about: the one whose url, @id or
// mainEntityOfPage is the page, else the only one (items sharing one name
// count as one). Several unrelated items mean a listing: nil.
func mainItem(cands []map[string]interface{}, base *url.URL) map[string]interface{} {
	switch len(cands) {
	case 0:
		return nil
	case 1:
		return cands[0]
	}
	for _, c := range cands {
		for _, ref := range []string{schemaString(c["url"]), schemaString(c["@id"]), schemaRef(c["mainEntityOfPage"])} {
			if sameDocument(base, resolveAny(base, ref)) {
				return c
			}
		}
	}
	names := map[string]bool{}
	for _, c := range cands {
		names[strings.ToLower(schemaString(c["name"])+"\x00"+schemaString(c["headline"]))] = true
	}
	if len(names) == 1 {
		return cands[0]
	}
	return nil
}

// sameDocument compares two URLs ignoring the scheme, a "www." prefix, a
// trailing slash and the fragment.
func sameDocument(base *url.URL, other string) bool {
	if base == nil || other == "" {
		return false
	}
	o, err := url.Parse(other)
	if err != nil {
		return false
	}
	key := func(u *url.URL) string {
		host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
		path := strings.TrimSuffix(u.EscapedPath(), "/")
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
		return host + path
	}
	return key(base) == key(o)
}

// idIndex maps JSON-LD @id values to their objects, to follow references
// such as {"@id": "https://site.ir/#faq-question-1"}.
type idIndex map[string]map[string]interface{}

func indexIDs(items []interface{}) idIndex {
	idx := idIndex{}
	var walk func(v interface{}, depth int)
	walk = func(v interface{}, depth int) {
		if depth > 2*maxItemDepth {
			return
		}
		switch t := v.(type) {
		case map[string]interface{}:
			// A bare reference has only @id; index the object that has data.
			if id := schemaString(t["@id"]); id != "" && len(t) > 1 {
				if _, ok := idx[id]; !ok {
					idx[id] = t
				}
			}
			for _, child := range t {
				walk(child, depth+1)
			}
		case []interface{}:
			for _, child := range t {
				walk(child, depth+1)
			}
		}
	}
	for _, item := range items {
		walk(item, 0)
	}
	return idx
}

// resolve replaces a bare {"@id": …} reference with the object it names.
func (idx idIndex) resolve(v interface{}) interface{} {
	if m, ok := v.(map[string]interface{}); ok && len(m) == 1 {
		if target, ok := idx[schemaString(m["@id"])]; ok {
			return target
		}
	}
	return v
}

// schemaList returns v as a list: a JSON array, or a one-item list.
func schemaList(v interface{}) []interface{} {
	switch t := v.(type) {
	case nil:
		return nil
	case []interface{}:
		return t
	}
	return []interface{}{v}
}

// schemaString returns the first scalar of v as text.
func schemaString(v interface{}) string {
	for _, item := range schemaList(v) {
		if m, ok := item.(map[string]interface{}); ok {
			item = m["@value"]
		}
		if s, ok := jsonLDScalar(item); ok {
			return s
		}
	}
	return ""
}

// schemaStrings returns every scalar of v; a single string with commas
// (keywords) is split.
func schemaStrings(v interface{}, splitCommas bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = cleanInline(s)
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}
	for _, item := range schemaList(v) {
		s := schemaString(item)
		if splitCommas && strings.ContainsAny(s, ",،") {
			for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '،' }) {
				add(part)
			}
			continue
		}
		add(s)
	}
	return out
}

// schemaName reads a Thing's name (brand, author, organization), or plain text.
func schemaName(v interface{}) string {
	for _, item := range schemaList(v) {
		if m, ok := item.(map[string]interface{}); ok {
			if s := schemaString(m["name"]); s != "" {
				return s
			}
			continue
		}
		if s := schemaString(item); s != "" {
			return s
		}
	}
	return ""
}

// schemaRef reads a URL given as text or as {"@id": …} / {"url": …}.
func schemaRef(v interface{}) string {
	for _, item := range schemaList(v) {
		if m, ok := item.(map[string]interface{}); ok {
			if s := schemaString(m["@id"]); s != "" {
				return s
			}
			if s := schemaString(m["url"]); s != "" {
				return s
			}
			continue
		}
		if s := schemaString(item); s != "" {
			return s
		}
	}
	return ""
}

func schemaNumber(v interface{}) float64 {
	if n, ok := modules.ParseNumber(schemaString(v)); ok {
		return n
	}
	return 0
}

// schemaDate normalises a date to RFC3339; unparseable dates are dropped.
func schemaDate(v string) string {
	if t, ok := modules.ParseDateAny(v); ok {
		return t.Format("2006-01-02T15:04:05Z07:00")
	}
	return ""
}

// schemaEnum turns "https://schema.org/EventCancelled" into "EventCancelled".
func schemaEnum(v string) string {
	if i := strings.LastIndexAny(v, "/#:"); i >= 0 {
		v = v[i+1:]
	}
	return strings.TrimSpace(v)
}

// schemaPrice parses a price value with its currency code. Zero and negative
// prices are treated as missing: shops use 0 for "call for price".
func schemaPrice(v interface{}, currency string) *modules.Price {
	raw := schemaString(v)
	if raw == "" {
		return nil
	}
	currency = strings.TrimSpace(currency)
	text := raw
	if currency != "" {
		text += " " + currency
	}
	pr, ok := modules.ParsePrice(text)
	if !ok || pr.Amount <= 0 {
		return nil
	}
	if pr.Currency == "" && len(currency) == 3 && isLetters(currency) {
		pr.Currency = strings.ToUpper(currency)
	}
	return &pr
}

func isLetters(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// schemaImages reads image URLs from a URL, an ImageObject or a list of them.
func schemaImages(v interface{}, base *url.URL, max int) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range schemaList(v) {
		var ref string
		if m, ok := item.(map[string]interface{}); ok {
			if ref = schemaString(m["url"]); ref == "" {
				ref = schemaString(m["contentUrl"])
			}
		} else {
			ref = schemaString(item)
		}
		if abs := resolveAny(base, ref); abs != "" && !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
			if len(out) == max {
				break
			}
		}
	}
	return out
}

// PostalAddress is a schema.org address; Text joins its parts for display.
type PostalAddress struct {
	Street     string `json:"street,omitempty"`
	Locality   string `json:"locality,omitempty"` // city
	Region     string `json:"region,omitempty"`   // province
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"`
	Text       string `json:"text,omitempty"`
}

// schemaAddress reads a PostalAddress object, or an address given as text.
func schemaAddress(v interface{}) *PostalAddress {
	for _, item := range schemaList(v) {
		m, ok := item.(map[string]interface{})
		if !ok {
			if s := cleanInline(schemaString(item)); s != "" {
				return &PostalAddress{Text: s}
			}
			continue
		}
		a := &PostalAddress{
			Street:     cleanInline(schemaString(m["streetAddress"])),
			Locality:   cleanInline(schemaString(m["addressLocality"])),
			Region:     cleanInline(schemaString(m["addressRegion"])),
			PostalCode: modules.ToLatinDigits(schemaString(m["postalCode"])),
			Country:    cleanInline(schemaName(m["addressCountry"])),
		}
		a.Text = joinNonEmpty(", ", a.Street, a.Locality, a.Region, a.Country)
		if a.PostalCode != "" && a.Text != "" {
			a.Text += " (" + a.PostalCode + ")"
		}
		if a.Text != "" {
			return a
		}
	}
	return nil
}

func joinNonEmpty(sep string, parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, sep)
}

// cleanInline turns text that may contain HTML (descriptions often do) into
// one line of plain text.
func cleanInline(s string) string {
	if strings.ContainsAny(s, "<&") {
		if doc, err := goquery.NewDocumentFromReader(strings.NewReader(s)); err == nil {
			s = doc.Text()
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n])) + "…"
}

// mergeInto copies each field of src that is empty in dst and records source
// under the field's JSON name. dst and src are pointers to the same struct
// type; its Sources field is skipped.
func mergeInto(dst, src interface{}, source string, sources map[string]string) {
	dv, sv := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem()
	t := dv.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if f.Name == "Sources" || name == "" || name == "-" {
			continue
		}
		if dv.Field(i).IsZero() && !sv.Field(i).IsZero() {
			dv.Field(i).Set(sv.Field(i))
			sources[name] = source
		}
	}
}

// ── microdata and RDFa ──────────────────────────────────────────────────

// itemSyntax names the attributes of an HTML structured-data syntax.
type itemSyntax struct {
	isScope func(*html.Node) bool
	types   func(*html.Node) []interface{}
	props   func(*html.Node) []string
}

var microdataSyntax = itemSyntax{
	isScope: func(n *html.Node) bool { _, ok := attrOf(n, "itemscope"); return ok },
	types:   func(n *html.Node) []interface{} { return fieldsOf(n, "itemtype") },
	props: func(n *html.Node) []string {
		v, _ := attrOf(n, "itemprop")
		return propNames(v)
	},
}

var rdfaSyntax = itemSyntax{
	isScope: func(n *html.Node) bool { _, ok := attrOf(n, "typeof"); return ok },
	types:   func(n *html.Node) []interface{} { return fieldsOf(n, "typeof") },
	props: func(n *html.Node) []string {
		v, _ := attrOf(n, "property")
		return propNames(v)
	},
}

func fieldsOf(n *html.Node, attr string) []interface{} {
	v, _ := attrOf(n, attr)
	var out []interface{}
	for _, f := range strings.Fields(v) {
		out = append(out, f)
	}
	return out
}

// propNames splits a property list and drops vocabulary prefixes
// ("schema:price", "https://schema.org/price" → "price").
func propNames(v string) []string {
	var out []string
	for _, f := range strings.Fields(v) {
		if i := strings.LastIndexAny(f, "/#:"); i >= 0 {
			f = f[i+1:]
		}
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// structuredItems converts the top-level microdata or RDFa items of a page
// into JSON-LD-shaped maps, so one reader handles all three syntaxes.
func structuredItems(root *html.Node, base *url.URL, sx itemSyntax) []interface{} {
	var out []interface{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && sx.isScope(n) && len(sx.props(n)) == 0 {
			out = append(out, structuredItem(n, base, sx, 0))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

func structuredItem(scope *html.Node, base *url.URL, sx itemSyntax, depth int) map[string]interface{} {
	m := map[string]interface{}{}
	if types := sx.types(scope); len(types) > 0 {
		m["@type"] = types
	}
	addProp := func(name string, v interface{}) {
		switch cur := m[name].(type) {
		case nil:
			m[name] = v
		case []interface{}:
			m[name] = append(cur, v)
		default:
			m[name] = []interface{}{cur, v}
		}
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			names := sx.props(c)
			nested := sx.isScope(c)
			if len(names) > 0 {
				var v interface{}
				if nested && depth < maxItemDepth {
					v = structuredItem(c, base, sx, depth+1)
				} else {
					v = itemValue(c, base)
				}
				for _, name := range names {
					addProp(name, v)
				}
			}
			if !nested {
				walk(c)
			}
		}
	}
	walk(scope)
	return m
}

// itemValue reads a property's value following the microdata rules (which
// RDFa's content/href/src attributes also fit).
func itemValue(n *html.Node, base *url.URL) string {
	if v, ok := attrOf(n, "content"); ok {
		return strings.TrimSpace(v)
	}
	switch n.Data {
	case "a", "link", "area":
		return resolveAny(base, firstAttr(n, "href"))
	case "img", "audio", "video", "source", "iframe", "embed", "track":
		return resolveAny(base, firstAttr(n, "src"))
	case "object":
		return resolveAny(base, firstAttr(n, "data"))
	case "data", "meter":
		return firstAttr(n, "value")
	case "time":
		if v := firstAttr(n, "datetime"); v != "" {
			return v
		}
	}
	if v := firstAttr(n, "resource", "href", "src"); v != "" {
		return resolveAny(base, v)
	}
	return nodeText(n)
}
