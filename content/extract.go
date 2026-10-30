package content

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"github.com/antchfx/htmlquery"
	"github.com/antchfx/xpath"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
	"golang.org/x/net/html"
)

// Field types supported by structured extraction.
var fieldTypes = map[string]bool{
	"": true, "text": true, "html": true, "attr": true, "url": true,
	"number": true, "price": true, "date": true, "exists": true, "list": true,
}

// FieldTypes lists the supported extraction field types (for UIs and docs).
var FieldTypes = []string{"text", "html", "attr", "url", "number", "price", "date", "exists", "list"}

const (
	xpathPrefix  = "xpath:"
	jsonLDPrefix = "jsonld:"
)

// SelectorPrefixes lists the selector syntaxes besides CSS (for UIs and docs).
var SelectorPrefixes = []string{xpathPrefix, jsonLDPrefix}

// ValidateFields checks an extraction schema before it is saved.
func ValidateFields(fields []models.ExtractField) error {
	return validateFields(fields, "", 0)
}

func validateFields(fields []models.ExtractField, path string, depth int) error {
	if depth > 4 {
		return fmt.Errorf("extraction schema nests too deeply")
	}
	if len(fields) > 100 {
		return fmt.Errorf("too many extraction fields")
	}
	seen := map[string]bool{}
	for _, f := range fields {
		name := strings.TrimSpace(f.Name)
		where := path + name
		if name == "" {
			return fmt.Errorf("extraction field %q: name is required", where)
		}
		if seen[name] {
			return fmt.Errorf("extraction field %q is defined twice", where)
		}
		seen[name] = true
		if !fieldTypes[f.Type] {
			return fmt.Errorf("extraction field %q: unknown type %q", where, f.Type)
		}
		if depth == 0 && strings.TrimSpace(f.Selector) == "" {
			return fmt.Errorf("extraction field %q: selector is required", where)
		}
		if f.Type == "attr" && f.Attr == "" {
			return fmt.Errorf("extraction field %q: attr is required for type attr", where)
		}
		if f.Selector != "" {
			if err := validateSelector(f, f.Selector); err != nil {
				return fmt.Errorf("extraction field %q: %v", where, err)
			}
		}
		if len(f.FallbackSelectors) > 5 {
			return fmt.Errorf("extraction field %q: at most 5 fallback selectors are allowed", where)
		}
		for _, sel := range f.FallbackSelectors {
			if strings.TrimSpace(sel) == "" {
				return fmt.Errorf("extraction field %q: fallback selectors must not be empty", where)
			}
			if err := validateSelector(f, sel); err != nil {
				return fmt.Errorf("extraction field %q: fallback %q: %v", where, sel, err)
			}
		}
		if f.Regex != "" {
			if _, err := regexp.Compile(f.Regex); err != nil {
				return fmt.Errorf("extraction field %q: invalid regex: %v", where, err)
			}
		}
		if f.Type == "list" {
			if len(f.Fields) == 0 {
				return fmt.Errorf("extraction field %q: type list needs sub-fields", where)
			}
			if err := validateFields(f.Fields, where+".", depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateSelector checks one CSS, "xpath:" or "jsonld:" selector of field f.
func validateSelector(f models.ExtractField, sel string) error {
	switch {
	case strings.HasPrefix(sel, xpathPrefix):
		if _, err := xpath.Compile(strings.TrimPrefix(sel, xpathPrefix)); err != nil {
			return fmt.Errorf("invalid XPath: %v", err)
		}
	case strings.HasPrefix(sel, jsonLDPrefix):
		if err := validateJSONLDPath(strings.TrimPrefix(sel, jsonLDPrefix)); err != nil {
			return err
		}
		if f.Type == "html" || f.Type == "attr" || f.Type == "list" {
			return fmt.Errorf("type %s cannot be used with a jsonld: selector", f.Type)
		}
	default:
		if err := compileCSS(sel); err != nil {
			return fmt.Errorf("invalid CSS selector: %v", err)
		}
	}
	return nil
}

// compileCSS reports whether sel is a valid CSS selector (goquery silently
// matches nothing for invalid selectors).
func compileCSS(sel string) error {
	_, err := cascadia.Compile(sel)
	return err
}

// Extract applies a schema to an HTML page and returns field name → value.
func Extract(pageURL string, body []byte, fields []models.ExtractField) (map[string]interface{}, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(pageURL)
	return extractFields(root, base, fields), nil
}

func extractFields(ctx *html.Node, base *url.URL, fields []models.ExtractField) map[string]interface{} {
	out := make(map[string]interface{}, len(fields))
	for _, f := range fields {
		out[f.Name] = extractField(ctx, base, f)
	}
	return out
}

// extractField tries the field's selector, then its fallback selectors in
// order, and returns the first value that is not empty.
func extractField(ctx *html.Node, base *url.URL, f models.ExtractField) interface{} {
	v := extractWith(ctx, base, f, f.Selector)
	for _, sel := range f.FallbackSelectors {
		if !emptyValue(v) {
			break
		}
		v = extractWith(ctx, base, f, sel)
	}
	return v
}

// emptyValue reports whether a field produced nothing: null, an empty list
// or, for type exists, false.
func emptyValue(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case []interface{}:
		return len(t) == 0
	}
	return false
}

func extractWith(ctx *html.Node, base *url.URL, f models.ExtractField, selector string) interface{} {
	if sel := strings.TrimSpace(selector); strings.HasPrefix(sel, jsonLDPrefix) {
		return extractJSONLDField(ctx, base, f, strings.TrimPrefix(sel, jsonLDPrefix))
	}
	nodes := selectNodes(ctx, selector)
	if f.Type == "exists" {
		return len(nodes) > 0
	}
	if f.Type == "list" {
		items := make([]interface{}, 0, len(nodes))
		for _, n := range nodes {
			items = append(items, extractFields(n, base, f.Fields))
		}
		return items
	}
	if f.Multiple {
		vals := make([]interface{}, 0, len(nodes))
		for _, n := range nodes {
			if v := nodeValue(n, base, f); v != nil {
				vals = append(vals, v)
			}
		}
		return vals
	}
	for _, n := range nodes {
		if v := nodeValue(n, base, f); v != nil {
			return v
		}
	}
	return nil
}

// selectNodes evaluates a CSS or "xpath:" selector relative to ctx. An empty
// selector selects ctx itself (useful for list sub-fields).
func selectNodes(ctx *html.Node, selector string) []*html.Node {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return []*html.Node{ctx}
	}
	if strings.HasPrefix(selector, xpathPrefix) {
		nodes, err := htmlquery.QueryAll(ctx, strings.TrimPrefix(selector, xpathPrefix))
		if err != nil {
			return nil
		}
		return nodes
	}
	var nodes []*html.Node
	func() {
		defer func() { _ = recover() }()
		nodes = goquery.NewDocumentFromNode(ctx).Find(selector).Nodes
	}()
	return nodes
}

func nodeValue(n *html.Node, base *url.URL, f models.ExtractField) interface{} {
	raw, ok := rawValue(n, f)
	if !ok {
		return nil
	}
	return convertValue(raw, base, f)
}

// convertValue applies a field's regex and type to the raw string of a match.
func convertValue(raw string, base *url.URL, f models.ExtractField) interface{} {
	if f.Regex != "" {
		re, err := regexp.Compile(f.Regex)
		if err != nil {
			return nil
		}
		m := re.FindStringSubmatch(raw)
		if m == nil {
			return nil
		}
		if len(m) > 1 {
			raw = m[1]
		} else {
			raw = m[0]
		}
	}

	switch f.Type {
	case "", "text":
		if raw == "" {
			return nil
		}
		return raw
	case "html", "attr":
		return raw
	case "url":
		if abs := resolveAny(base, raw); abs != "" {
			return abs
		}
		return nil
	case "number":
		if v, ok := modules.ParseNumber(raw); ok {
			return v
		}
		return nil
	case "price":
		if p, ok := modules.ParsePrice(raw); ok {
			return p
		}
		return nil
	case "date":
		if t, ok := modules.ParseDateAny(raw); ok {
			return t.Format("2006-01-02T15:04:05Z07:00")
		}
		return nil
	}
	return nil
}

// rawValue reads the string a field type is computed from.
func rawValue(n *html.Node, f models.ExtractField) (string, bool) {
	if isXPathAttr(n) { // XPath like //a/@href
		return strings.TrimSpace(n.FirstChild.Data), true
	}
	if n.Type == html.TextNode { // XPath like //p/text()
		return strings.TrimSpace(n.Data), true
	}
	switch f.Type {
	case "html":
		h, err := goquery.OuterHtml(goquery.NewDocumentFromNode(n).Selection)
		return h, err == nil
	case "attr":
		return attrOf(n, f.Attr)
	case "url":
		if f.Attr != "" {
			return attrOf(n, f.Attr)
		}
		if v, ok := attrOf(n, "href"); ok {
			return v, true
		}
		return attrOf(n, "src")
	case "date":
		if v := firstAttr(n, "datetime", "content"); v != "" {
			return v, true
		}
		return nodeText(n), true
	}
	if n.Data == "meta" {
		return firstAttr(n, "content"), true
	}
	return nodeText(n), true
}

// isXPathAttr reports whether n is htmlquery's detached stand-in for an
// attribute: an element with no parent whose only child is the value text.
func isXPathAttr(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Parent == nil && len(n.Attr) == 0 &&
		n.FirstChild != nil && n.FirstChild == n.LastChild && n.FirstChild.Type == html.TextNode
}

func nodeText(n *html.Node) string {
	return strings.Join(strings.Fields(goquery.NewDocumentFromNode(n).Text()), " ")
}

func attrOf(n *html.Node, name string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val, true
		}
	}
	return "", false
}

func firstAttr(n *html.Node, names ...string) string {
	for _, name := range names {
		if v, ok := attrOf(n, name); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func resolveAny(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	return u.String()
}
