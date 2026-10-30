package content

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/modules"
)

const productPage = `<!doctype html>
<html lang="fa" dir="rtl"><head>
<title>گوشی موبایل | فروشگاه</title>
<meta name="description" content="خرید گوشی">
<meta property="og:image" content="/img/p.jpg">
<meta property="article:published_time" content="2024-05-01T10:00:00Z">
<link rel="canonical" href="/p/1">
<script type="application/ld+json">{"@type":"Product","name":"Phone"}</script>
</head><body>
<nav><a href="/">خانه</a><a href="/cat">دسته</a></nav>
<article>
 <h1 class="title">گوشي موبايل سامسونگ</h1>
 <span class="price">۱۲٬۵۰۰٬۰۰۰ تومان</span>
 <span class="old">قیمت قبلی: 15,000,000 ریال</span>
 <time datetime="1403/02/15">۱۵ اردیبهشت ۱۴۰۳</time>
 <a class="more" href="/p/2?x=1">بیشتر</a>
 <ul class="specs"><li><b>رنگ</b><i>مشکی</i></li><li><b>وزن</b><i>۱۸۰ گرم</i></li></ul>
 <p>` + "Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit." + `</p>
</article>
<footer>کپی‌رایت</footer>
<script>var x = 1;</script>
</body></html>`

