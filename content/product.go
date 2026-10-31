package content

import (
	"math"
	"net/url"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/resolver/crawler/modules"
)

// Product is the product a page sells. Sources says where each field came
// from: json_ld, microdata, rdfa, opengraph or a shop platform's HTML
// (woocommerce, magento, prestashop, opencart).
type Product struct {
	Name            string            `json:"name,omitempty"`
	Description     string            `json:"description,omitempty"`
	URL             string            `json:"url,omitempty"`
	Image           string            `json:"image,omitempty"`
	Images          []string          `json:"images,omitempty"`
	Brand           string            `json:"brand,omitempty"`
	SKU             string            `json:"sku,omitempty"`
	GTIN            string            `json:"gtin,omitempty"` // barcode: GTIN-8/12/13/14, EAN, UPC, ISBN
	MPN             string            `json:"mpn,omitempty"`
	Category        string            `json:"category,omitempty"`
	Breadcrumbs     []string          `json:"breadcrumbs,omitempty"`
	Price           *modules.Price    `json:"price,omitempty"`         // what the buyer pays now (the sale price during a discount)
	RegularPrice    *modules.Price    `json:"regular_price,omitempty"` // the price before a discount
	DiscountPercent int               `json:"discount_percent,omitempty"`
	LowPrice        *modules.Price    `json:"low_price,omitempty"`  // cheapest offer or variant, when there are several
	HighPrice       *modules.Price    `json:"high_price,omitempty"` // dearest offer or variant, when there are several
	OfferCount      int               `json:"offer_count,omitempty"`
	Availability    string            `json:"availability,omitempty"` // in_stock, out_of_stock, preorder, backorder, limited, discontinued
	Condition       string            `json:"condition,omitempty"`    // new, used, refurbished, damaged
	Seller          string            `json:"seller,omitempty"`
	Rating          float64           `json:"rating,omitempty"`
	BestRating      float64           `json:"best_rating,omitempty"` // scale of Rating (usually 5)
	ReviewCount     int               `json:"review_count,omitempty"`
	Variants        []ProductVariant  `json:"variants,omitempty"` // purchasable variants (size, colour, storage…), from hasVariant
	Sources         map[string]string `json:"sources,omitempty"`  // field → where it was found
}

// ProductVariant is one purchasable variant of a Product (a size, colour,
// storage capacity…), read from a schema.org hasVariant list. Attributes are
// parsed out of Name so the same variant can be matched across sites that
// word it differently ("128GB Black" vs. "مشکی 128 گیگابایت").
type ProductVariant struct {
	Name         string            `json:"name,omitempty"`
	SKU          string            `json:"sku,omitempty"`
	GTIN         string            `json:"gtin,omitempty"`
	MPN          string            `json:"mpn,omitempty"`
	Price        *modules.Price    `json:"price,omitempty"`
	RegularPrice *modules.Price    `json:"regular_price,omitempty"`
	Availability string            `json:"availability,omitempty"`
	Image        string            `json:"image,omitempty"`
	Attributes   map[string]string `json:"attributes,omitempty"` // color, storage, ram, size — parsed from Name
}

// maxProductVariants bounds the variants read off one page.
const maxProductVariants = 40

// Shop platforms whose default product templates are read.
const (
	SourceWooCommerce = "woocommerce"
	SourceMagento     = "magento"
	SourcePrestaShop  = "prestashop"
	SourceOpenCart    = "opencart"
)

const (
	maxProductImages = 10
	maxDescription   = 2000
)

// productTypes are the schema.org types that describe something for sale.
var productTypes = []string{"Product", "ProductGroup", "ProductModel", "IndividualProduct", "SomeProducts", "Vehicle", "Car", "Motorcycle"}

