package content

import (
	"fmt"
	"strings"
	"testing"

	"github.com/resolver/crawler/models"
)

func classify(t *testing.T, pageURL, page string) Classification {
	t.Helper()
	p, err := ParsePage(pageURL, []byte(page))
	if err != nil {
		t.Fatal(err)
	}
	return p.Classify()
}

// productCards renders n listing cards: link, image, title and price.
func productCards(n int, withButton bool) string {
	var b strings.Builder
	b.WriteString(`<div class="products">`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<div class="product-card"><a href="/item/%d"><img src="/i/%d.jpg"><h3>کالای %d</h3></a><span class="price">%d,000 تومان</span>`, i, i, i, 100+i)
		if withButton {
			b.WriteString(`<button class="add-to-cart">افزودن به سبد</button>`)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func TestClassifyPlatformMarks(t *testing.T) {
	cases := []struct {
		name, url, page, want string
	}{
		{"prestashop product without structured data", "https://www.lighthome.ir/garden-light/12554-chirag.html",
			`<html><head><script>var prestashop = {"page":{"page_name":"product"}};</script></head><body id="product" class="lang-fa page-product product-id-12554"><h1>چراغ باغی</h1></body></html>`,
			models.PageTypeProduct},
		{"prestashop category", "https://shop.ir/12-garden-light",
			`<html><body id="category" class="lang-fa page-category"><h1>چراغ باغی</h1></body></html>`, models.PageTypeListing},
		{"woocommerce product", "https://shop.ir/kala/x/",
			`<html><head><link rel="stylesheet" href="/wp-content/themes/x.css"></head><body class="product-template-default single single-product postid-5 woocommerce"></body></html>`,
			models.PageTypeProduct},
		{"wordpress post", "https://blog.ir/my-post/",
			`<html><head><link rel="stylesheet" href="/wp-content/themes/x.css"></head><body class="post-template-default single single-post postid-9"></body></html>`,
			models.PageTypeArticle},
		{"magento category", "https://shop.ir/lamps.html", `<html><body class="page-products catalog-category-view"></body></html>`, models.PageTypeListing},
		{"opencart product", "https://shop.ir/index.php?route=product/product&product_id=42", `<html><body class="product-product-42"></body></html>`, models.PageTypeProduct},
		{"shopify collection", "https://shop.com/collections/mens",
			`<html><head><script>window.ShopifyAnalytics = {}; var meta = {"page":{"pageType":"collection"}};</script></head><body></body></html>`,
			models.PageTypeListing},
		{"next.js route", "https://shop.ir/product-123/x",
			`<html><body><script id="__NEXT_DATA__" type="application/json">{"page":"/product-[id]/[slug]","props":{}}</script></body></html>`,
			models.PageTypeProduct},
		{"home page", "https://shop.ir/", `<html><body>` + productCards(20, true) + `</body></html>`, models.PageTypeHome},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classify(t, c.url, c.page)
			if got.Type != c.want {
				t.Errorf("type = %s, want %s (%v %v)", got.Type, c.want, got.Scores, got.Signals)
			}
		})
	}
}

func TestClassifyLayout(t *testing.T) {
	related := productCards(6, true) // a "related products" carousel
	longText := strings.Repeat("<p>"+strings.Repeat("کلمه ", 60)+"</p>", 8)
	cases := []struct {
		name, url, page, want string
	}{
		{"buy button outside cards beats a related-products carousel", "https://shop.ir/x/123-lamp.html",
			`<html><body><h1>چراغ</h1><div class="price">۱,۲۵۰,۰۰۰ تومان</div><button class="btn">افزودن به سبد خرید</button><input name="qty" value="1">` + related + `</body></html>`,
			models.PageTypeProduct},
		{"out of stock product", "https://shop.ir/x/124-lamp.html",
			`<html><body><h1>چراغ</h1><button>موجود شد خبرم کن</button></body></html>`, models.PageTypeProduct},
		{"category with many product cards", "https://shop.ir/garden-lights",
			`<html><body><h1>چراغ باغی</h1>` + productCards(24, true) + `<a rel="next" href="?page=2">بعدی</a></body></html>`,
			models.PageTypeListing},
		{"article with long text and a date", "https://news.ir/2295141/x",
			`<html><body><article><h1>عنوان</h1><time datetime="2026-10-01">۱ مهر</time>` + longText + `</article></body></html>`,
			models.PageTypeArticle},
		{"classified ad", "https://divar.ir/v/x/gaABC",
			`<html><body><h1>پراید ۱۳۹۰</h1><button>اطلاعات تماس</button><div>۲۵۰ میلیون تومان</div></body></html>`,
			models.PageTypeProduct},
		{"empty search result", "https://divar.ir/s/karaj/car/seat",
			`<html><head><meta property="og:type" content="product"></head><body><h1>خودرو</h1><p>نتیجهٔ دقیقی پیدا نشد</p></body></html>`,
			models.PageTypeListing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classify(t, c.url, c.page)
			if got.Type != c.want {
				t.Errorf("type = %s, want %s (%v %v)", got.Type, c.want, got.Scores, got.Signals)
			}
		})
	}
}

// A listing with one result has a single Product in its JSON-LD, but the
// product's url is another page: it is still a listing.
func TestClassifyItemElsewhere(t *testing.T) {
	page := `<html><head><script type="application/ld+json">[{"@context":"https://schema.org","@type":"Car","name":"فورد","url":"https://divar.ir/v/ford-edge/gaijhk7S","offers":{"@type":"Offer","price":1}}]</script></head>
	<body><h1>فورد در کرج</h1></body></html>`
	if got := classify(t, "https://divar.ir/s/karaj/car/ford", page); got.Type != models.PageTypeListing {
		t.Errorf("listing with one result: %s %v", got.Type, got.Scores)
	}
	// The ad itself: same id segment, a slightly different slug.
	page = strings.Replace(page, "https://divar.ir/v/ford-edge/gaijhk7S", "https://divar.ir/v/ford-edge-/gaijhk7S", 1)
	if got := classify(t, "https://divar.ir/v/ford-edge/gaijhk7S", page); got.Type != models.PageTypeProduct {
		t.Errorf("ad page: %s %v", got.Type, got.Scores)
	}
}

func TestTemplateIndex(t *testing.T) {
	idx := NewTemplateIndex()
	shell := func(body string) string {
		return `<html><body><header class="site-header"><nav class="menu"><a href="/">خانه</a></nav></header>` + body +
			`<footer class="site-footer">©</footer></body></html>`
	}
	product := func(i int, buy bool) string {
		btn := ""
		if buy {
			btn = `<button class="add-to-cart">افزودن به سبد</button>`
		}
		return shell(fmt.Sprintf(`<div class="product-gallery"><img src="/%d.jpg"></div><div class="product-summary"><h1>محصول %d</h1><div class="product-specs">مشخصات</div>%s<div class="product-price">%d تومان</div></div>`, i, i, btn, 1000+i))
	}
	listing := func(i int) string {
		return shell(`<h1>دسته</h1><div class="category-filters">فیلتر</div>` + productCards(20, false))
	}
	// Learn from pages the classifier is sure about.
	for i := 0; i < 6; i++ {
		for _, page := range []string{product(i, true), listing(i)} {
			p, _ := ParsePage(fmt.Sprintf("https://shop.ir/%d", i), []byte(page))
			p.ClassifyWith(idx)
		}
	}
	// Out of stock: no buy button, so its own signals are weak.
	p, _ := ParsePage("https://shop.ir/99", []byte(product(99, false)))
	if own := p.Classify(); own.Confidence == ConfidenceHigh {
		t.Fatalf("test page should be weak on its own: %+v", own)
	}
	got := p.ClassifyWith(idx)
	if got.Type != models.PageTypeProduct || !strings.HasPrefix(got.Signals[len(got.Signals)-1], "template:product") {
		t.Errorf("template match: %+v", got)
	}
}

// Each page runs the detector of its type: a product page whose theme also
// marks it up as an Article yields the product only.
func TestDetectRoutesByPageType(t *testing.T) {
	page := `<html><head><script type="application/ld+json">[
	 {"@context":"https://schema.org","@type":"Product","name":"چراغ","offers":{"@type":"Offer","price":"1200000","priceCurrency":"IRR"}},
	 {"@context":"https://schema.org","@type":"Article","headline":"چراغ"}]</script></head>
	<body><h1>چراغ</h1><button>افزودن به سبد</button></body></html>`
	all := map[string]bool{models.FormatProduct: true, models.FormatArticle: true}
	d, err := Detect("https://shop.ir/lamp", []byte(page), all)
	if err != nil {
		t.Fatal(err)
	}
	if d.Product == nil || d.Article != nil {
		t.Errorf("product page: product=%v article=%v", d.Product != nil, d.Article != nil)
	}
	// Listings get no product: their items are other pages.
	p, _ := ParsePage("https://shop.ir/lamps", []byte(page))
	if d := p.Detect(all, models.PageTypeListing); d.Product != nil || d.Article != nil {
		t.Errorf("listing got a main detector result")
	}
	// Unknown type: every requested detector runs.
	if d := p.Detect(all, models.PageTypeOther); d.Product == nil || d.Article == nil {
		t.Errorf("other page should run every detector")
	}
}

// A product page without structured data is read from its HTML.
func TestProductFromHTML(t *testing.T) {
	page := `<html><head><meta property="og:image" content="https://shop.ir/lamp.jpg"></head><body>
	<header><a href="/cart">سبد خرید ۰ تومان</a></header>
	<nav class="breadcrumb"><a href="/">خانه</a><a href="/garden">چراغ باغی</a></nav>
	<h1>چراغ باغی ۱۳ وات</h1>
	<div class="prices"><del>۱,۵۰۰,۰۰۰ تومان</del><span class="price">۱,۲۰۰,۰۰۰ تومان</span></div>
	<button class="add-to-cart">افزودن به سبد خرید</button>
	` + productCards(5, true) + `</body></html>`
	p, _ := ParsePage("https://www.lighthome.ir/garden-light/12554-x.html", []byte(page))
	c := p.Classify()
	if c.Type != models.PageTypeProduct {
		t.Fatalf("type = %s %v", c.Type, c.Scores)
	}
	d := p.Detect(map[string]bool{models.FormatProduct: true}, c.Type)
	pr := d.Product
	if pr == nil {
		t.Fatal("no product")
	}
	if pr.Name != "چراغ باغی ۱۳ وات" || pr.Price == nil || pr.Price.Amount != 1200000 || pr.Price.Currency != "IRT" {
		t.Errorf("name/price = %q %+v", pr.Name, pr.Price)
	}
	if pr.RegularPrice == nil || pr.RegularPrice.Amount != 1500000 || pr.DiscountPercent != 20 {
		t.Errorf("regular = %+v discount %d", pr.RegularPrice, pr.DiscountPercent)
	}
	if pr.Availability != "in_stock" || pr.Image != "https://shop.ir/lamp.jpg" || len(pr.Breadcrumbs) != 2 {
		t.Errorf("availability %q image %q crumbs %v", pr.Availability, pr.Image, pr.Breadcrumbs)
	}
}
