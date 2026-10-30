package content

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/resolver/crawler/models"
	"golang.org/x/net/html"
)

// A "jsonld:" selector reads schema.org data from <script type="application/ld+json">
// blocks: "jsonld:Product.offers.price" finds every object whose @type is
// Product (at any depth, including inside @graph) and follows the keys after
// it. Arrays along the path are expanded. The type "*" matches every object.

func validateJSONLDPath(p string) error {
	parts := strings.Split(strings.TrimSpace(p), ".")
	if len(parts) < 2 {
		return fmt.Errorf("jsonld selector needs a type and a key, e.g. jsonld:Product.name")
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return fmt.Errorf("jsonld selector %q has an empty segment", p)
		}
	}
	return nil
}

// jsonLDLeaf is a scalar found at the end of a path, with the object holding it.
type jsonLDLeaf struct {
	raw    string
	parent map[string]interface{}
}

func extractJSONLDField(ctx *html.Node, base *url.URL, f models.ExtractField, path string) interface{} {
	leaves := jsonLDValues(ctx, path)
	if f.Type == "exists" {
		return len(leaves) > 0
	}
	convert := func(l jsonLDLeaf) interface{} {
		raw := l.raw
		// Offers carry the currency next to the price.
		if f.Type == "price" {
			if cur, ok := l.parent["priceCurrency"].(string); ok && strings.TrimSpace(cur) != "" {
				raw += " " + strings.TrimSpace(cur)
			}
		}
		return convertValue(raw, base, f)
	}
	if f.Multiple {
		vals := make([]interface{}, 0, len(leaves))
		for _, l := range leaves {
			if v := convert(l); v != nil {
				vals = append(vals, v)
			}
		}
		return vals
	}
	for _, l := range leaves {
		if v := convert(l); v != nil {
			return v
		}
	}
	return nil
}

// jsonLDValues evaluates a jsonld path against the JSON-LD blocks inside ctx.
func jsonLDValues(ctx *html.Node, path string) []jsonLDLeaf {
	parts := strings.Split(strings.TrimSpace(path), ".")
	typ, keys := strings.TrimSpace(parts[0]), parts[1:]
	var objects []map[string]interface{}
	for _, block := range jsonLDBlocks(ctx) {
		collectTyped(block, typ, &objects)
	}
	var out []jsonLDLeaf
	for _, obj := range objects {
		follow(obj, keys, obj, &out)
	}
	return out
}

// jsonLDBlocks parses every JSON-LD script under n; invalid blocks are skipped.
func jsonLDBlocks(n *html.Node) []interface{} {
	var out []interface{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			if t, _ := attrOf(n, "type"); isJSONLDType(t) {
				var sb strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						sb.WriteString(c.Data)
					}
				}
				var v interface{}
				if raw := strings.TrimSpace(sb.String()); raw != "" && len(raw) <= 1<<20 && json.Unmarshal([]byte(raw), &v) == nil {
					out = append(out, v)
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func isJSONLDType(t string) bool {
	t = strings.ToLower(t)
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t) == "application/ld+json"
}

// collectTyped appends every object of type typ in v, parents before children.
// Keys of an object are visited in sorted order so results are deterministic.
func collectTyped(v interface{}, typ string, out *[]map[string]interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		if typ == "*" || hasJSONLDType(t["@type"], typ) {
			*out = append(*out, t)
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			collectTyped(t[k], typ, out)
		}
	case []interface{}:
		for _, item := range t {
			collectTyped(item, typ, out)
		}
	}
}

// hasJSONLDType compares @type values case-insensitively, ignoring a
// vocabulary prefix such as "https://schema.org/".
func hasJSONLDType(v interface{}, typ string) bool {
	match := func(s string) bool {
		if i := strings.LastIndexAny(s, "/#:"); i >= 0 {
			s = s[i+1:]
		}
		return strings.EqualFold(strings.TrimSpace(s), typ)
	}
	switch t := v.(type) {
	case string:
		return match(t)
	case []interface{}:
		for _, item := range t {
			if s, ok := item.(string); ok && match(s) {
				return true
			}
		}
	}
	return false
}

func follow(v interface{}, keys []string, parent map[string]interface{}, out *[]jsonLDLeaf) {
	switch t := v.(type) {
	case []interface{}:
		for _, item := range t {
			follow(item, keys, parent, out)
		}
	case map[string]interface{}:
		if len(keys) == 0 {
			// A value object such as {"@value": "...", "@language": "fa"}.
			if inner, ok := t["@value"]; ok {
				follow(inner, nil, parent, out)
			}
			return
		}
		if next, ok := t[strings.TrimSpace(keys[0])]; ok {
			follow(next, keys[1:], t, out)
		}
	default:
		if len(keys) > 0 {
			return
		}
		if raw, ok := jsonLDScalar(t); ok {
			*out = append(*out, jsonLDLeaf{raw: raw, parent: parent})
		}
	}
}

func jsonLDScalar(v interface{}) (string, bool) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(html.UnescapeString(t))
		return s, s != ""
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}