// detectProduct finds the product a page sells, or nil when the page is not
// a single product page.
func detectProduct(pg *pageData) *Product {
	out := &Product{Sources: map[string]string{}}
	found := false
	add := func(source string, p *Product) {
		if p != nil {
			found = true
			out.merge(source, p)
		}
	}
	for _, src := range pg.schemaSources() {
		if m := mainItem(findItems(src.items, ofTypes(productTypes), nil), pg.base); m != nil {
			add(src.name, productFromSchema(m, pg.base))
		}
	}
	add(SourceOpenGraph, productFromOpenGraph(pg))
	for _, pf := range platforms {
		if pg.doc.Find(pf.marker).Length() > 0 {
			add(pf.name, pf.extract(pg.doc, pg.base))
		}
	}
	if !found || (out.Name == "" && out.Price == nil) {
		return nil
	}
	if out.Breadcrumbs == nil {
		for _, src := range pg.schemaSources() {
			if crumbs := breadcrumbsFromItems(src.items); len(crumbs) > 0 {
				out.Breadcrumbs = crumbs
				out.Sources["breadcrumbs"] = src.name
				break
			}
		}
	}
	fixIranianCurrency(pg, out)
	out.finish()
	return out
}

// merge fills the fields of p that are still empty from c, recording source.
// Prices have their own rules, so this is not mergeInto.
func (p *Product) merge(source string, c *Product) {
	regular, low, high, offers := c.RegularPrice, c.LowPrice, c.HighPrice, c.OfferCount
	c.RegularPrice, c.LowPrice, c.HighPrice, c.OfferCount = nil, nil, nil, 0
	images, best := c.Images, c.BestRating
	c.Images, c.BestRating = nil, 0
	hadImage := p.Image != ""
	mergeInto(p, c, source, p.Sources)
	if !hadImage && p.Image != "" {
		p.Images = images
	}
	// A regular price is only meaningful next to a price in the same currency.
	if p.RegularPrice == nil && regular != nil && p.Price != nil && samePriceUnit(p.Price, regular) {
		p.RegularPrice = regular
		p.Sources["regular_price"] = source
	}
	if p.LowPrice == nil && low != nil && high != nil {
		p.LowPrice, p.HighPrice = low, high
		p.Sources["price_range"] = source
	}
	if p.OfferCount == 0 && offers > 0 {
		p.OfferCount = offers
		p.Sources["offer_count"] = source
	}
	// The scale belongs with the rating it came with.
	if p.Sources["rating"] == source && p.BestRating == 0 {
		p.BestRating = best
	}
}

// finish drops a regular price that is not above the price and computes the discount.
func (p *Product) finish() {
	if p.RegularPrice != nil && (p.Price == nil || p.RegularPrice.Amount <= p.Price.Amount) {
		p.RegularPrice = nil
		delete(p.Sources, "regular_price")
	}
	if p.RegularPrice != nil {
		p.DiscountPercent = int(math.Round((1 - p.Price.Amount/p.RegularPrice.Amount) * 100))
	}
}

func samePriceUnit(a, b *modules.Price) bool {
	return a.Currency == "" || b.Currency == "" || a.Currency == b.Currency
}

// ── schema.org Product ──────────────────────────────────────────────────

func productFromSchema(m map[string]interface{}, base *url.URL) *Product {
	p := &Product{
		Name:        cleanInline(schemaString(m["name"])),
		Description: truncateRunes(cleanInline(schemaString(m["description"])), maxDescription),
		URL:         resolveAny(base, schemaString(m["url"])),
		Brand:       cleanInline(schemaName(m["brand"])),
		SKU:         schemaString(m["sku"]),
		MPN:         schemaString(m["mpn"]),
		Category:    cleanInline(schemaName(m["category"])),
		Condition:   normalizeCondition(schemaString(m["itemCondition"])),
	}
	if p.Brand == "" {
		p.Brand = cleanInline(schemaName(m["manufacturer"]))
	}
	if p.SKU == "" {
		p.SKU = schemaString(m["productID"])
	}
	for _, k := range []string{"gtin", "gtin13", "gtin12", "gtin14", "gtin8", "isbn"} {
		if p.GTIN = schemaString(m[k]); p.GTIN != "" {
			break
		}
	}
	p.Images = schemaImages(m["image"], base, maxProductImages)
	if len(p.Images) > 0 {
		p.Image = p.Images[0]
	}
	p.Rating, p.BestRating, p.ReviewCount = schemaRating(m["aggregateRating"])

	offers := flattenOffers(m["offers"], 0)
	// A product group without its own offers is priced by its variants.
	if len(offers) == 0 {
		for _, v := range schemaList(m["hasVariant"]) {
			if vm, ok := v.(map[string]interface{}); ok {
				offers = append(offers, flattenOffers(vm["offers"], 0)...)
			}
		}
	}
	applyOffers(p, offers)
	p.Variants = variantsFromSchema(m, base)
	return p
}