func TestAnalyze(t *testing.T) {
	res, err := Analyze("https://shop.example.ir/p/1", []byte(productPage), Options{Markdown: true, Text: true, Links: true, Metadata: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "گوشی موبایل | فروشگاه" {
		t.Errorf("title %q", res.Title)
	}
	md := res.Markdown
	if !strings.Contains(md, "گوشي موبايل سامسونگ") || strings.Contains(md, "var x") {
		t.Errorf("markdown missing heading or contains script:\n%s", md)
	}
	if !strings.Contains(md, "https://shop.example.ir/p/2?x=1") {
		t.Errorf("links in markdown not absolute:\n%s", md)
	}
	if res.Metadata["canonical"] != "https://shop.example.ir/p/1" || res.Metadata["language"] != "fa" {
		t.Errorf("metadata %v", res.Metadata)
	}
	if res.Metadata["image"] != "/img/p.jpg" && res.Metadata["image"] != "https://shop.example.ir/img/p.jpg" {
		t.Errorf("og image %v", res.Metadata["image"])
	}
	if _, ok := res.Metadata["json_ld"]; !ok {
		t.Error("json_ld missing")
	}
	if res.Metadata["published_at"] != "2024-05-01T10:00:00Z" {
		t.Errorf("published_at %v", res.Metadata["published_at"])
	}
	if !regexp.MustCompile(`تومان\s+قیمت`).MatchString(md) {
		t.Errorf("inline spans lost their separating space:\n%s", md)
	}
	if len(res.Links) != 3 {
		t.Errorf("links %v", res.Links)
	}

	main, _ := Analyze("https://shop.example.ir/p/1", []byte(productPage), Options{Markdown: true, OnlyMainContent: true})
	if strings.Contains(main.Markdown, "کپی‌رایت") || strings.Contains(main.Markdown, "دسته") {
		t.Errorf("main content still has chrome:\n%s", main.Markdown)
	}
	if main.ContentHash == "" {
		t.Error("empty content hash")
	}
}

func TestHashTextIgnoresCosmeticChanges(t *testing.T) {
	if HashText("قیمت  ۱۲۰ تومان") != HashText("قيمت 120 تومان") {
		t.Error("hash should ignore Arabic yeh, digit script and whitespace")
	}
	if HashText("a") == HashText("b") {
		t.Error("different text, same hash")
	}
}

func TestExtract(t *testing.T) {
	fields := []models.ExtractField{
		{Name: "title", Selector: "h1.title"},
		{Name: "price", Selector: ".price", Type: "price"},
		{Name: "old_price", Selector: ".old", Type: "number"},
		{Name: "date", Selector: "time", Type: "date"},
		{Name: "next", Selector: "a.more", Type: "url"},
		{Name: "next_xpath", Selector: "xpath://a[@class='more']/@href", Type: "url"},
		{Name: "has_specs", Selector: "ul.specs", Type: "exists"},
		{Name: "missing", Selector: ".nope"},
		{Name: "links", Selector: "nav a", Multiple: true},
		{Name: "weight_grams", Selector: "xpath://li[b='وزن']/i", Type: "number"},
		{Name: "specs", Selector: "ul.specs li", Type: "list", Fields: []models.ExtractField{
			{Name: "key", Selector: "b"},
			{Name: "value", Selector: "i"},
		}},
		{Name: "brand", Selector: "h1", Regex: `موبايل\s+(\S+)`},
	}
	if err := ValidateFields(fields); err != nil {
		t.Fatal(err)
	}
	out, err := Extract("https://shop.example.ir/p/1", []byte(productPage), fields)
	if err != nil {
		t.Fatal(err)
	}
	if out["title"] != "گوشي موبايل سامسونگ" {
		t.Errorf("title %v", out["title"])
	}
	if p, ok := out["price"].(modules.Price); !ok || p.Amount != 12500000 || p.Currency != "IRT" {
		t.Errorf("price %#v", out["price"])
	}
	if out["old_price"] != 15000000.0 {
		t.Errorf("old_price %v", out["old_price"])
	}
	if d, _ := out["date"].(string); !strings.HasPrefix(d, "2024-05-04") {
		t.Errorf("date %v", out["date"])
	}
	if out["next"] != "https://shop.example.ir/p/2?x=1" || out["next_xpath"] != out["next"] {
		t.Errorf("next %v / %v", out["next"], out["next_xpath"])
	}
	if out["has_specs"] != true || out["missing"] != nil {
		t.Errorf("exists/missing %v %v", out["has_specs"], out["missing"])
	}
	if l, _ := out["links"].([]interface{}); len(l) != 2 {
		t.Errorf("links %v", out["links"])
	}
	if out["weight_grams"] != 180.0 {
		t.Errorf("weight %v", out["weight_grams"])
	}
	specs, _ := out["specs"].([]interface{})
	if len(specs) != 2 || specs[1].(map[string]interface{})["value"] != "۱۸۰ گرم" {
		t.Errorf("specs %v", out["specs"])
	}
	if out["brand"] != "سامسونگ" {
		t.Errorf("brand %v", out["brand"])
	}
	b, _ := json.Marshal(out)
	if !json.Valid(b) {
		t.Error("result is not JSON-serializable")
	}
}

func TestValidateFields(t *testing.T) {
	bad := [][]models.ExtractField{
		{{Name: "", Selector: "a"}},
		{{Name: "a", Selector: ""}},
		{{Name: "a", Selector: "a"}, {Name: "a", Selector: "b"}},
		{{Name: "a", Selector: "a", Type: "weird"}},
		{{Name: "a", Selector: "a", Type: "attr"}},
		{{Name: "a", Selector: "xpath://a[", Type: "text"}},
		{{Name: "a", Selector: "a[[["}},
		{{Name: "a", Selector: "a", Regex: "("}},
		{{Name: "a", Selector: "a", Type: "list"}},
	}
	for i, f := range bad {
		if err := ValidateFields(f); err == nil {
			t.Errorf("case %d accepted: %+v", i, f)
		}
	}
}

func TestLLMExtract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad", 400)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Sure!\n` + "```json" + `\n{\"name\":\"Phone\",\"price\":12500000}\n` + "```" + `"}}],"usage":{"total_tokens":321}}`))
	}))
	defer srv.Close()
	c := NewLLMClient(config.LLMConfig{BaseURL: srv.URL + "/v1/", APIKey: "k", Model: "m"})
	out, tokens, err := c.Extract(context.Background(), "get product", `{"name":"string"}`, "https://x", "page")
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 321 || string(out) != `{"name":"Phone","price":12500000}` {
		t.Fatalf("got %s tokens=%d", out, tokens)
	}
	if NewLLMClient(config.LLMConfig{}) != nil {
		t.Fatal("unconfigured client should be nil")
	}
	var nilClient *LLMClient
	if _, _, err := nilClient.Extract(context.Background(), "", "", "", ""); err != ErrLLMDisabled {
		t.Fatal("nil client must return ErrLLMDisabled")
	}
}

const jsonLDPage = `<html><head>
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[
 {"@type":"WebPage","name":"page"},
 {"@type":["Product","Thing"],"name":"Galaxy &amp; Co","sku":12345,
  "image":["https://cdn.x.ir/1.jpg","https://cdn.x.ir/2.jpg"],
  "offers":[{"@type":"Offer","price":"12500000","priceCurrency":"IRT","availability":"https://schema.org/InStock"},
            {"@type":"Offer","price":13000000,"priceCurrency":"IRR"}]}]}</script>
<script type="application/ld+json">{ not json }</script>
<script type="APPLICATION/LD+JSON; charset=utf-8">{"@type":"http://schema.org/BreadcrumbList","itemListElement":[{"name":"خانه"},{"name":"موبایل"}]}</script>
</head><body><h1>x</h1></body></html>`

