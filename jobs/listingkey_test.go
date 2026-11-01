package jobs

import (
	"testing"

	"github.com/resolver/crawler/models"
)

func keyOf(site, url, sku, gtin, brand, mpn, variant string) listingKeyParts {
	return listingKeyFor(site, url, sku, gtin, brand, mpn, variant)
}

// The case the whole design exists for: a shop rewrites a product's slug
// for SEO and the product has not changed. Keyed by the URL, that started a
// second listing and forked the price history; keyed by the id in the path,
// it is the same listing.
func TestListingKeySurvivesSlugRewrite(t *testing.T) {
	before := keyOf("shop.ir", "https://shop.ir/product/12345-iphone-15-pro", "", "", "", "", "")
	after := keyOf("shop.ir", "https://shop.ir/product/12345-iphone-15-pro-max-256gb", "", "", "", "", "")
	if before.key != after.key {
		t.Errorf("a slug rewrite changed the identity:\n before %q\n after  %q", before.key, after.key)
	}
	if before.method != models.ListingByPath {
		t.Errorf("method = %q, want %q", before.method, models.ListingByPath)
	}
	if !models.StableListingKey(before.method) {
		t.Error("a path key should count as stable")
	}
}

// An id is only an identity within the section of the site it appears in,
// or /product/12345 and /blog/12345 would be the same listing.
func TestListingKeyDoesNotCollideAcrossSections(t *testing.T) {
	product := keyOf("shop.ir", "https://shop.ir/product/12345-thing", "", "", "", "", "")
	blog := keyOf("shop.ir", "https://shop.ir/blog/12345-thing", "", "", "", "", "")
	if product.key == blog.key {
		t.Errorf("different sections share an identity: %q", product.key)
	}
}

// A listing is per shop, so the same barcode at two shops is two listings
// with two histories — unlike a product group, which spans shops.
func TestListingKeyIsScopedToTheShop(t *testing.T) {
	a := keyOf("shop-a.ir", "https://shop-a.ir/p/1", "", "0712345678901", "", "", "")
	b := keyOf("shop-b.ir", "https://shop-b.ir/p/2", "", "0712345678901", "", "", "")
	if a.key == b.key {
		t.Error("the same barcode at two shops must be two listings")
	}
	if a.method != models.ListingByGTIN {
		t.Errorf("method = %q, want %q", a.method, models.ListingByGTIN)
	}
}

func TestListingKeyPrefersStrongestIdentity(t *testing.T) {
	cases := []struct {
		name                  string
		sku, gtin, brand, mpn string
		wantMethod            string
	}{
		{"sku wins", "DKP-9", "0712345678901", "apple", "MU123", models.ListingBySKU},
		{"gtin next", "", "0712345678901", "apple", "MU123", models.ListingByGTIN},
		{"brand and mpn next", "", "", "apple", "MU123", models.ListingByMPN},
		{"mpn alone is not enough", "", "", "", "MU123", models.ListingByPath},
		{"nothing falls back to the path", "", "", "", "", models.ListingByPath},
	}
	for _, c := range cases {
		got := keyOf("shop.ir", "https://shop.ir/product/12345-x", c.sku, c.gtin, c.brand, c.mpn, "")
		if got.method != c.wantMethod {
			t.Errorf("%s: method = %q, want %q (key %q)", c.name, got.method, c.wantMethod, got.key)
		}
	}
}

// A URL with no id at all cannot survive a rewrite, and says so rather
// than pretending otherwise.
func TestListingKeyFallsBackToTheWholeURL(t *testing.T) {
	got := keyOf("shop.ir", "https://shop.ir/shop/iphone-15-pro", "", "", "", "", "")
	if got.method != models.ListingByURL {
		t.Errorf("method = %q, want %q", got.method, models.ListingByURL)
	}
	if models.StableListingKey(got.method) {
		t.Error("a URL key must not count as stable")
	}
	if describeListingKey(got.method) == "" {
		t.Error("the weakest key is the one that most needs explaining")
	}
}

// One shop's 128GB and 256GB offers are two listings with two histories,
// the same discipline groups apply to variants.
func TestListingKeySeparatesVariants(t *testing.T) {
	small := keyOf("shop.ir", "https://shop.ir/p/1", "SKU1", "", "", "", "storage:128gb")
	large := keyOf("shop.ir", "https://shop.ir/p/1", "SKU1", "", "", "", "storage:256gb")
	plain := keyOf("shop.ir", "https://shop.ir/p/1", "SKU1", "", "", "", "")
	if small.key == large.key {
		t.Error("two variants of one page must be two listings")
	}
	if plain.key == small.key {
		t.Error("a page with no variants must not collide with one of its variants")
	}
}

// The identity ignores what normalization should ignore, so a scheme
// change or a tracking anchor does not fork a history.
func TestListingKeyIgnoresSchemeAndFragment(t *testing.T) {
	a := keyOf("shop.ir", "http://shop.ir/shop/thing", "", "", "", "", "")
	b := keyOf("shop.ir", "https://www.shop.ir/shop/thing/#reviews", "", "", "", "", "")
	if a.key != b.key {
		t.Errorf("normalization missed something:\n a %q\n b %q", a.key, b.key)
	}
}

