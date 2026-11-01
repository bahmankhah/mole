package jobs

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/resolver/crawler/models"
)

// ── listing identity ───────────────────────────────────────────────────
//
// A listing's key answers "is this the same offer at this shop as the one
// we saw last run?". It must therefore survive everything a shop routinely
// changes — above all the URL slug, which shops rewrite for SEO without
// the product changing at all — while never conflating two genuinely
// different offers.
//
// The key is always scoped to the site. That is the difference from a
// ProductGroup's canonical key, which is deliberately cross-site: a GTIN
// identifies the same product everywhere, but "digikala's offer of it" and
// "technolife's offer of it" are two listings with two separate histories.

// idSegmentRe matches a path segment that is nothing but a long number:
// /product/12345, /dkp-12345 (after the prefix is split off), /p/987654.
var idSegmentRe = regexp.MustCompile(`^[0-9]{4,}$`)

// slugIDRe pulls a leading or trailing numeric id off a slug segment:
// "12345-iphone-15-pro" or "iphone-15-pro-12345" both yield "12345". This
// is the pattern that makes a key survive a slug rewrite, since the id
// stays put while the words after it change.
var slugIDRe = regexp.MustCompile(`(?:^([0-9]{4,})[-_]|[-_]([0-9]{4,})$)`)

// looksLikeOpaqueID reports whether a path segment is an opaque
// per-product code — at least six characters mixing letters and digits,
// like "b07xjc8f5" or "abc123x". Shops use these as stable product ids;
// ordinary words and slugs almost never look like one. (Written out rather
// than as a regexp because Go's RE2 engine has no lookahead.)
func looksLikeOpaqueID(seg string) bool {
	if len(seg) < 6 {
		return false
	}
	var hasLetter, hasDigit bool
	for _, r := range seg {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'z':
			hasLetter = true
		default:
			return false // anything else (including -, _) is not an opaque id
		}
	}
	return hasLetter && hasDigit
}

// listingKeyParts is the identity of one listing, before hashing.
type listingKeyParts struct {
	key    string // readable form, e.g. "sku:digikala.com|DKP-123#color:black"
	method string // one of the models.ListingBy* constants
}

// listingKeyFor derives a listing's identity from the strongest per-site id
// the page carries, strongest first:
//
//	sku  — the shop's own product id, read off the page. Best: it is what
//	       the shop itself uses to mean "this product".
//	gtin — a barcode. Globally unique, scoped here to the site.
//	mpn  — brand + manufacturer part number, likewise scoped.
//	path — the stable id segments of the URL path. Survives a slug rewrite.
//	url  — the whole normalized URL. Honest last resort: any URL change
//	       starts a new listing, and the dashboard says so.
//
// variantKey is appended, so one shop's 128GB and 256GB offers of a model
// are two listings with two histories — the same discipline
// scopedCanonicalKey applies to groups.
func listingKeyFor(siteKey, pageURL, sku, gtin, brand, mpn, variantKey string) listingKeyParts {
	site := normKey(siteKey)
	var p listingKeyParts
	switch {
	case normKey(sku) != "":
		p = listingKeyParts{"sku:" + site + "|" + normKey(sku), models.ListingBySKU}
	case normKey(gtin) != "":
		p = listingKeyParts{"gtin:" + site + "|" + normKey(gtin), models.ListingByGTIN}
	case normKey(brand) != "" && normKey(mpn) != "":
		p = listingKeyParts{"mpn:" + site + "|" + normKey(brand) + "|" + normKey(mpn), models.ListingByMPN}
	default:
		if structural, ids, ok := urlPathIdentity(pageURL); ok {
			p = listingKeyParts{"path:" + site + "|" + structural + "|" + ids, models.ListingByPath}
		} else {
			p = listingKeyParts{"url:" + site + "|" + normalizedURLKey(pageURL), models.ListingByURL}
		}
	}
	if variantKey != "" {
		p.key += "#" + variantKey
	}
	return p
}

