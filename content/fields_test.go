package content

import (
	"testing"

	"github.com/resolver/crawler/modules"
)

func TestProductFieldValue(t *testing.T) {
	p := &Product{
		Price:        &modules.Price{Amount: 1200000, Currency: "IRT"},
		RegularPrice: &modules.Price{Amount: 1500000, Currency: "IRT"},
		Availability: "in_stock",
		Rating:       4.2,
		ReviewCount:  31,
		Brand:        "Samsung",
	}
	if v, ok := ProductFieldValue(p, nil, "price"); !ok || v.Num == nil || *v.Num != 1200000 || v.Currency != "IRT" {
		t.Errorf("price = %+v %v", v, ok)
	}
	if v, ok := ProductFieldValue(p, nil, "availability"); !ok || v.Text != "in_stock" {
		t.Errorf("availability = %+v %v", v, ok)
	}
	if v, ok := ProductFieldValue(p, nil, "review_count"); !ok || v.Num == nil || *v.Num != 31 {
		t.Errorf("review_count = %+v %v", v, ok)
	}
	// A field the product does not carry yields nothing, rather than a zero.
	if _, ok := ProductFieldValue(p, nil, "seller"); ok {
		t.Errorf("an empty seller should not produce a value")
	}
	if _, ok := ProductFieldValue(p, nil, "not_a_field"); ok {
		t.Errorf("an unknown field should not produce a value")
	}
	if _, ok := ProductFieldValue(nil, nil, "price"); ok {
		t.Errorf("a nil product should not produce a value")
	}
}

func TestExtractedFieldValue(t *testing.T) {
	raw := []byte(`{"warranty":"18 months","stock":"1,250","score":4.5,"active":true,"blank":""}`)
	if v, ok := ExtractedFieldValue(raw, "warranty"); !ok || v.Text != "18 months" || v.Num != nil {
		t.Errorf("text field = %+v %v", v, ok)
	}
	if v, ok := ExtractedFieldValue(raw, "stock"); !ok || v.Num == nil || *v.Num != 1250 {
		t.Errorf("grouped number = %+v %v", v, ok)
	}
	if v, ok := ExtractedFieldValue(raw, "score"); !ok || v.Num == nil || *v.Num != 4.5 {
		t.Errorf("json number = %+v %v", v, ok)
	}
	if v, ok := ExtractedFieldValue(raw, "active"); !ok || v.Text != "true" {
		t.Errorf("bool = %+v %v", v, ok)
	}
	if _, ok := ExtractedFieldValue(raw, "blank"); ok {
		t.Errorf("an empty string should not produce a value")
	}
	if _, ok := ExtractedFieldValue(raw, "missing"); ok {
		t.Errorf("a missing key should not produce a value")
	}
	if _, ok := ExtractedFieldValue(nil, "warranty"); ok {
		t.Errorf("no extracted JSON should not produce a value")
	}
}

func TestFieldKindOf(t *testing.T) {
	if FieldKindOf("price") != FieldKindMoney || !IsMoneyField("regular_price") {
		t.Errorf("price fields should be money")
	}
	if FieldKindOf("availability") != FieldKindText || FieldKindOf("rating") != FieldKindNumber {
		t.Errorf("unexpected kinds")
	}
	if FieldKindOf("my_extract_field") != "" {
		t.Errorf("an unknown field should have no kind")
	}
}
