package content

import (
	"encoding/json"
	"reflect"
	"sort"

	"github.com/resolver/crawler/models"
)

// DiffExtracted lists the extracted fields whose value differs between a
// baseline page and the same page now. It returns nil when either side has
// no extracted data. Prices compare by amount and currency, not their raw text.
func DiffExtracted(prev, cur models.JSONRaw) []models.FieldChange {
	a, b := decodeObjects(prev, cur)
	if a == nil || b == nil {
		return nil
	}
	return diffObjects(a, b, "")
}

// DiffDetected lists how a detector's result (format "product", "article",
// …) differs between a baseline page and the same page now, with fields
// named "<format>.<field>" ("product.price"). Lists such as the FAQ are
// compared as a whole ("faq"). It returns nil unless both runs detected
// something. Where a value was found (sources) is not compared.
func DiffDetected(format string, prev, cur models.JSONRaw) []models.FieldChange {
	if len(prev) == 0 || len(cur) == 0 {
		return nil
	}
	var a, b interface{}
	if json.Unmarshal(prev, &a) != nil || json.Unmarshal(cur, &b) != nil || a == nil || b == nil {
		return nil
	}
	am, aok := a.(map[string]interface{})
	bm, bok := b.(map[string]interface{})
	if aok && bok {
		delete(am, "sources")
		delete(bm, "sources")
		return diffObjects(am, bm, format+".")
	}
	if reflect.DeepEqual(a, b) {
		return nil
	}
	return []models.FieldChange{{Name: format, Old: json.RawMessage(prev), New: json.RawMessage(cur)}}
}

func decodeObjects(prev, cur models.JSONRaw) (map[string]interface{}, map[string]interface{}) {
	if len(prev) == 0 || len(cur) == 0 {
		return nil, nil
	}
	var a, b map[string]interface{}
	if json.Unmarshal(prev, &a) != nil || json.Unmarshal(cur, &b) != nil {
		return nil, nil
	}
	return a, b
}

func diffObjects(a, b map[string]interface{}, prefix string) []models.FieldChange {
	names := make([]string, 0, len(a)+len(b))
	for k := range a {
		names = append(names, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	var out []models.FieldChange
	for _, name := range names {
		if reflect.DeepEqual(comparableValue(a[name]), comparableValue(b[name])) {
			continue
		}
		oldRaw, err1 := json.Marshal(a[name])
		newRaw, err2 := json.Marshal(b[name])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, models.FieldChange{Name: prefix + name, Old: oldRaw, New: newRaw})
	}
	return out
}

// comparableValue drops the "raw" text of price objects so a cosmetic change
// in how a price is written is not reported as a price change.
func comparableValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		_, isPrice := t["amount"]
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if isPrice && k == "raw" {
				continue
			}
			out[k] = comparableValue(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = comparableValue(val)
		}
		return out
	}
	return v
}
