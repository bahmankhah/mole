package content

import (
	"regexp"
	"sort"
	"strings"

	"github.com/resolver/crawler/modules"
)

// Variant attribute keys recognised in a variant's display name.
const (
	AttrColor   = "color"
	AttrStorage = "storage"
	AttrRAM     = "ram"
	AttrSize    = "size"
)

// Word boundaries are spelled out as (^|\s)…(\s|$) rather than \b: \b is an
// ASCII word boundary, so it does not reliably bracket Persian letters.
var (
	ramRe     = regexp.MustCompile(`(?i)(^|\s)(\d+)\s?(gb|گیگ)\s*(ram|رم)(\s|$)`)
	storageRe = regexp.MustCompile(`(?i)(^|\s)(\d+)\s?(tb|gb|ترابایت|گیگابایت|گیگ)(\s|$)`)
	sizeRe    = regexp.MustCompile(`(?i)(^|\s)(xxs|xs|small|s|medium|m|large|l|extra large|xl|xxl|xxxl)(\s|$)`)
)

// sizeWords canonicalizes a clothing-size word or abbreviation to its
// single-token form ("medium" and "m" both → "m").
var sizeWords = map[string]string{
	"xxs": "xxs", "xs": "xs", "small": "s", "s": "s", "medium": "m", "m": "m",
	"large": "l", "l": "l", "extra large": "xl", "xl": "xl", "xxl": "xxl", "xxxl": "xxxl",
}

// colorWords maps an English or Persian color word to one canonical English
// token, so "سیاه" and "black" resolve to the same attribute value.
var colorWords = map[string]string{
	"black": "black", "سیاه": "black", "مشکی": "black",
	"white": "white", "سفید": "white",
	"red": "red", "قرمز": "red",
	"blue": "blue", "آبی": "blue",
	"green": "green", "سبز": "green",
	"yellow": "yellow", "زرد": "yellow",
	"gray": "gray", "grey": "gray", "خاکستری": "gray", "طوسی": "gray",
	"silver": "silver", "نقره‌ای": "silver", "نقره ای": "silver",
	"gold": "gold", "طلایی": "gold",
	"pink": "pink", "صورتی": "pink",
	"purple": "purple", "بنفش": "purple",
	"orange": "orange", "نارنجی": "orange",
	"brown": "brown", "قهوه‌ای": "brown", "قهوه ای": "brown",
	"navy": "navy", "سرمه‌ای": "navy", "سرمه ای": "navy",
	"titanium": "titanium", "تیتانیوم": "titanium",
	"beige": "beige",
}

// ParseVariantAttributes reads the purchasable attributes (colour, storage
// size, RAM, clothing size) out of a variant's display name, e.g.
// "iPhone 15 Pro 256GB Black" → {storage: "256gb", color: "black"}. Grouping
// matches on these, not the raw name, so the same variant is recognised
// across sites that word it differently.
func ParseVariantAttributes(name string) map[string]string {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	out := map[string]string{}
	norm := " " + modules.NormalizePersian(strings.ToLower(name)) + " "
	if m := ramRe.FindStringSubmatch(norm); m != nil {
		out[AttrRAM] = m[2] + "gb"
		norm = ramRe.ReplaceAllString(norm, " ") // consumed, so it isn't also read as storage
	}
	if m := storageRe.FindStringSubmatch(norm); m != nil {
		unit := "gb"
		switch strings.ToLower(m[3]) {
		case "tb", "ترابایت":
			unit = "tb"
		}
		out[AttrStorage] = m[2] + unit
	}
	for word, canon := range colorWords {
		if strings.Contains(norm, word) {
			out[AttrColor] = canon
			break
		}
	}
	if m := sizeRe.FindStringSubmatch(norm); m != nil {
		out[AttrSize] = sizeWords[strings.ToLower(m[2])]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// VariantKey turns a variant's attributes into a stable, sorted string, e.g.
// {"color":"black","storage":"128gb"} → "color:black|storage:128gb". Two
// pages whose variants parse to the same key are the same purchasable
// variant, however each site worded it.
func VariantKey(attrs map[string]string) string {
	if len(attrs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+":"+attrs[k])
	}
	return strings.Join(parts, "|")
}
