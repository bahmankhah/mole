package content

import "testing"

func TestParseVariantAttributes(t *testing.T) {
	cases := []struct {
		name string
		want map[string]string
	}{
		{"128GB Black", map[string]string{AttrStorage: "128gb", AttrColor: "black"}},
		{"مشکی 256 گیگابایت", map[string]string{AttrStorage: "256gb", AttrColor: "black"}},
		{"8GB RAM / 128GB Blue", map[string]string{AttrRAM: "8gb", AttrStorage: "128gb", AttrColor: "blue"}},
		{"Medium", map[string]string{AttrSize: "m"}},
		{"", nil},
		{"Deluxe Edition", nil},
	}
	for _, c := range cases {
		got := ParseVariantAttributes(c.name)
		if len(got) != len(c.want) {
			t.Errorf("ParseVariantAttributes(%q) = %v, want %v", c.name, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("ParseVariantAttributes(%q)[%q] = %q, want %q", c.name, k, got[k], v)
			}
		}
	}
}

func TestVariantKeyIsSortedAndStable(t *testing.T) {
	a := VariantKey(map[string]string{"color": "black", "storage": "128gb"})
	b := VariantKey(map[string]string{"storage": "128gb", "color": "black"})
	if a != b {
		t.Errorf("VariantKey not order-independent: %q vs %q", a, b)
	}
	if a != "color:black|storage:128gb" {
		t.Errorf("VariantKey = %q", a)
	}
	if VariantKey(nil) != "" {
		t.Errorf("VariantKey(nil) should be empty")
	}
}