// variantsFromSchema reads a ProductGroup's hasVariant list into individual
// purchasable variants, each with the offer a buyer would take and the
// attributes parsed out of its name.
func variantsFromSchema(m map[string]interface{}, base *url.URL) []ProductVariant {
	list := schemaList(m["hasVariant"])
	if len(list) == 0 {
		return nil
	}
	var out []ProductVariant
	for _, v := range list {
		vm, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		name := cleanInline(schemaString(vm["name"]))
		sku := schemaString(vm["sku"])
		if sku == "" {
			sku = schemaString(vm["productID"])
		}
		mpn := schemaString(vm["mpn"])
		var gtin string
		for _, k := range []string{"gtin", "gtin13", "gtin12", "gtin14", "gtin8", "isbn"} {
			if gtin = schemaString(vm[k]); gtin != "" {
				break
			}
		}
		price, regular, availability := chosenOffer(flattenOffers(vm["offers"], 0))
		if name == "" && sku == "" && gtin == "" && price == nil {
			continue
		}
		var image string
		if imgs := schemaImages(vm["image"], base, 1); len(imgs) > 0 {
			image = imgs[0]
		}
		out = append(out, ProductVariant{
			Name: name, SKU: sku, GTIN: gtin, MPN: mpn,
			Price: price, RegularPrice: regular, Availability: availability, Image: image,
			Attributes: ParseVariantAttributes(name),
		})
		if len(out) >= maxProductVariants {
			break
		}
	}
	return out
}

// chosenOffer picks the offer a buyer would take: the first in stock with a
// price, else the first priced offer.
func chosenOffer(offers []offer) (price, regular *modules.Price, availability string) {
	for i := range offers {
		o := &offers[i]
		if o.price == nil {
			continue
		}
		if price == nil || (availability != "in_stock" && o.availability == "in_stock") {
			price, regular, availability = o.price, o.regular, o.availability
		}
		if availability == "in_stock" {
			break
		}
	}
	return price, regular, availability
}

// schemaRating reads an AggregateRating: value, scale and number of reviews.
func schemaRating(v interface{}) (rating, best float64, count int) {
	r, ok := v.(map[string]interface{})
	if !ok {
		return 0, 0, 0
	}
	count = int(schemaNumber(r["reviewCount"]))
	if count == 0 {
		count = int(schemaNumber(r["ratingCount"]))
	}
	return schemaNumber(r["ratingValue"]), schemaNumber(r["bestRating"]), count
}

// offer is one priced offer read from an Offer, AggregateOffer or price specification.
type offer struct {
	price, regular, low, high *modules.Price
	count                     int
	availability, condition   string
	seller                    string
}

// flattenOffers turns an "offers" value (Offer, AggregateOffer, or a list of
// them) into offers; an AggregateOffer's own offers follow its summary.
func flattenOffers(v interface{}, depth int) []offer {
	if depth > 3 {
		return nil
	}
	var out []offer
	for _, item := range schemaList(v) {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		currency := schemaString(m["priceCurrency"])
		o := offer{
			price:        schemaPrice(m["price"], currency),
			low:          schemaPrice(m["lowPrice"], currency),
			high:         schemaPrice(m["highPrice"], currency),
			count:        int(schemaNumber(m["offerCount"])),
			availability: normalizeAvailability(schemaString(m["availability"])),
			condition:    normalizeCondition(schemaString(m["itemCondition"])),
			seller:       cleanInline(schemaName(m["seller"])),
		}
		for _, spec := range schemaList(m["priceSpecification"]) {
			sm, ok := spec.(map[string]interface{})
			if !ok {
				continue
			}
			cur := schemaString(sm["priceCurrency"])
			if cur == "" {
				cur = currency
			}
			sp := schemaPrice(sm["price"], cur)
			if sp == nil {
				continue
			}
			switch typ := strings.ToLower(schemaString(sm["priceType"])); {
			case strings.Contains(typ, "listprice"), strings.Contains(typ, "strikethroughprice"), strings.Contains(typ, "srp"):
				if o.regular == nil {
					o.regular = sp
				}
			case strings.Contains(typ, "saleprice"), typ == "":
				if o.price == nil {
					o.price = sp
				}
			}
		}
		out = append(out, o)
		out = append(out, flattenOffers(m["offers"], depth+1)...)
	}
	return out
}

