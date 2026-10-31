package content

import "testing"

func iranProductPage(jsonLD, body string) string {
	return `<html><head><script type="application/ld+json">` + jsonLD + `</script></head><body>` + body + `</body></html>`
}

func TestIranianCurrencyFromPageText(t *testing.T) {
	cases := []struct {
		name, jsonLD, body string
		amount             float64
		currency           string
	}{
		{
			// Divar labels toman amounts IRR; the page shows tomans.
			name:     "mislabelled IRR",
			jsonLD:   `{"@context":"https://schema.org","@type":"Product","name":"دیفرانسیل","offers":{"@type":"Offer","price":1500000,"priceCurrency":"IRR"}}`,
			body:     `<div><p>قیمت</p><p>` + "‏" + `۱,۵۰۰,۰۰۰ تومان</p></div>`,
			amount:   1500000,
			currency: "IRT",
		},
		{
			name:     "correct IRR shown in tomans",
			jsonLD:   `{"@context":"https://schema.org","@type":"Product","name":"x","offers":{"@type":"Offer","price":15000000,"priceCurrency":"IRR"}}`,
			body:     `<span>۱,۵۰۰,۰۰۰</span> <span>تومان</span>`,
			amount:   15000000,
			currency: "IRR",
		},
		{
			name:     "mislabelled IRT",
			jsonLD:   `{"@context":"https://schema.org","@type":"Product","name":"x","offers":{"@type":"Offer","price":250000,"priceCurrency":"IRT"}}`,
			body:     `<div class="price"><b>250,000</b><small>ریال</small></div>`,
			amount:   250000,
			currency: "IRR",
		},
		{
			name:     "no evidence keeps the label",
			jsonLD:   `{"@context":"https://schema.org","@type":"Product","name":"x","offers":{"@type":"Offer","price":1500000,"priceCurrency":"IRR"}}`,
			body:     `<p>ارسال رایگان</p><p>۹۹,۰۰۰ تومان</p>`,
			amount:   1500000,
			currency: "IRR",
		},
		{
			name:     "unlabelled price",
			jsonLD:   `{"@context":"https://schema.org","@type":"Product","name":"x","offers":{"@type":"Offer","price":"1500000"}}`,
			body:     `<p>۱٫۵ میلیون تومان</p>`,
			amount:   1500000,
			currency: "IRT",
		},
		{
			name:     "script text is ignored",
			jsonLD:   `{"@context":"https://schema.org","@type":"Product","name":"x","offers":{"@type":"Offer","price":1500000,"priceCurrency":"IRR"}}`,
			body:     `<script>var p = "1,500,000 تومان"</script>`,
			amount:   1500000,
			currency: "IRR",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := detect(t, "https://divar.ir/v/x/ga1dgMJW", iranProductPage(c.jsonLD, c.body))
			if p == nil {
				t.Fatal("no product")
			}
			wantPrice(t, "price", p, c.amount, c.currency)
		})
	}
}

// A mislabel found on the sale price applies to the regular price too.
func TestIranianCurrencyAppliesToAllPrices(t *testing.T) {
	ld := `{"@context":"https://schema.org","@type":"Product","name":"x","offers":[
	 {"@type":"Offer","price":900000,"priceCurrency":"IRR"},
	 {"@type":"Offer","price":1200000,"priceCurrency":"IRR"}]}`
	p := detect(t, "https://shop.ir/p/1", iranProductPage(ld, `<p>از ۹۰۰,۰۰۰ تومان</p>`))
	if p == nil {
		t.Fatal("no product")
	}
	wantPrice(t, "low_price", p, 900000, "IRT")
	wantPrice(t, "high_price", p, 1200000, "IRT")
	if p.Sources["currency"] != "page_text" {
		t.Errorf("sources[currency] = %q", p.Sources["currency"])
	}
}
