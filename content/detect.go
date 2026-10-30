package content

import (
	"bytes"
	"encoding/json"
	"net/url"

	"github.com/resolver/crawler/models"
	"golang.org/x/net/html"
)

// Detected holds what the detectors found on a page. Each detector is an
// output format (models.FormatProduct, …); a field is nil when its format was
// not requested or the page has no such thing.
type Detected struct {
	Product    *Product    `json:"product,omitempty"`
	Article    *Article    `json:"article,omitempty"`
	Business   *Business   `json:"business,omitempty"`
	JobPosting *JobPosting `json:"job_posting,omitempty"`
	Event      *Event      `json:"event,omitempty"`
	FAQ        []FAQItem   `json:"faq,omitempty"`
}

// DetectorInfo describes a detector for UIs and API clients (GET /v1/info).
type DetectorInfo struct {
	Format      string   `json:"format"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Fields      []string `json:"fields"` // the main fields returned
}

// Detectors lists the detectors in the order UIs show them.
var Detectors = []DetectorInfo{
	{models.FormatProduct, "Product", "Shop product pages: price, discount and stock without selectors.",
		[]string{"name", "price", "regular_price", "discount_percent", "availability", "sku", "gtin", "brand", "images", "category", "breadcrumbs", "rating", "seller"}},
	{models.FormatArticle, "Article", "News stories and blog posts.",
		[]string{"headline", "authors", "published_at", "modified_at", "section", "keywords", "publisher", "image", "language"}},
	{models.FormatBusiness, "Business & contact", "The shop, company or office behind the page, as it publishes itself.",
		[]string{"name", "type", "telephones", "emails", "address", "geo", "opening_hours", "social_links", "logo", "rating"}},
	{models.FormatJobPosting, "Job posting", "Job ads on job boards and careers pages.",
		[]string{"title", "organization", "location", "remote", "employment_types", "salary", "date_posted", "valid_through"}},
	{models.FormatEvent, "Event", "Concerts, conferences, webinars, exhibitions, courses.",
		[]string{"name", "start_date", "end_date", "status", "attendance_mode", "venue", "address", "organizer", "price"}},
	{models.FormatFAQ, "FAQ", "Questions and answers from FAQ sections.",
		[]string{"question", "answer"}},
}

// DetectFormats returns the requested formats that are detectors.
func DetectFormats(formats map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, d := range Detectors {
		if formats[d.Format] {
			out[d.Format] = true
		}
	}
	return out
}

// Detect runs the detectors named in formats on an HTML page. The page is
// parsed once for all of them.
func Detect(pageURL string, body []byte, formats map[string]bool) (*Detected, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(pageURL)
	pg := newPageData(root, base)
	d := &Detected{}
	if formats[models.FormatProduct] {
		d.Product = detectProduct(pg)
	}
	if formats[models.FormatArticle] {
		d.Article = detectArticle(pg)
	}
	if formats[models.FormatBusiness] {
		d.Business = detectBusiness(pg)
	}
	if formats[models.FormatJobPosting] {
		d.JobPosting = detectJobPosting(pg)
	}
	if formats[models.FormatEvent] {
		d.Event = detectEvent(pg)
	}
	if formats[models.FormatFAQ] {
		d.FAQ = detectFAQ(pg)
	}
	return d, nil
}

// IsEmpty reports whether nothing was detected.
func (d *Detected) IsEmpty() bool {
	return d.Product == nil && d.Article == nil && d.Business == nil && d.JobPosting == nil && d.Event == nil && len(d.FAQ) == 0
}

// Store writes what was detected into a PageContent row's columns.
func (d *Detected) Store(dst *models.DetectedData) error {
	for format, v := range map[string]interface{}{
		models.FormatProduct: d.Product, models.FormatArticle: d.Article, models.FormatBusiness: d.Business,
		models.FormatJobPosting: d.JobPosting, models.FormatEvent: d.Event, models.FormatFAQ: d.FAQ,
	} {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if string(raw) == "null" {
			raw = nil
		}
		dst.Set(format, models.JSONRaw(raw))
	}
	return nil
}