// applyOffers picks the offer a buyer would take (the first in stock with a
// price) and the price range across all offers.
func applyOffers(p *Product, offers []offer) {
	var chosen *offer
	for i := range offers {
		o := &offers[i]
		if o.price == nil {
			continue
		}
		if chosen == nil || (chosen.availability != "in_stock" && o.availability == "in_stock") {
			chosen = o
		}
		if chosen.availability == "in_stock" {
			break
		}
	}
	low, high := priceRange(offers)
	if chosen != nil {
		p.Price, p.RegularPrice = chosen.price, chosen.regular
		p.Availability, p.Seller = chosen.availability, chosen.seller
		if chosen.condition != "" {
			p.Condition = chosen.condition
		}
	} else if len(offers) > 0 {
		// Only a range (AggregateOffer): the buyer pays from the low price.
		p.Price = low
		p.Availability, p.Seller = offers[0].availability, offers[0].seller
	}
	if low != nil && high != nil && low.Amount < high.Amount {
		p.LowPrice, p.HighPrice = low, high
	}
	count, priced := 0, 0
	for _, o := range offers {
		if o.count > count {
			count = o.count
		}
		if o.price != nil {
			priced++
		}
	}
	if count == 0 && priced > 1 {
		count = priced
	}
	p.OfferCount = count
}

// priceRange returns the lowest and highest price across offers, in the
// currency of the first price found.
func priceRange(offers []offer) (low, high *modules.Price) {
	for _, o := range offers {
		for _, cand := range []*modules.Price{o.price, o.low, o.high} {
			if cand == nil || (low != nil && !samePriceUnit(low, cand)) {
				continue
			}
			if low == nil || cand.Amount < low.Amount {
				low = cand
			}
			if high == nil || cand.Amount > high.Amount {
				high = cand
			}
		}
	}
	return low, high
}

// breadcrumbsFromItems reads the first BreadcrumbList, ordered by position.
func breadcrumbsFromItems(items []interface{}) []string {
	var lists []map[string]interface{}
	for _, item := range items {
		collectTyped(item, "BreadcrumbList", &lists)
	}
	for _, l := range lists {
		type crumb struct {
			pos  float64
			name string
		}
		var crumbs []crumb
		for i, el := range schemaList(l["itemListElement"]) {
			m, ok := el.(map[string]interface{})
			if !ok {
				continue
			}
			name := cleanInline(schemaString(m["name"]))
			if name == "" {
				name = cleanInline(schemaName(m["item"]))
			}
			if name == "" {
				continue
			}
			pos := schemaNumber(m["position"])
			if pos == 0 {
				pos = float64(i + 1)
			}
			crumbs = append(crumbs, crumb{pos, name})
		}
		if len(crumbs) == 0 {
			continue
		}
		sort.SliceStable(crumbs, func(i, j int) bool { return crumbs[i].pos < crumbs[j].pos })
		out := make([]string, len(crumbs))
		for i, c := range crumbs {
			out[i] = c.name
		}
		return out
	}
	return nil
}