func TestExtractJSONLD(t *testing.T) {
	fields := []models.ExtractField{
		{Name: "name", Selector: "jsonld:Product.name"},
		{Name: "sku", Selector: "jsonld:product.sku", Type: "number"},
		{Name: "price", Selector: "jsonld:Product.offers.price", Type: "price"},
		{Name: "prices", Selector: "jsonld:Product.offers.price", Type: "price", Multiple: true},
		{Name: "image", Selector: "jsonld:Product.image", Type: "url"},
		{Name: "in_stock", Selector: "jsonld:Offer.availability", Regex: `(InStock)`},
		{Name: "crumbs", Selector: "jsonld:BreadcrumbList.itemListElement.name", Multiple: true},
		{Name: "has_product", Selector: "jsonld:Product.name", Type: "exists"},
		{Name: "missing", Selector: "jsonld:Recipe.name"},
	}
	if err := ValidateFields(fields); err != nil {
		t.Fatal(err)
	}
	out, err := Extract("https://shop.example.ir/p/1", []byte(jsonLDPage), fields)
	if err != nil {
		t.Fatal(err)
	}
	if out["name"] != "Galaxy & Co" || out["sku"] != 12345.0 || out["image"] != "https://cdn.x.ir/1.jpg" {
		t.Errorf("name/sku/image: %v %v %v", out["name"], out["sku"], out["image"])
	}
	if p, ok := out["price"].(modules.Price); !ok || p.Amount != 12500000 || p.Currency != "IRT" {
		t.Errorf("price %#v", out["price"])
	}
	prices, _ := out["prices"].([]interface{})
	if len(prices) != 2 || prices[1].(modules.Price).Currency != "IRR" || prices[1].(modules.Price).Amount != 13000000 {
		t.Errorf("prices %#v", out["prices"])
	}
	if out["in_stock"] != "InStock" || out["has_product"] != true || out["missing"] != nil {
		t.Errorf("in_stock/has/missing: %v %v %v", out["in_stock"], out["has_product"], out["missing"])
	}
	if c, _ := out["crumbs"].([]interface{}); len(c) != 2 || c[1] != "موبایل" {
		t.Errorf("crumbs %v", out["crumbs"])
	}
	for _, bad := range [][]models.ExtractField{
		{{Name: "a", Selector: "jsonld:Product"}},
		{{Name: "a", Selector: "jsonld:Product..name"}},
		{{Name: "a", Selector: "jsonld:Product.name", Type: "html"}},
	} {
		if err := ValidateFields(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestDiffExtracted(t *testing.T) {
	prev := models.JSONRaw(`{"price":{"amount":100,"currency":"IRT","raw":"۱۰۰ تومان"},"title":"A","stock":null}`)
	same := models.JSONRaw(`{"price":{"amount":100,"currency":"IRT","raw":"100 IRT"},"title":"A","stock":null}`)
	if d := DiffExtracted(prev, same); d != nil {
		t.Errorf("cosmetic price change reported: %+v", d)
	}
	cur := models.JSONRaw(`{"price":{"amount":120,"currency":"IRT","raw":"120"},"title":"A","stock":true,"new":1}`)
	d := DiffExtracted(prev, cur)
	if len(d) != 3 || d[0].Name != "new" || d[1].Name != "price" || d[2].Name != "stock" || string(d[2].Old) != "null" || string(d[2].New) != "true" {
		t.Fatalf("diff = %+v", d)
	}
	if DiffExtracted(nil, cur) != nil || DiffExtracted(prev, models.JSONRaw("null")) != nil {
		t.Error("missing side should not report changes")
	}
}

func TestFallbackSelectors(t *testing.T) {
	page := `<html><body><span class="p">۱۲۰ هزار تومان</span><div class="tags"></div></body></html>`
	fields := []models.ExtractField{
		{Name: "price", Selector: "jsonld:Product.offers.price", Type: "price", FallbackSelectors: []string{".missing", ".p"}},
		{Name: "tags", Selector: ".tags a", Multiple: true, FallbackSelectors: []string{"xpath://div[@class='tags']/@class"}},
		{Name: "has", Selector: ".none", Type: "exists", FallbackSelectors: []string{"span.p"}},
	}
	if err := ValidateFields(fields); err != nil {
		t.Fatal(err)
	}
	out, err := Extract("https://x.ir/", []byte(page), fields)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := out["price"].(modules.Price); !ok || p.Amount != 120000 || p.Currency != "IRT" {
		t.Errorf("price %#v", out["price"])
	}
	if tags, _ := out["tags"].([]interface{}); len(tags) != 1 || tags[0] != "tags" {
		t.Errorf("tags %#v", out["tags"])
	}
	if out["has"] != true {
		t.Errorf("has %v", out["has"])
	}
	bad := []models.ExtractField{{Name: "a", Selector: "h1", FallbackSelectors: []string{"xpath://a["}}}
	if ValidateFields(bad) == nil {
		t.Error("invalid fallback accepted")
	}
}
