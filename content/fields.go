package content

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/resolver/crawler/modules"
)

// FieldValue is one comparable field read off a page: a number (price,
// rating, review count…), a text value (availability, seller…), or both
// when the text is a label for the number. Currency is set for money.
type FieldValue struct {
	Num      *float64 `json:"num,omitempty"`
	Text     string   `json:"text,omitempty"`
	Currency string   `json:"currency,omitempty"`
}

// FieldKind tells the UI how to render and compare a field.
const (
	FieldKindMoney  = "money"
	FieldKindNumber = "number"
	FieldKindText   = "text"
)

// ComparableField is one product field that grouping can track per run.
type ComparableField struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// ComparableFields are the detected product fields a group can compare.
// A group may also name any of its job's extract_fields, which are read
// from the page's extracted JSON instead.
var ComparableFields = []ComparableField{
	{"price", "Price", FieldKindMoney},
	{"regular_price", "Price before discount", FieldKindMoney},
	{"discount_percent", "Discount %", FieldKindNumber},
	{"low_price", "Lowest offer", FieldKindMoney},
	{"high_price", "Highest offer", FieldKindMoney},
	{"offer_count", "Number of offers", FieldKindNumber},
	{"availability", "Availability", FieldKindText},
	{"condition", "Condition", FieldKindText},
	{"seller", "Seller", FieldKindText},
	{"rating", "Rating", FieldKindNumber},
	{"review_count", "Reviews", FieldKindNumber},
	{"brand", "Brand", FieldKindText},
	{"sku", "SKU", FieldKindText},
	{"gtin", "Barcode (GTIN)", FieldKindText},
	{"mpn", "Model number (MPN)", FieldKindText},
	{"category", "Category", FieldKindText},
}

// DefaultCompareFields are tracked when a group names no fields of its own.
var DefaultCompareFields = []string{"price", "regular_price", "availability"}

// FieldKindOf returns the kind of a known comparable field, or "" when the
// name is not one (an extract field, whose kind is decided by its value).
func FieldKindOf(field string) string {
	for _, f := range ComparableFields {
		if f.Name == field {
			return f.Kind
		}
	}
	return ""
}

// IsMoneyField reports whether a field holds an amount with a currency.
func IsMoneyField(field string) bool { return FieldKindOf(field) == FieldKindMoney }

// ProductFieldValue reads one comparable field off a detected product. The
// variant, when given, wins for the fields a variant carries of its own
// (its price and availability differ per size/colour/storage).
func ProductFieldValue(p *Product, v *ProductVariant, field string) (FieldValue, bool) {
	if p == nil {
		return FieldValue{}, false
	}
	num := func(f float64) (FieldValue, bool) {
		if f == 0 {
			return FieldValue{}, false
		}
		return FieldValue{Num: &f}, true
	}
	text := func(s string) (FieldValue, bool) {
		if strings.TrimSpace(s) == "" {
			return FieldValue{}, false
		}
		return FieldValue{Text: s}, true
	}
	switch field {
	case "price":
		if v != nil && v.Price != nil {
			return money(v.Price)
		}
		return money(p.Price)
	case "regular_price":
		if v != nil && v.RegularPrice != nil {
			return money(v.RegularPrice)
		}
		return money(p.RegularPrice)
	case "low_price":
		return money(p.LowPrice)
	case "high_price":
		return money(p.HighPrice)
	case "discount_percent":
		return num(float64(p.DiscountPercent))
	case "offer_count":
		return num(float64(p.OfferCount))
	case "rating":
		return num(p.Rating)
	case "review_count":
		return num(float64(p.ReviewCount))
	case "availability":
		if v != nil && v.Availability != "" {
			return text(v.Availability)
		}
		return text(p.Availability)
	case "condition":
		return text(p.Condition)
	case "seller":
		return text(p.Seller)
	case "brand":
		return text(p.Brand)
	case "sku":
		if v != nil && v.SKU != "" {
			return text(v.SKU)
		}
		return text(p.SKU)
	case "gtin":
		if v != nil && v.GTIN != "" {
			return text(v.GTIN)
		}
		return text(p.GTIN)
	case "mpn":
		if v != nil && v.MPN != "" {
			return text(v.MPN)
		}
		return text(p.MPN)
	case "category":
		return text(p.Category)
	}
	return FieldValue{}, false
}

func money(p *modules.Price) (FieldValue, bool) {
	if p == nil || p.Amount == 0 {
		return FieldValue{}, false
	}
	amount := p.Amount
	return FieldValue{Num: &amount, Currency: p.Currency}, true
}

// ExtractedFieldValue reads a field the job's extract_fields (or an LLM
// schema) produced, from a page's extracted JSON. A value that parses as a
// number is comparable as one; anything else is compared as text.
func ExtractedFieldValue(raw []byte, field string) (FieldValue, bool) {
	if len(raw) == 0 || field == "" {
		return FieldValue{}, false
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return FieldValue{}, false
	}
	v, ok := obj[field]
	if !ok {
		return FieldValue{}, false
	}
	switch t := v.(type) {
	case float64:
		return FieldValue{Num: &t}, true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return FieldValue{}, false
		}
		// Prices and counts often arrive as "1,250,000" or "4.5".
		if n, err := strconv.ParseFloat(strings.NewReplacer(",", "", " ", "", "٬", "").Replace(s), 64); err == nil {
			return FieldValue{Num: &n, Text: s}, true
		}
		return FieldValue{Text: s}, true
	case bool:
		return FieldValue{Text: strconv.FormatBool(t)}, true
	}
	return FieldValue{}, false
}