// normalizeAvailability maps schema.org availability values ("InStock",
// "https://schema.org/OutOfStock"), OpenGraph values ("in stock", "oos") and
// common shop wording, including Persian, to a short code.
func normalizeAvailability(v string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	if strings.Contains(s, "schema") {
		s = strings.ToLower(schemaEnum(s))
	}
	if s == "" {
		return ""
	}
	compact := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(s)
	switch {
	case strings.Contains(s, "ناموجود"), strings.Contains(s, "موجود نیست"), strings.Contains(s, "عدم موجودی"),
		strings.Contains(s, "تمام شد"), strings.Contains(s, "اتمام موجودی"),
		strings.Contains(compact, "outofstock"), strings.Contains(compact, "soldout"), compact == "oos",
		strings.Contains(compact, "unavailable"):
		return "out_of_stock"
	case strings.Contains(compact, "discontinued"), strings.Contains(s, "توقف تولید"):
		return "discontinued"
	case strings.Contains(compact, "preorder"), strings.Contains(compact, "presale"), strings.Contains(s, "پیش خرید"), strings.Contains(s, "پیش‌خرید"):
		return "preorder"
	case strings.Contains(compact, "backorder"):
		return "backorder"
	case strings.Contains(compact, "limitedavailability"):
		return "limited"
	case strings.Contains(compact, "instock"), strings.Contains(compact, "instoreonly"), strings.Contains(compact, "onlineonly"),
		compact == "available", strings.Contains(s, "موجود"):
		return "in_stock"
	}
	return ""
}

// normalizeCondition maps schema.org item conditions to new, used, refurbished or damaged.
func normalizeCondition(v string) string {
	s := strings.ToLower(v)
	switch {
	case strings.Contains(s, "refurbished"):
		return "refurbished"
	case strings.Contains(s, "used"):
		return "used"
	case strings.Contains(s, "damaged"):
		return "damaged"
	case strings.Contains(s, "new"):
		return "new"
	}
	return ""
}

// ── OpenGraph and Facebook product tags ─────────────────────────────────

func productFromOpenGraph(pg *pageData) *Product {
	get := pg.metaFirst
	price := schemaPrice(get("product:price:amount", "og:price:amount"), get("product:price:currency", "og:price:currency"))
	sale := schemaPrice(get("product:sale_price:amount"), get("product:sale_price:currency", "product:price:currency"))
	if !strings.Contains(strings.ToLower(get("og:type")), "product") && price == nil && sale == nil {
		return nil
	}
	p := &Product{
		Name:         cleanInline(get("og:title")),
		Description:  truncateRunes(cleanInline(get("og:description")), maxDescription),
		URL:          resolveAny(pg.base, get("og:url")),
		Brand:        get("product:brand", "og:brand"),
		SKU:          get("product:retailer_item_id", "product:sku"),
		GTIN:         get("product:gtin", "product:ean", "product:upc", "product:isbn"),
		MPN:          get("product:mfr_part_no"),
		Category:     get("product:category"),
		Availability: normalizeAvailability(get("product:availability", "og:availability")),
		Condition:    normalizeCondition(get("product:condition")),
		Price:        price,
	}
	if sale != nil {
		p.Price, p.RegularPrice = sale, price
	}
	if img := resolveAny(pg.base, get("og:image:secure_url", "og:image", "og:image:url")); img != "" {
		p.Image, p.Images = img, []string{img}
	}
	return p
}

// ── shop platforms (HTML of their default product templates) ────────────

// platform reads product pages of a shop platform. marker matches only on
// its product pages; selector lists are tried in order.
type platform struct {
	name, marker string
	title        []string
	price        []string
	regular      []string
	sku          []string
	stock        []string
	category     []string
	images       []string
}