// The fallbacks are what let a listing be adopted and upgraded when a shop
// starts publishing a SKU, instead of forking its history.
func TestListingFallbackKeysOfferThePathThenTheURL(t *testing.T) {
	fallbacks := listingFallbackKeys("shop.ir", "https://shop.ir/product/12345-x", "")
	if len(fallbacks) != 2 {
		t.Fatalf("fallbacks = %d, want 2 (path then URL)", len(fallbacks))
	}
	if fallbacks[0].method != models.ListingByPath || fallbacks[1].method != models.ListingByURL {
		t.Errorf("fallback order = %q, %q; want path then URL", fallbacks[0].method, fallbacks[1].method)
	}
	// The path fallback must equal what a page with no strong id derives,
	// or an upgrade would never find the listing it should adopt.
	plain := keyOf("shop.ir", "https://shop.ir/product/12345-x", "", "", "", "", "")
	if fallbacks[0].key != plain.key {
		t.Errorf("path fallback %q does not match the path key %q", fallbacks[0].key, plain.key)
	}
	// And a variant must stay separated through the fallbacks too.
	withVariant := listingFallbackKeys("shop.ir", "https://shop.ir/product/12345-x", "color:black")
	if withVariant[0].key == fallbacks[0].key {
		t.Error("a fallback key must still separate variants")
	}
}

// A page whose strong key is already the path must not list it twice.
func TestCandidateFallbacksExcludeThePrimaryKey(t *testing.T) {
	c := candidate{siteKey: "shop.ir", url: "https://shop.ir/product/12345-x"}
	c.listingIdentity()
	if c.keyMethod != models.ListingByPath {
		t.Fatalf("method = %q, want %q", c.keyMethod, models.ListingByPath)
	}
	for _, h := range c.fallbackHashes {
		if h == c.listingHash {
			t.Error("the primary identity is repeated as a fallback")
		}
	}
	if len(c.fallbackHashes) != 1 {
		t.Errorf("fallbacks = %d, want 1 (the URL key only)", len(c.fallbackHashes))
	}
}

// A candidate with a SKU should still be able to adopt the listing it was
// filed under before the shop published one.
func TestCandidateWithSKUKeepsPathFallback(t *testing.T) {
	c := candidate{siteKey: "shop.ir", url: "https://shop.ir/product/12345-x", sku: "DKP-9"}
	c.listingIdentity()
	if c.keyMethod != models.ListingBySKU {
		t.Fatalf("method = %q, want %q", c.keyMethod, models.ListingBySKU)
	}
	if len(c.fallbackHashes) != 2 {
		t.Fatalf("fallbacks = %d, want 2 (path and URL)", len(c.fallbackHashes))
	}
	want := listingHash(listingFallbackKeys("shop.ir", "https://shop.ir/product/12345-x", "")[0].key)
	if c.fallbackHashes[0] != want {
		t.Error("the first fallback is not the path key the listing would have been filed under")
	}
}

func TestLooksLikeOpaqueID(t *testing.T) {
	yes := []string{"b07xjc8f5", "abc123x", "x1y2z3"}
	no := []string{"iphone", "12345678", "ab1", "a-b-123456", "خرید"}
	for _, s := range yes {
		if !looksLikeOpaqueID(s) {
			t.Errorf("%q should look like a product code", s)
		}
	}
	for _, s := range no {
		if looksLikeOpaqueID(s) {
			t.Errorf("%q should not look like a product code", s)
		}
	}
}

func TestURLPathIdentityFindsOpaqueCodes(t *testing.T) {
	structural, ids, ok := urlPathIdentity("https://shop.com/dp/B07XJC8F5/ref=sr_1")
	if !ok {
		t.Fatal("no id found in an Amazon-style path")
	}
	if ids == "" || structural == "" {
		t.Errorf("structural = %q, ids = %q; want both", structural, ids)
	}
	if _, _, ok := urlPathIdentity("https://shop.com/shop/all-phones"); ok {
		t.Error("a path of plain words carries no id")
	}
}

// A short number is a page number or a size, not a product id; treating it
// as one would collapse unrelated listings.
func TestURLPathIdentityIgnoresShortNumbers(t *testing.T) {
	if _, _, ok := urlPathIdentity("https://shop.ir/category/phones/2"); ok {
		t.Error("a short number should not be read as a product id")
	}
}

func TestListingHashIsStableAndDistinct(t *testing.T) {
	a := listingHash("sku:shop.ir|dkp-9")
	if a != listingHash("sku:shop.ir|dkp-9") {
		t.Error("the same key must hash the same way")
	}
	if len(a) != 40 {
		t.Errorf("hash length = %d, want 40 so the unique index is fixed width", len(a))
	}
	if a == listingHash("sku:shop.ir|dkp-8") {
		t.Error("different keys must hash differently")
	}
}
