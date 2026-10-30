package content

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/resolver/crawler/models"
)

// detectAll runs every detector on a page.
func detectAll(t *testing.T, pageURL, page string) *Detected {
	t.Helper()
	all := map[string]bool{}
	for _, d := range Detectors {
		all[d.Format] = true
	}
	d, err := Detect(pageURL, []byte(page), all)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func detect(t *testing.T, pageURL, page string) *Product {
	t.Helper()
	return detectAll(t, pageURL, page).Product
}

func wantPrice(t *testing.T, field string, got *Product, amount float64, currency string) {
	t.Helper()
	var pr interface{}
	switch field {
	case "price":
		pr = got.Price
	case "regular_price":
		pr = got.RegularPrice
	case "low_price":
		pr = got.LowPrice
	case "high_price":
		pr = got.HighPrice
	}
	b, _ := json.Marshal(pr)
	var m struct {
		Amount   float64
		Currency string
	}
	if json.Unmarshal(b, &m) != nil || string(b) == "null" || m.Amount != amount || m.Currency != currency {
		t.Errorf("%s = %s, want %v %s", field, b, amount, currency)
	}
}

// A WooCommerce product with Yoast's JSON-LD graph: the sale price is in the
// JSON-LD, the price before the discount only in the HTML.
const wooPage = `<html><head>
<meta property="og:type" content="product">
<meta property="og:title" content="گوشی سامسونگ A55 - فروشگاه نمونه">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[
 {"@type":"WebPage","@id":"https://shop.ir/product/a55/","name":"گوشی سامسونگ A55"},
 {"@type":"BreadcrumbList","itemListElement":[
   {"@type":"ListItem","position":2,"name":"موبایل","item":"https://shop.ir/c/mobile/"},
   {"@type":"ListItem","position":1,"name":"خانه","item":"https://shop.ir/"},
   {"@type":"ListItem","position":3,"name":"گوشی سامسونگ A55"}]}
]}</script>
<script type="application/ld+json">{"@context":"https://schema.org/","@type":"Product","@id":"https://shop.ir/product/a55/#product",
 "name":"گوشی سامسونگ A55","url":"https://shop.ir/product/a55/","sku":"A55-128",
 "description":"<p>گوشی &amp; <b>هوشمند</b></p>","image":"https://shop.ir/wp-content/a55.jpg",
 "brand":{"@type":"Brand","name":"Samsung"},"gtin13":"8806095467528",
 "offers":[{"@type":"Offer","price":"12500000","priceValidUntil":"2026-12-31",
   "priceSpecification":{"price":"12500000","priceCurrency":"IRT","valueAddedTaxIncluded":"false"},
   "priceCurrency":"IRT","availability":"http://schema.org/InStock","url":"https://shop.ir/product/a55/",
   "seller":{"@type":"Organization","name":"فروشگاه نمونه"}}],
 "aggregateRating":{"@type":"AggregateRating","ratingValue":"4.6","bestRating":"5","reviewCount":"37"}}</script>
</head><body class="product-template-default single single-product postid-1">
<div class="product"><div class="summary entry-summary">
 <h1 class="product_title entry-title">گوشی سامسونگ A55</h1>
 <p class="price"><del><span class="woocommerce-Price-amount amount"><bdi>۱۵٬۰۰۰٬۰۰۰&nbsp;<span class="woocommerce-Price-currencySymbol">تومان</span></bdi></span></del>
  <ins><span class="woocommerce-Price-amount amount"><bdi>۱۲٬۵۰۰٬۰۰۰&nbsp;<span class="woocommerce-Price-currencySymbol">تومان</span></bdi></span></ins></p>
 <p class="stock in-stock">۵ در انبار</p>
 <div class="product_meta"><span class="sku_wrapper">شناسه: <span class="sku">A55-128</span></span>
  <span class="posted_in">دسته: <a href="/c/mobile/">موبایل</a></span></div>
</div></div>
<section class="related products"><ul class="products"><li class="product"><span class="price"><span class="woocommerce-Price-amount amount"><bdi>9,000,000 تومان</bdi></span></span></li></ul></section>
</body></html>`

func TestDetectProductWooCommerce(t *testing.T) {
	p := detect(t, "https://shop.ir/product/a55/", wooPage)
	if p == nil {
		t.Fatal("no product detected")
	}
	if p.Name != "گوشی سامسونگ A55" || p.Sources["name"] != SourceJSONLD {
		t.Errorf("name = %q from %q", p.Name, p.Sources["name"])
	}
	wantPrice(t, "price", p, 12500000, "IRT")
	wantPrice(t, "regular_price", p, 15000000, "IRT")
	if p.Sources["price"] != SourceJSONLD || p.Sources["regular_price"] != SourceWooCommerce {
		t.Errorf("price sources = %q, %q", p.Sources["price"], p.Sources["regular_price"])
	}
	if p.DiscountPercent != 17 {
		t.Errorf("discount = %d, want 17", p.DiscountPercent)
	}
	if p.Availability != "in_stock" || p.SKU != "A55-128" || p.Brand != "Samsung" || p.GTIN != "8806095467528" {
		t.Errorf("availability/sku/brand/gtin = %q %q %q %q", p.Availability, p.SKU, p.Brand, p.GTIN)
	}
	if p.Description != "گوشی & هوشمند" {
		t.Errorf("description = %q", p.Description)
	}
	if p.Seller != "فروشگاه نمونه" || p.Rating != 4.6 || p.BestRating != 5 || p.ReviewCount != 37 {
		t.Errorf("seller/rating = %q %v/%v (%d)", p.Seller, p.Rating, p.BestRating, p.ReviewCount)
	}
	if p.Image != "https://shop.ir/wp-content/a55.jpg" || p.Category != "موبایل" {
		t.Errorf("image/category = %q %q", p.Image, p.Category)
	}
	if want := []string{"خانه", "موبایل", "گوشی سامسونگ A55"}; !reflect.DeepEqual(p.Breadcrumbs, want) {
		t.Errorf("breadcrumbs = %v, want %v", p.Breadcrumbs, want)
	}
	if p.LowPrice != nil || p.OfferCount != 0 {
		t.Errorf("a single offer has no range: %+v %d", p.LowPrice, p.OfferCount)
	}
}

func TestDetectProductMicrodata(t *testing.T) {
	page := `<html><body>
<div itemscope itemtype="https://schema.org/Product">
 <h1 itemprop="name">Kettle</h1>
 <img itemprop="image" src="/k.jpg">
 <link itemprop="url" href="/p/kettle">
 <div itemprop="brand" itemscope itemtype="https://schema.org/Brand"><span itemprop="name">Pars</span></div>
 <div itemprop="offers" itemscope itemtype="https://schema.org/Offer">
  <span itemprop="price" content="1250000">۱٫۲۵۰٫۰۰۰ ریال</span><meta itemprop="priceCurrency" content="IRR">
  <link itemprop="availability" href="https://schema.org/OutOfStock">
 </div>
 <div itemprop="aggregateRating" itemscope itemtype="https://schema.org/AggregateRating">
  <span itemprop="ratingValue">4</span> (<span itemprop="ratingCount">12</span>)</div>
</div>
<div itemscope itemtype="https://schema.org/Product"><span itemprop="name">Toaster</span>
 <a itemprop="url" href="/p/toaster">more</a></div>
<ol itemscope itemtype="https://schema.org/BreadcrumbList">
 <li itemprop="itemListElement" itemscope itemtype="https://schema.org/ListItem"><a itemprop="item" href="/"><span itemprop="name">Home</span></a><meta itemprop="position" content="1"></li>
 <li itemprop="itemListElement" itemscope itemtype="https://schema.org/ListItem"><span itemprop="name">Kitchen</span><meta itemprop="position" content="2"></li>
</ol></body></html>`
	p := detect(t, "https://shop.ir/p/kettle", page)
	if p == nil {
		t.Fatal("no product detected")
	}
	if p.Name != "Kettle" || p.Brand != "Pars" || p.Sources["name"] != SourceMicrodata {
		t.Errorf("name/brand = %q %q from %q (the related product must not win)", p.Name, p.Brand, p.Sources["name"])
	}
	wantPrice(t, "price", p, 1250000, "IRR")
	if p.Availability != "out_of_stock" || p.Rating != 4 || p.ReviewCount != 12 {
		t.Errorf("availability/rating = %q %v %d", p.Availability, p.Rating, p.ReviewCount)
	}
	if p.Image != "https://shop.ir/k.jpg" || !reflect.DeepEqual(p.Breadcrumbs, []string{"Home", "Kitchen"}) {
		t.Errorf("image/breadcrumbs = %q %v", p.Image, p.Breadcrumbs)
	}
}

func TestDetectProductRDFa(t *testing.T) {
	page := `<html><body vocab="https://schema.org/">
<div typeof="Product"><h1 property="name">Lamp</h1>
 <div property="offers" typeof="Offer"><span property="price" content="49.5">$49.50</span>
  <meta property="priceCurrency" content="USD"><link property="availability" href="https://schema.org/PreOrder"></div>
</div></body></html>`
	p := detect(t, "https://shop.example/lamp", page)
	if p == nil || p.Name != "Lamp" || p.Sources["name"] != SourceRDFa {
		t.Fatalf("product = %+v", p)
	}
	wantPrice(t, "price", p, 49.5, "USD")
	if p.Availability != "preorder" {
		t.Errorf("availability = %q", p.Availability)
	}
}

func TestDetectProductOpenGraph(t *testing.T) {
	page := `<html><head>
<meta property="og:type" content="og:product"><meta property="og:title" content="Desk">
<meta property="og:image" content="/desk.png">
<meta property="product:price:amount" content="99.90"><meta property="product:price:currency" content="EUR">
<meta property="product:sale_price:amount" content="79.92"><meta property="product:sale_price:currency" content="EUR">
<meta property="product:availability" content="in stock"><meta property="product:retailer_item_id" content="D-1">
</head><body></body></html>`
	p := detect(t, "https://shop.example/desk", page)
	if p == nil || p.Name != "Desk" || p.SKU != "D-1" || p.Sources["price"] != SourceOpenGraph {
		t.Fatalf("product = %+v", p)
	}
	wantPrice(t, "price", p, 79.92, "EUR")
	wantPrice(t, "regular_price", p, 99.9, "EUR")
	if p.DiscountPercent != 20 || p.Availability != "in_stock" || p.Image != "https://shop.example/desk.png" {
		t.Errorf("discount/availability/image = %d %q %q", p.DiscountPercent, p.Availability, p.Image)
	}
}

func TestDetectProductOffers(t *testing.T) {
	t.Run("aggregate offer", func(t *testing.T) {
		page := `<script type="application/ld+json">{"@type":"Product","name":"Cable",
 "offers":{"@type":"AggregateOffer","lowPrice":"100","highPrice":200,"priceCurrency":"USD","offerCount":"5"}}</script>`
		p := detect(t, "https://m.example/cable", page)
		wantPrice(t, "price", p, 100, "USD")
		wantPrice(t, "low_price", p, 100, "USD")
		wantPrice(t, "high_price", p, 200, "USD")
		if p.OfferCount != 5 {
			t.Errorf("offer count = %d", p.OfferCount)
		}
	})
	t.Run("variants prefer an offer in stock", func(t *testing.T) {
		page := `<script type="application/ld+json">{"@type":"ProductGroup","name":"Shirt","hasVariant":[
 {"@type":"Product","name":"Shirt S","offers":{"@type":"Offer","price":300000,"priceCurrency":"IRT","availability":"https://schema.org/OutOfStock"}},
 {"@type":"Product","name":"Shirt M","offers":{"@type":"Offer","price":350000,"priceCurrency":"IRT","availability":"https://schema.org/InStock"}}]}</script>`
		p := detect(t, "https://m.example/shirt", page)
		if p == nil || p.Name != "Shirt" {
			t.Fatalf("product = %+v", p)
		}
		wantPrice(t, "price", p, 350000, "IRT")
		wantPrice(t, "low_price", p, 300000, "IRT")
		wantPrice(t, "high_price", p, 350000, "IRT")
		if p.Availability != "in_stock" || p.OfferCount != 2 {
			t.Errorf("availability/offers = %q %d", p.Availability, p.OfferCount)
		}
	})
	t.Run("list price specification", func(t *testing.T) {
		page := `<script type="application/ld+json">{"@type":"Product","name":"Pan","offers":{"@type":"Offer","price":"80","priceCurrency":"USD",
 "priceSpecification":[{"@type":"UnitPriceSpecification","priceType":"https://schema.org/ListPrice","price":"100","priceCurrency":"USD"}],
 "itemCondition":"https://schema.org/RefurbishedCondition"}}</script>`
		p := detect(t, "https://m.example/pan", page)
		wantPrice(t, "price", p, 80, "USD")
		wantPrice(t, "regular_price", p, 100, "USD")
		if p.DiscountPercent != 20 || p.Condition != "refurbished" {
			t.Errorf("discount/condition = %d %q", p.DiscountPercent, p.Condition)
		}
	})
	t.Run("zero means call for price", func(t *testing.T) {
		page := `<script type="application/ld+json">{"@type":"Product","name":"Engine","offers":{"price":"0","priceCurrency":"IRR"}}</script>`
		p := detect(t, "https://m.example/engine", page)
		if p == nil || p.Name != "Engine" || p.Price != nil {
			t.Errorf("product = %+v", p)
		}
	})
	t.Run("currency code the parser does not know", func(t *testing.T) {
		page := `<script type="application/ld+json">{"@type":"Product","name":"Yen","offers":{"price":"1200","priceCurrency":"JPY"}}</script>`
		wantPrice(t, "price", detect(t, "https://m.example/yen", page), 1200, "JPY")
	})
}

func TestDetectProductOpenCart(t *testing.T) {
	page := `<html><body class="product-product-42"><div id="content">
<h1>هدفون بی‌سیم</h1>
<ul class="list-unstyled"><li>برند: <a href="#">X</a></li></ul>
<ul class="list-unstyled"><li><span style="text-decoration: line-through;">2,400,000 تومان</span></li><li><h2>1,990,000 تومان</h2></li></ul>
<div class="row"><div class="product-thumb"><p class="price"><span class="price-new">500,000 تومان</span><span class="price-old">700,000 تومان</span></p></div></div>
</div></body></html>`
	p := detect(t, "https://shop.ir/index.php?route=product/product&product_id=42", page)
	if p == nil || p.Name != "هدفون بی‌سیم" || p.Sources["price"] != SourceOpenCart {
		t.Fatalf("product = %+v", p)
	}
	wantPrice(t, "price", p, 1990000, "IRT")
	wantPrice(t, "regular_price", p, 2400000, "IRT")
}

func TestDetectProductNotAProductPage(t *testing.T) {
	cases := map[string]string{
		"article": `<html><head><meta property="og:type" content="article"><script type="application/ld+json">{"@type":"NewsArticle","headline":"x"}</script></head><body><h1>News</h1></body></html>`,
		"category listing": `<script type="application/ld+json">{"@type":"ItemList","itemListElement":[
 {"@type":"ListItem","position":1,"item":{"@type":"Product","name":"A","offers":{"price":"1","priceCurrency":"USD"}}},
 {"@type":"ListItem","position":2,"item":{"@type":"Product","name":"B","offers":{"price":"2","priceCurrency":"USD"}}}]}</script>`,
		"several unrelated products": `<script type="application/ld+json">[{"@type":"Product","name":"A"},{"@type":"Product","name":"B"}]</script>`,
		"woocommerce shop page":      `<html><body class="archive post-type-archive-product"><ul class="products"><li><span class="price">1,000 تومان</span></li></ul></body></html>`,
	}
	for name, page := range cases {
		if p := detect(t, "https://shop.ir/x", page); p != nil {
			t.Errorf("%s: detected %+v", name, p)
		}
	}
}

func TestNormalizeAvailability(t *testing.T) {
	for in, want := range map[string]string{
		"https://schema.org/InStock": "in_stock", "http://schema.org/OutOfStock": "out_of_stock", "SoldOut": "out_of_stock",
		"LimitedAvailability": "limited", "PreOrder": "preorder", "BackOrder": "backorder", "Discontinued": "discontinued",
		"in stock": "in_stock", "oos": "out_of_stock", "stock in-stock": "in_stock", "stock out-of-stock": "out_of_stock",
		"stock available-on-backorder": "backorder", "stock unavailable": "out_of_stock",
		"موجود در انبار": "in_stock", "ناموجود": "out_of_stock", "این کالا موجود نیست": "out_of_stock", "پیش‌خرید": "preorder",
		"": "", "call us": "",
	} {
		if got := normalizeAvailability(in); got != want {
			t.Errorf("normalizeAvailability(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiffDetected(t *testing.T) {
	before := models.JSONRaw(`{"name":"A","price":{"amount":100,"currency":"IRT","raw":"100 IRT"},"availability":"in_stock","sources":{"price":"json_ld"}}`)
	same := models.JSONRaw(`{"name":"A","price":{"amount":100,"currency":"IRT","raw":"۱۰۰ تومان"},"availability":"in_stock","sources":{"price":"woocommerce"}}`)
	if d := DiffDetected("product", before, same); len(d) != 0 {
		t.Errorf("cosmetic changes reported: %+v", d)
	}
	after := models.JSONRaw(`{"name":"A","price":{"amount":90,"currency":"IRT","raw":"90 IRT"},"availability":"out_of_stock"}`)
	d := DiffDetected("product", before, after)
	if len(d) != 2 || d[0].Name != "product.availability" || d[1].Name != "product.price" {
		t.Errorf("diff = %+v", d)
	}
	if DiffDetected("product", nil, after) != nil {
		t.Error("a page without a baseline result must not report field changes")
	}
	faqA := models.JSONRaw(`[{"question":"Q","answer":"A"}]`)
	faqB := models.JSONRaw(`[{"question":"Q","answer":"B"}]`)
	if d := DiffDetected("faq", faqA, faqA); d != nil {
		t.Errorf("unchanged list reported: %+v", d)
	}
	if d := DiffDetected("faq", faqA, faqB); len(d) != 1 || d[0].Name != "faq" {
		t.Errorf("faq diff = %+v", d)
	}
}