// listingFallbackKeys are the weaker identities the same offer would carry
// if the page had no strong per-product id: the ids in its URL path, then
// the whole URL.
//
// upsertListing tries these when the strong key matches nothing, so an
// offer first seen before the shop published a SKU — or before listings
// carried keys at all — is adopted and upgraded in place rather than
// forked into a second history. They are derived from the current URL, so
// they can only ever match a listing last seen at this same URL.
func listingFallbackKeys(siteKey, pageURL, variantKey string) []listingKeyParts {
	site := normKey(siteKey)
	var out []listingKeyParts
	if structural, ids, ok := urlPathIdentity(pageURL); ok {
		out = append(out, listingKeyParts{"path:" + site + "|" + structural + "|" + ids, models.ListingByPath})
	}
	out = append(out, listingKeyParts{"url:" + site + "|" + normalizedURLKey(pageURL), models.ListingByURL})
	if variantKey != "" {
		for i := range out {
			out[i].key += "#" + variantKey
		}
	}
	return out
}

// urlPathIdentity splits a URL's path into the parts that name a place on
// the site and the parts that look like a product id. It reports false
// when the path carries no id at all, which is when a slug rewrite cannot
// be survived and the caller must fall back to the whole URL.
//
// The structural part is kept alongside the ids so that /product/12345 and
// /blog/12345 stay different listings: an id is only an identity within
// the section of the site it appears in.
func urlPathIdentity(rawURL string) (structural, ids string, ok bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", "", false
	}
	var structuralParts, idParts []string
	for _, seg := range strings.Split(parsed.Path, "/") {
		seg = normKey(seg)
		if seg == "" {
			continue
		}
		switch {
		case idSegmentRe.MatchString(seg):
			idParts = append(idParts, seg)
		case looksLikeOpaqueID(seg):
			idParts = append(idParts, seg)
		default:
			// A slug may carry the id with the words: keep the id and
			// drop the words, which are what a rewrite changes.
			if m := slugIDRe.FindStringSubmatch(seg); m != nil {
				id := m[1]
				if id == "" {
					id = m[2]
				}
				idParts = append(idParts, id)
				continue
			}
			structuralParts = append(structuralParts, seg)
		}
	}
	if len(idParts) == 0 {
		return "", "", false
	}
	return strings.Join(structuralParts, "/"), strings.Join(idParts, "/"), true
}

// normalizedURLKey is the fallback identity: host, path and the query,
// lower-cased with the scheme and fragment dropped, so http/https and a
// trailing #anchor do not split a listing in two.
func normalizedURLKey(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return normKey(rawURL)
	}
	parsed.Scheme, parsed.Fragment = "", ""
	host := strings.TrimPrefix(normKey(parsed.Host), "www.")
	path := strings.TrimSuffix(parsed.Path, "/")
	out := host + path
	if q := parsed.Query(); len(q) > 0 {
		out += "?" + q.Encode()
	}
	return normKey(out)
}

// listingHash is the stored identity: a digest of the readable key, so the
// unique index is a fixed 40 characters however long the key grows.
func listingHash(key string) string {
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:])
}

// describeListingKey explains a key method in the terms the dashboard
// shows, so a user can see why a history broke (or why it will not).
func describeListingKey(method string) string {
	switch method {
	case models.ListingBySKU:
		return "matched by the shop's own product id — the most reliable; survives URL changes"
	case models.ListingByGTIN:
		return "matched by barcode at this shop — survives URL changes"
	case models.ListingByMPN:
		return "matched by brand and part number at this shop — survives URL changes"
	case models.ListingByPath:
		return "matched by the product id in its URL — survives a slug rewrite"
	case models.ListingByURL:
		return "matched by its full URL — this page carries no product id, so if the shop changes the URL this history restarts"
	}
	return ""
}

// overrideKey identifies a listing by the tuple the pre-listing schema
// used. It is kept only to backfill and to read rows written before
// listings existed; new code keys on a listing id.
func overrideKey(siteKey, url, variantKey string) string {
	return siteKey + "\x00" + url + "\x00" + variantKey
}

// scopeLockName is the advisory lock one scope's matching pass takes.
func scopeLockName(scopeType, scopeID string) string {
	sum := sha1.Sum([]byte(scopeType + "/" + scopeID))
	return fmt.Sprintf("mole:regroup:%s", hex.EncodeToString(sum[:8]))
}
