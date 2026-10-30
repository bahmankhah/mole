package modules

import (
	"math"
	"testing"
	"time"
)

func TestNormalizePersian(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"arabic yeh and kaf", "كتاب علي", "کتاب علی"},
		{"alef maksura", "موسى", "موسی"},
		{"teh marbuta kept", "مدرسة", "مدرسة"},
		{"diacritics", "ک\u0650تاب\u064c", "کتاب"},
		{"superscript alef", "رحم\u0670ن", "رحمن"},
		{"tatweel", "س\u0640\u0640لام", "سلام"},
		{"bidi marks", "\u200fسلام\u200e \u202bدنیا\u202c\u2067", "سلام دنیا"},
		{"nbsp and spaces", "سلام\u00a0\u2003 دنیا\n\tخوب", "سلام دنیا خوب"},
		{"in-word zwnj kept", "می\u200cروم", "می\u200cروم"},
		{"zwnj run collapsed", "می\u200c\u200c\u200cروم", "می\u200cروم"},
		{"zwnj next to space", "کتاب\u200c ها \u200cرا", "کتاب ها را"},
		{"zwnj at edges", "\u200cسلام\u200c", "سلام"},
		{"trim", "  سلام  ", "سلام"},
		{"digits untouched", "۱۲۳ 456 ٧٨٩", "۱۲۳ 456 ٧٨٩"},
		{"latin untouched", "Hello World", "Hello World"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizePersian(tt.in); got != tt.want {
				t.Errorf("NormalizePersian(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDigitConversion(t *testing.T) {
	latin := []struct{ in, want string }{
		{"۰۱۲۳۴۵۶۷۸۹", "0123456789"},
		{"٠١٢٣٤٥٦٧٨٩", "0123456789"},
		{"۱۲٬۵۰۰٫۷۵", "12,500.75"},
		{"abc 12", "abc 12"},
	}
	for _, tt := range latin {
		if got := ToLatinDigits(tt.in); got != tt.want {
			t.Errorf("ToLatinDigits(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	persian := []struct{ in, want string }{
		{"0123456789", "۰۱۲۳۴۵۶۷۸۹"},
		{"page 12", "page ۱۲"},
	}
	for _, tt := range persian {
		if got := ToPersianDigits(tt.in); got != tt.want {
			t.Errorf("ToPersianDigits(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseNumber(t *testing.T) {
	tests := []struct {
		in     string
		want   float64
		wantOK bool
	}{
		{"قیمت: ۱۲٬۵۰۰", 12500, true},
		{"1,234,567", 1234567, true},
		{"۱۲۳۴", 1234, true},
		{"٣٫١٤", 3.14, true},
		{"-42", -42, true},
		{"+7.5", 7.5, true},
		{"دمای \u2212۵ درجه", -5, true},
		{"12.75", 12.75, true},
		{"1’000’000", 1000000, true},
		{"۱۲ ۵۰۰ تومان", 12500, true},
		{"12,5", 12, true},
		{"1,2345", 1, true},
		{"a-b 3", 3, true},
		{"بدون عدد", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		got, ok := ParseNumber(tt.in)
		if ok != tt.wantOK || math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("ParseNumber(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestParsePrice(t *testing.T) {
	tests := []struct {
		in       string
		amount   float64
		currency string
		ok       bool
	}{
		{"۱۲۵,۰۰۰ تومان", 125000, "IRT", true},
		{"1.2 میلیون تومان", 1.2e6, "IRT", true},
		{"۲۵ هزار تومان", 25000, "IRT", true},
		{"۲۵هزار تومن", 25000, "IRT", true},
		{"۳ میلیارد ریال", 3e9, "IRR", true},
		{"120,000 ریال", 120000, "IRR", true},
		{"$12.99", 12.99, "USD", true},
		{"12.99 €", 12.99, "EUR", true},
		{"£5", 5, "GBP", true},
		{"100 AED", 100, "AED", true},
		{"۵۰ درهم", 50, "AED", true},
		{"200 لیر", 200, "TRY", true},
		{"15 USD", 15, "USD", true},
		{"۲۰ دلار", 20, "USD", true},
		{"10k toman", 10000, "IRT", true},
		{"قیمت: ۴۵٬۰۰۰ تومان", 45000, "IRT", true},
		{"500 ﷼", 500, "IRR", true},
		{"1200", 1200, "", true},
		{"رایگان", 0, "", true},
		{"Free", 0, "", true},
		{"ناموجود", 0, "", false},
		// Ranges: the lower end, with the multiplier written once for both.
		{"از ۲۵ تا ۳۰ میلیون تومان", 25e6, "IRT", true},
		{"۲۵ الی ۳۰ میلیون تومان", 25e6, "IRT", true},
		{"۱.۵-۲ میلیون تومان", 1.5e6, "IRT", true},
		{"2 to 3k USD", 2000, "USD", true},
		{"۲۰ هزار تا ۱ میلیون تومان", 20000, "IRT", true},
		{"۵۰۰,۰۰۰ تا ۷۰۰,۰۰۰ تومان", 500000, "IRT", true},
		{"۳ تا ۵ عدد", 3, "", true},
	}
	for _, tt := range tests {
		got, ok := ParsePrice(tt.in)
		if ok != tt.ok {
			t.Errorf("ParsePrice(%q) ok = %v, want %v", tt.in, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if math.Abs(got.Amount-tt.amount) > 1e-6 || got.Currency != tt.currency {
			t.Errorf("ParsePrice(%q) = %+v, want amount %v currency %q", tt.in, got, tt.amount, tt.currency)
		}
		if got.Raw != tt.in {
			t.Errorf("ParsePrice(%q).Raw = %q", tt.in, got.Raw)
		}
	}
	if p, _ := ParsePrice("  $3  "); p.Raw != "$3" {
		t.Errorf("Raw not trimmed: %q", p.Raw)
	}
}

func TestJalaliConversion(t *testing.T) {
	tests := []struct{ jy, jm, jd, gy, gm, gd int }{
		{1402, 1, 1, 2023, 3, 21},
		{1403, 12, 30, 2025, 3, 20},
		{1399, 12, 30, 2021, 3, 20},
		{1400, 1, 1, 2021, 3, 21},
		{1404, 7, 5, 2025, 9, 27},
		{1357, 11, 22, 1979, 2, 11},
	}
	for _, tt := range tests {
		gy, gm, gd := JalaliToGregorian(tt.jy, tt.jm, tt.jd)
		if gy != tt.gy || gm != tt.gm || gd != tt.gd {
			t.Errorf("JalaliToGregorian(%d,%d,%d) = %d-%d-%d, want %d-%d-%d", tt.jy, tt.jm, tt.jd, gy, gm, gd, tt.gy, tt.gm, tt.gd)
		}
		jy, jm, jd := GregorianToJalali(tt.gy, tt.gm, tt.gd)
		if jy != tt.jy || jm != tt.jm || jd != tt.jd {
			t.Errorf("GregorianToJalali(%d,%d,%d) = %d/%d/%d, want %d/%d/%d", tt.gy, tt.gm, tt.gd, jy, jm, jd, tt.jy, tt.jm, tt.jd)
		}
	}
}

func TestJalaliRoundTrip(t *testing.T) {
	end := time.Date(2030, 12, 31, 0, 0, 0, 0, time.UTC)
	prevJ := [3]int{}
	for d := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC); !d.After(end); d = d.AddDate(0, 0, 1) {
		jy, jm, jd := GregorianToJalali(d.Year(), int(d.Month()), d.Day())
		if jm < 1 || jm > 12 || jd < 1 || jd > jalaliMonthLength(jy, jm) {
			t.Fatalf("%s -> invalid Jalali %d/%d/%d", d.Format("2006-01-02"), jy, jm, jd)
		}
		gy, gm, gd := JalaliToGregorian(jy, jm, jd)
		if gy != d.Year() || gm != int(d.Month()) || gd != d.Day() {
			t.Fatalf("round trip %s -> %d/%d/%d -> %d-%d-%d", d.Format("2006-01-02"), jy, jm, jd, gy, gm, gd)
		}
		cur := [3]int{jy, jm, jd}
		if prevJ != [3]int{} && !jalaliFollows(prevJ, cur) {
			t.Fatalf("Jalali dates not consecutive: %v -> %v", prevJ, cur)
		}
		prevJ = cur
	}
}

func jalaliFollows(p, c [3]int) bool {
	if c[0] == p[0] && c[1] == p[1] {
		return c[2] == p[2]+1
	}
	if p[2] != jalaliMonthLength(p[0], p[1]) || c[2] != 1 {
		return false
	}
	if c[0] == p[0] {
		return c[1] == p[1]+1
	}
	return c[0] == p[0]+1 && p[1] == 12 && c[1] == 1
}

func TestParseJalaliDate(t *testing.T) {
	loc := tehranLocation()
	tests := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"۱۴۰۲/۰۵/۱۲", time.Date(2023, 8, 3, 0, 0, 0, 0, loc), true},
		{"1402-5-12", time.Date(2023, 8, 3, 0, 0, 0, 0, loc), true},
		{"۱۲ مرداد ۱۴۰۲", time.Date(2023, 8, 3, 0, 0, 0, 0, loc), true},
		{"مرداد ۱۲، ۱۴۰۲", time.Date(2023, 8, 3, 0, 0, 0, 0, loc), true},
		{"۱۲ امرداد ۱۴۰۲", time.Date(2023, 8, 3, 0, 0, 0, 0, loc), true},
		{"۱ فروردين ۱۴۰۰", time.Date(2021, 3, 21, 0, 0, 0, 0, loc), true},
		{"۵ مهر ۱۴۰۴", time.Date(2025, 9, 27, 0, 0, 0, 0, loc), true},
		{"۳۰ اسفند ۱۴۰۳", time.Date(2025, 3, 20, 0, 0, 0, 0, loc), true},
		{"۱۰ دی ۱۴۰۲ ساعت ۱۴:۳۰", time.Date(2023, 12, 31, 14, 30, 0, 0, loc), true},
		{"1402/05/12 14:30:05", time.Date(2023, 8, 3, 14, 30, 5, 0, loc), true},
		{"۳۰ اسفند ۱۴۰۲", time.Time{}, false}, // 1402 is not a leap year
		{"1402/13/01", time.Time{}, false},
		{"1402/07/31", time.Time{}, false},
		{"1402/05/12 25:00", time.Time{}, false},
		{"hello", time.Time{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseJalaliDate(tt.in)
		if ok != tt.ok {
			t.Errorf("ParseJalaliDate(%q) ok = %v, want %v", tt.in, ok, tt.ok)
			continue
		}
		if ok && !got.Equal(tt.want) {
			t.Errorf("ParseJalaliDate(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseDateAny(t *testing.T) {
	loc := tehranLocation()
	tests := []struct {
		in   string
		want time.Time
	}{
		{"2024-03-05T10:20:30Z", time.Date(2024, 3, 5, 10, 20, 30, 0, time.UTC)},
		{"2024-03-05T10:20:30+03:30", time.Date(2024, 3, 5, 6, 50, 30, 0, time.UTC)},
		{"2024-03-05T10:20:30.123Z", time.Date(2024, 3, 5, 10, 20, 30, 123e6, time.UTC)},
		{"2024-03-05", time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC)},
		{"2024-03-05 10:20:30", time.Date(2024, 3, 5, 10, 20, 30, 0, time.UTC)},
		{"2024/03/05", time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC)},
		{"Mar 5, 2024", time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC)},
		{"5 March 2024", time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC)},
		{"Tue, 05 Mar 2024 10:20:30 GMT", time.Date(2024, 3, 5, 10, 20, 30, 0, time.UTC)},
		{"1402-05-12", time.Date(2023, 8, 3, 0, 0, 0, 0, loc)},
		{"۱۴۰۲/۰۵/۱۲ ۱۴:۳۰", time.Date(2023, 8, 3, 14, 30, 0, 0, loc)},
		{"۱۲ مرداد ۱۴۰۲", time.Date(2023, 8, 3, 0, 0, 0, 0, loc)},
	}
	for _, tt := range tests {
		got, ok := ParseDateAny(tt.in)
		if !ok {
			t.Errorf("ParseDateAny(%q) failed", tt.in)
			continue
		}
		if !got.Equal(tt.want) {
			t.Errorf("ParseDateAny(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
	for _, bad := range []string{"", "not a date", "2024-13-45"} {
		if _, ok := ParseDateAny(bad); ok {
			t.Errorf("ParseDateAny(%q) unexpectedly succeeded", bad)
		}
	}
}
