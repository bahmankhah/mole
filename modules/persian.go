package modules

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const zwnj = '\u200c'

// NormalizePersian canonicalizes Persian text for matching and display.
// It unifies Arabic Yeh/Kaf to their Persian forms, removes tatweel, diacritics
// and bidi control marks, cleans up ZWNJ usage and collapses whitespace.
// Digits and Latin text are left unchanged.
func NormalizePersian(s string) string {
	out := make([]rune, 0, len(s))
	last := func() rune {
		if len(out) == 0 {
			return 0
		}
		return out[len(out)-1]
	}
	for _, r := range s {
		switch {
		case r == 'ي' || r == 'ى':
			r = 'ی'
		case r == 'ك':
			r = 'ک'
		case r == '\u0640', r >= '\u064b' && r <= '\u065f', r == '\u0670':
			continue
		case r == '\u200e', r == '\u200f', r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069', r == '\ufeff':
			continue
		}

		if r == zwnj {
			if l := last(); l == 0 || l == ' ' || l == zwnj {
				continue
			}
			out = append(out, r)
			continue
		}
		if unicode.IsSpace(r) {
			if last() == zwnj {
				out = out[:len(out)-1]
			}
			if l := last(); l == 0 || l == ' ' {
				continue
			}
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	for len(out) > 0 && (out[len(out)-1] == ' ' || out[len(out)-1] == zwnj) {
		out = out[:len(out)-1]
	}
	return string(out)
}

// ToLatinDigits converts Persian (۰-۹) and Arabic-Indic (٠-٩) digits to ASCII,
// the Arabic decimal separator ٫ to '.', and the Arabic thousands separator ٬ to ','.
func ToLatinDigits(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		case r == '٫':
			return '.'
		case r == '٬':
			return ','
		}
		return r
	}, s)
}

// ToPersianDigits converts ASCII digits to Persian digits.
func ToPersianDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '۰' + (r - '0')
		}
		return r
	}, s)
}

// ParseNumber parses a number written with Latin, Persian or Arabic digits and
// thousands separators (',', '٬', '’', or space between digit groups) and an optional
// decimal separator ('.' or '٫') and leading sign. Returns false if no number is found.
// It extracts the first number in the string ("قیمت: ۱۲٬۵۰۰" -> 12500).
func ParseNumber(s string) (float64, bool) {
	v, _, _, ok := findNumber(ToLatinDigits(s))
	return v, ok
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// groupSepLen reports the byte length of a thousands separator at s[i], or 0.
func groupSepLen(s string, i int) int {
	switch {
	case s[i] == ',' || s[i] == ' ' || s[i] == '\'':
		return 1
	case strings.HasPrefix(s[i:], "’"):
		return len("’")
	case strings.HasPrefix(s[i:], "\u00a0"):
		return len("\u00a0")
	case strings.HasPrefix(s[i:], "\u202f"):
		return len("\u202f")
	}
	return 0
}

// findNumber locates the first number in s (which must already use Latin digits)
// and returns its value and byte span [start, end).
func findNumber(s string) (val float64, start, end int, ok bool) {
	i := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return 0, 0, 0, false
	}
	start = i
	neg := false
	if i > 0 && (s[i-1] == '-' || s[i-1] == '+') {
		start = i - 1
		neg = s[i-1] == '-'
	} else if r, n := utf8.DecodeLastRuneInString(s[:i]); r == '\u2212' {
		start = i - n
		neg = true
	}

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i < len(s) && isASCIIDigit(s[i]) {
		b.WriteByte(s[i])
		i++
	}
	// Thousands groups: separator followed by exactly three digits.
	for i < len(s) {
		n := groupSepLen(s, i)
		if n == 0 {
			break
		}
		j := i + n
		if j+3 > len(s) || !isASCIIDigit(s[j]) || !isASCIIDigit(s[j+1]) || !isASCIIDigit(s[j+2]) || (j+3 < len(s) && isASCIIDigit(s[j+3])) {
			break
		}
		b.WriteString(s[j : j+3])
		i = j + 3
	}
	if i+1 < len(s) && s[i] == '.' && isASCIIDigit(s[i+1]) {
		b.WriteByte('.')
		i++
		for i < len(s) && isASCIIDigit(s[i]) {
			b.WriteByte(s[i])
			i++
		}
	}
	v, err := strconv.ParseFloat(b.String(), 64)
	if err != nil {
		return 0, 0, 0, false
	}
	return v, start, i, true
}