var platforms = []platform{
	{
		name:     SourceWooCommerce,
		marker:   "body.single-product",
		title:    []string{".summary .product_title", "h1.product_title"},
		price:    []string{".summary .price ins .woocommerce-Price-amount", ".summary .price .woocommerce-Price-amount", "p.price ins .woocommerce-Price-amount", "p.price .woocommerce-Price-amount"},
		regular:  []string{".summary .price del .woocommerce-Price-amount", "p.price del .woocommerce-Price-amount"},
		sku:      []string{".product_meta .sku"},
		stock:    []string{".summary .stock", "p.stock"},
		category: []string{".product_meta .posted_in a"},
		images:   []string{".woocommerce-product-gallery__image a", ".woocommerce-product-gallery__image img"},
	},
	{
		name:    SourceMagento,
		marker:  "body.catalog-product-view",
		title:   []string{".product-info-main .page-title", "h1.page-title"},
		price:   []string{".product-info-main [data-price-type=finalPrice] .price", ".product-info-main .price-final_price .price"},
		regular: []string{".product-info-main [data-price-type=oldPrice] .price", ".product-info-main .old-price .price"},
		sku:     []string{".product-info-main .product.attribute.sku .value"},
		stock:   []string{".product-info-main .stock"},
		images:  []string{".gallery-placeholder img", ".product.media img"},
	},
	{
		name:    SourcePrestaShop,
		marker:  "body#product",
		title:   []string{"h1.product-title", ".product-information h1", "#main h1"},
		price:   []string{".product-prices .current-price-value", ".product-prices .current-price span", ".current-price span"},
		regular: []string{".product-prices .regular-price", ".product-discount .regular-price"},
		sku:     []string{".product-reference span", "[itemprop=sku]"},
		stock:   []string{"#product-availability"},
		images:  []string{".product-cover img", "#product-modal .js-modal-product-cover"},
	},
	{
		name:   SourceOpenCart,
		marker: `body[class*="product-product"], #product-product, #product-info`,
		title:  []string{"#content h1", "h1"},
		// .price-new/.price-old outside these containers belong to related-product cards.
		price:   []string{".product-price-group .product-price-new", ".product-price-group .product-price", "#product-info .price-new", "#product-info .price", "#content ul.list-unstyled h2"},
		regular: []string{".product-price-group .product-price-old", "#product-info .price-old", `#content ul.list-unstyled span[style*="line-through"]`},
		images:  []string{"#content .thumbnails a", "#content .image img"},
	},
}

func (pf platform) extract(doc *goquery.Document, base *url.URL) *Product {
	text := func(sels []string) string {
		for _, sel := range sels {
			if t := strings.Join(strings.Fields(doc.Find(sel).First().Text()), " "); t != "" {
				return t
			}
		}
		return ""
	}
	price := func(sels []string) *modules.Price {
		for _, sel := range sels {
			s := doc.Find(sel).First()
			if s.Length() == 0 {
				continue
			}
			raw := strings.TrimSpace(s.AttrOr("content", ""))
			if raw == "" {
				raw = strings.Join(strings.Fields(s.Text()), " ")
			}
			if pr, ok := modules.ParsePrice(raw); ok && pr.Amount > 0 {
				return &pr
			}
		}
		return nil
	}
	p := &Product{
		Name:         text(pf.title),
		Price:        price(pf.price),
		RegularPrice: price(pf.regular),
		SKU:          text(pf.sku),
		Category:     text(pf.category),
	}
	for _, sel := range pf.stock {
		s := doc.Find(sel).First()
		if s.Length() == 0 {
			continue
		}
		if p.Availability = normalizeAvailability(s.AttrOr("class", "")); p.Availability == "" {
			p.Availability = normalizeAvailability(s.Text())
		}
		if p.Availability != "" {
			break
		}
	}
	seen := map[string]bool{}
	for _, sel := range pf.images {
		doc.Find(sel).EachWithBreak(func(_ int, s *goquery.Selection) bool {
			ref := ""
			for _, attr := range []string{"data-large_image", "href", "data-zoom-image", "data-src", "src"} {
				if v := strings.TrimSpace(s.AttrOr(attr, "")); v != "" && !strings.HasPrefix(v, "data:") && !strings.HasPrefix(v, "#") {
					ref = v
					break
				}
			}
			if abs := resolveAny(base, ref); abs != "" && !seen[abs] {
				seen[abs] = true
				p.Images = append(p.Images, abs)
			}
			return len(p.Images) < maxProductImages
		})
		if len(p.Images) > 0 {
			p.Image = p.Images[0]
			break
		}
	}
	return p
}
