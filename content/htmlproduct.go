package content

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/resolver/crawler/modules"
)

// productFromHTML reads a product page that has no structured data or known
// shop platform: the name from the main heading, the price shown outside item
// cards (related-product cards and the header's cart are skipped) and stock
// from the buy or notify button.
func (p *Page) productFromHTML() *Product {
	l := p.lay()
	pg := p.pg
	name := l.h1
	if name == "" {
		name = cleanInline(pg.metaFirst("og:title"))
	}
	if name == "" {
		return nil
	}
	out := &Product{Name: truncateRunes(name, 300), Sources: map[string]string{"name": SourceHTML}}
	for _, pt := range l.prices {
		pr, ok := modules.ParsePrice(pt.text)
		if !ok || pr.Amount <= 0 {
			continue
		}
		switch {
		case pt.old && out.RegularPrice == nil:
			out.RegularPrice = &pr
			out.Sources["regular_price"] = SourceHTML
		case !pt.old && out.Price == nil:
			out.Price = &pr
			out.Sources["price"] = SourceHTML
		}
		if out.Price != nil && out.RegularPrice != nil {
			break
		}
	}
	if out.RegularPrice != nil && (out.Price == nil || !samePriceUnit(out.Price, out.RegularPrice)) {
		out.RegularPrice = nil
		delete(out.Sources, "regular_price")
	}
	switch {
	case l.buyButtons > 0:
		out.Availability = "in_stock"
	case l.notifyStock:
		out.Availability = "out_of_stock"
	}
	if out.Availability != "" {
		out.Sources["availability"] = SourceHTML
	}
	if img := resolveAny(pg.base, pg.metaFirst("og:image:secure_url", "og:image", "og:image:url")); img != "" {
		out.Image, out.Images = img, []string{img}
		out.Sources["image"] = SourceOpenGraph
	}
	if d := cleanInline(pg.metaFirst("og:description", "description")); d != "" {
		out.Description = truncateRunes(d, maxDescription)
		out.Sources["description"] = SourceMeta
	}
	if crumbs := htmlBreadcrumbs(pg.doc); len(crumbs) > 0 {
		out.Breadcrumbs = crumbs
		out.Sources["breadcrumbs"] = SourceHTML
	} else {
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

// htmlBreadcrumbs reads a breadcrumb trail from the page markup.
func htmlBreadcrumbs(doc *goquery.Document) []string {
	var out []string
	doc.Find(`nav[aria-label*="breadcrumb" i], [class*="breadcrumb"]`).First().Find("a, li > span, [aria-current]").Each(func(_ int, s *goquery.Selection) {
		if t := strings.Join(strings.Fields(s.Text()), " "); t != "" && len(out) < 12 && (len(out) == 0 || out[len(out)-1] != t) {
			out = append(out, t)
		}
	})
	if len(out) < 2 {
		return nil
	}
	return out
}