// Price is a monetary amount parsed from free text.
type Price struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"` // "IRT" (toman), "IRR" (rial), "USD", "EUR", "GBP", "AED", "TRY" or "" when unknown
	Raw      string  `json:"raw"`
}

var priceMultipliers = []struct {
	word string
	mult float64
}{
	{"میلیارد", 1e9}, {"ملیارد", 1e9},
	{"میلیون", 1e6}, {"ملیون", 1e6},
	{"هزار", 1e3},
	{"thousand", 1e3}, {"million", 1e6}, {"billion", 1e9},
	{"k", 1e3},
}

var currencyTokens = []struct {
	token    string
	currency string
}{
	{"تومان", "IRT"}, {"تومن", "IRT"}, {"toman", "IRT"}, {"irt", "IRT"},
	{"ریال", "IRR"}, {"rial", "IRR"}, {"irr", "IRR"}, {"﷼", "IRR"},
	{"$", "USD"}, {"usd", "USD"}, {"دلار", "USD"}, {"dollar", "USD"},
	{"€", "EUR"}, {"eur", "EUR"}, {"یورو", "EUR"},
	{"£", "GBP"}, {"gbp", "GBP"}, {"پوند", "GBP"},
	{"درهم", "AED"}, {"aed", "AED"},
	{"لیر", "TRY"}, {"try", "TRY"}, {"₺", "TRY"},
}

var freePriceRe = regexp.MustCompile(`رایگان|مجانی|\bfree\b`)

// isASCIILetter reports whether the byte at s[i] is an ASCII letter (false if out of range).
func isASCIILetter(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ParsePrice extracts a price from text such as "۱۲۵,۰۰۰ تومان", "1.2 میلیون تومان",
// "۲۵ هزار تومان", "120,000 ریال", "$12.99", "12.99 €", "رایگان" (free → 0).
func ParsePrice(s string) (Price, bool) {
	raw := strings.TrimSpace(s)
	t := strings.ToLower(NormalizePersian(ToLatinDigits(raw)))

	val, start, end, ok := findNumber(t)
	if !ok {
		if freePriceRe.MatchString(t) {
			return Price{Amount: 0, Raw: raw}, true
		}
		return Price{}, false
	}

	rest := strings.TrimLeft(t[end:], " \u200c")
	for _, m := range priceMultipliers {
		if !strings.HasPrefix(rest, m.word) {
			continue
		}
		if m.word[0] < utf8.RuneSelf && isASCIILetter(rest, len(m.word)) {
			continue
		}
		val *= m.mult
		end = len(t) - len(rest) + len(m.word)
		break
	}

	currency := ""
	best := -1
	for _, c := range currencyTokens {
		ascii := isASCIILetter(c.token, 0)
		for off := 0; off < len(t); {
			idx := strings.Index(t[off:], c.token)
			if idx < 0 {
				break
			}
			idx += off
			off = idx + len(c.token)
			if ascii && (isASCIILetter(t, idx-1) || isASCIILetter(t, idx+len(c.token))) {
				continue
			}
			var dist int
			switch {
			case idx >= end:
				dist = idx - end
			case idx+len(c.token) <= start:
				dist = start - (idx + len(c.token))
			default:
				continue
			}
			if best < 0 || dist < best {
				best = dist
				currency = c.currency
			}
		}
	}
	return Price{Amount: val, Currency: currency, Raw: raw}, true
}

// div and mod follow the truncating semantics used by the jalaali-js algorithm.
func jdiv(a, b int) int { return a / b }
func jmod(a, b int) int { return a % b }

var jalaliBreaks = [...]int{-61, 9, 38, 199, 426, 686, 756, 818, 1111, 1181, 1210,
	1635, 2060, 2097, 2192, 2262, 2324, 2394, 2456, 3178}

// jalCal returns the leap offset (0 for a leap year), the Gregorian year in which
// the Jalali year begins and the day of March on which Farvardin 1 falls.
func jalCal(jy int) (leap, gy, march int, ok bool) {
	bl := len(jalaliBreaks)
	gy = jy + 621
	leapJ := -14
	jp := jalaliBreaks[0]
	if jy < jp || jy >= jalaliBreaks[bl-1] {
		return 0, 0, 0, false
	}
	jump := 0
	for i := 1; i < bl; i++ {
		jm := jalaliBreaks[i]
		jump = jm - jp
		if jy < jm {
			break
		}
		leapJ += jdiv(jump, 33)*8 + jdiv(jmod(jump, 33), 4)
		jp = jm
	}
	n := jy - jp
	leapJ += jdiv(n, 33)*8 + jdiv(jmod(n, 33)+3, 4)
	if jmod(jump, 33) == 4 && jump-n == 4 {
		leapJ++
	}
	leapG := jdiv(gy, 4) - jdiv((jdiv(gy, 100)+1)*3, 4) - 150
	march = 20 + leapJ - leapG
	if jump-n < 6 {
		n = n - jump + jdiv(jump+4, 33)*33
	}
	leap = jmod(jmod(n+1, 33)-1, 4)
	if leap == -1 {
		leap = 4
	}
	return leap, gy, march, true
}

func g2d(gy, gm, gd int) int {
	d := jdiv((gy+jdiv(gm-8, 6)+100100)*1461, 4) + jdiv(153*jmod(gm+9, 12)+2, 5) + gd - 34840408
	return d - jdiv(jdiv(gy+100100+jdiv(gm-8, 6), 100)*3, 4) + 752
}

func d2g(jdn int) (gy, gm, gd int) {
	j := 4*jdn + 139361631
	j += jdiv(jdiv(4*jdn+183187720, 146097)*3, 4)*4 - 3908
	i := jdiv(jmod(j, 1461), 4)*5 + 308
	gd = jdiv(jmod(i, 153), 5) + 1
	gm = jmod(jdiv(i, 153), 12) + 1
	gy = jdiv(j, 1461) - 100100 + jdiv(8-gm, 6)
	return
}

// jalaliMonthLength returns the number of days in the given Jalali month, or 0 if invalid.
func jalaliMonthLength(jy, jm int) int {
	switch {
	case jm >= 1 && jm <= 6:
		return 31
	case jm >= 7 && jm <= 11:
		return 30
	case jm == 12:
		if leap, _, _, ok := jalCal(jy); ok && leap == 0 {
			return 30
		}
		return 29
	}
	return 0
}

// JalaliToGregorian converts a Jalali (Solar Hijri) date to Gregorian.
// Out-of-range input yields an unspecified result; use ParseJalaliDate for validation.
func JalaliToGregorian(jy, jm, jd int) (gy, gm, gd int) {
	_, y, march, _ := jalCal(jy)
	jdn := g2d(y, 3, march) + (jm-1)*31 - jdiv(jm, 7)*(jm-7) + jd - 1
	return d2g(jdn)
}

// GregorianToJalali converts a Gregorian date to Jalali.
func GregorianToJalali(gy, gm, gd int) (jy, jm, jd int) {
	jdn := g2d(gy, gm, gd)
	gy, _, _ = d2g(jdn)
	jy = gy - 621
	leap, _, march, _ := jalCal(jy)
	k := jdn - g2d(gy, 3, march)
	if k >= 0 {
		if k <= 185 {
			return jy, 1 + jdiv(k, 31), jmod(k, 31) + 1
		}
		k -= 186
	} else {
		jy--
		k += 179
		if leap == 1 {
			k++
		}
	}
	return jy, 7 + jdiv(k, 30), jmod(k, 30) + 1
}

var jalaliMonths = map[string]int{
	"فروردین": 1, "فروردين": 1,
	"اردیبهشت": 2, "اردی بهشت": 2, "اردی\u200cبهشت": 2,
	"خرداد": 3,
	"تیر":   4,
	"مرداد": 5, "امرداد": 5,
	"شهریور": 6,
	"مهر":    7,
	"آبان":   8, "ابان": 8,
	"آذر": 9, "اذر": 9,
	"دی":    10,
	"بهمن":  11,
	"اسفند": 12,
}

var (
	jalaliNumericRe  *regexp.Regexp
	jalaliDayFirst   *regexp.Regexp
	jalaliMonthFirst *regexp.Regexp
	clockRe          = regexp.MustCompile(`(\d{1,2}):(\d{2})(?::(\d{2}))?`)
	tehranOnce       sync.Once
	tehranLoc        *time.Location
)

func init() {
	names := make([]string, 0, len(jalaliMonths))
	for n := range jalaliMonths {
		names = append(names, regexp.QuoteMeta(n))
	}
	// Longest first so that e.g. "امرداد" wins over "مرداد".
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && len(names[j]) > len(names[j-1]); j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	alt := strings.Join(names, "|")
	jalaliNumericRe = regexp.MustCompile(`(\d{4})\s*[/\-.]\s*(\d{1,2})\s*[/\-.]\s*(\d{1,2})`)
	jalaliDayFirst = regexp.MustCompile(`(\d{1,2})\s*(?:ام|م)?\s+(` + alt + `)\s*(?:ماه)?\s*[،,]?\s*(\d{4})`)
	jalaliMonthFirst = regexp.MustCompile(`(` + alt + `)\s+(\d{1,2})\s*[،,]?\s*(\d{4})`)
}

func tehranLocation() *time.Location {
	tehranOnce.Do(func() {
		loc, err := time.LoadLocation("Asia/Tehran")
		if err != nil {
			loc = time.FixedZone("IRST", 3*3600+30*60)
		}
		tehranLoc = loc
	})
	return tehranLoc
}

// ParseJalaliDate parses Jalali dates like "۱۴۰۲/۰۵/۱۲", "1402-5-12", "۱۲ مرداد ۱۴۰۲",
// "مرداد ۱۲، ۱۴۰۲", optionally followed by a time "۱۴:۳۰" or "14:30:05". The result is
// the equivalent instant in the Asia/Tehran time zone (fall back to a fixed +03:30 zone
// if the tz database is unavailable).
func ParseJalaliDate(s string) (time.Time, bool) {
	t := NormalizePersian(ToLatinDigits(s))

	var jy, jm, jd, end int
	atoi := func(x string) int { n, _ := strconv.Atoi(x); return n }
	if m := jalaliNumericRe.FindStringSubmatchIndex(t); m != nil {
		jy, jm, jd = atoi(t[m[2]:m[3]]), atoi(t[m[4]:m[5]]), atoi(t[m[6]:m[7]])
		end = m[1]
	} else if m := jalaliDayFirst.FindStringSubmatchIndex(t); m != nil {
		jd, jm, jy = atoi(t[m[2]:m[3]]), jalaliMonths[t[m[4]:m[5]]], atoi(t[m[6]:m[7]])
		end = m[1]
	} else if m := jalaliMonthFirst.FindStringSubmatchIndex(t); m != nil {
		jm, jd, jy = jalaliMonths[t[m[2]:m[3]]], atoi(t[m[4]:m[5]]), atoi(t[m[6]:m[7]])
		end = m[1]
	} else {
		return time.Time{}, false
	}
	if _, _, _, ok := jalCal(jy); !ok || jd < 1 || jd > jalaliMonthLength(jy, jm) {
		return time.Time{}, false
	}

	hh, mm, ss := 0, 0, 0
	if m := clockRe.FindStringSubmatch(t[end:]); m != nil {
		hh, mm = atoi(m[1]), atoi(m[2])
		if m[3] != "" {
			ss = atoi(m[3])
		}
		if hh > 23 || mm > 59 || ss > 59 {
			return time.Time{}, false
		}
	}
	gy, gm, gd := JalaliToGregorian(jy, jm, jd)
	return time.Date(gy, time.Month(gm), gd, hh, mm, ss, 0, tehranLocation()), true
}

var dateLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02 15:04",
	"2006/01/02",
	time.RFC1123,
	time.RFC1123Z,
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04:05 -0700",
	time.RFC850,
	time.RFC822,
	time.RFC822Z,
	time.ANSIC,
	time.UnixDate,
	"Jan 2, 2006",
	"January 2, 2006",
	"Jan 2, 2006 15:04",
	"January 2, 2006 15:04",
	"2 January 2006",
	"2 Jan 2006",
	"02 Jan 2006 15:04",
	"Monday, January 2, 2006",
}

var leadingYearRe = regexp.MustCompile(`^\s*(\d{4})\D`)

// ParseDateAny parses common machine and human date formats: RFC3339/ISO-8601 (with or
// without time/zone), "2006-01-02 15:04:05", "2006/01/02", "Jan 2, 2006", "2 January 2006",
// RFC1123, and finally Jalali dates (years 1300–1499 are treated as Jalali).
func ParseDateAny(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	latin := ToLatinDigits(s)
	if m := leadingYearRe.FindStringSubmatch(latin); m != nil {
		if y, _ := strconv.Atoi(m[1]); y >= 1300 && y <= 1499 {
			return ParseJalaliDate(latin)
		}
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, latin); err == nil {
			return t, true
		}
	}
	if t, ok := ParseJalaliDate(latin); ok && t.Year() >= 1921 && t.Year() <= 2120 {
		return t, true
	}
	return time.Time{}, false
}
