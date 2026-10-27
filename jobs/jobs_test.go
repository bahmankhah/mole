package jobs

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/resolver/crawler/models"
)

func TestParseTarget(t *testing.T) {
	cases := []struct{ in, url, domain string }{
		{"https://www.digikala.com/product/1", "https://www.digikala.com/product/1", "digikala.com"},
		{"shop.example.co.ir", "https://shop.example.co.ir", "example.co.ir"},
		{"http://127.0.0.1:8080/x", "http://127.0.0.1:8080/x", "127.0.0.1"},
	}
	for _, c := range cases {
		u, d, err := ParseTarget(c.in)
		if err != nil || u != c.url || d != c.domain {
			t.Errorf("ParseTarget(%q) = %q, %q, %v; want %q, %q", c.in, u, d, err, c.url, c.domain)
		}
	}
	for _, bad := range []string{"", "ftp://x.com", "http://"} {
		if _, _, err := ParseTarget(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("ParseTarget(%q) error = %v, want ErrInvalidInput", bad, err)
		}
	}
}

func TestExpandTemplateURL(t *testing.T) {
	seeds, err := ExpandTemplateURL("https://x.ir/p/{{ID}}", map[string]string{"ID": "1-3"})
	if err != nil || len(seeds) != 3 || seeds[2] != "https://x.ir/p/3" {
		t.Fatalf("got %v %v", seeds, err)
	}
	if _, err := ExpandTemplateURL("https://x.ir/p/{{ID}}", nil); err == nil {
		t.Fatal("missing vars accepted")
	}
	if s, err := ExpandTemplateURL("https://x.ir/", nil); s != nil || err != nil {
		t.Fatalf("plain URL: %v %v", s, err)
	}
}

func TestNextRun(t *testing.T) {
	from := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	next, err := NextRun("0 3 * * *", "Asia/Tehran", from)
	if err != nil {
		t.Fatal(err)
	}
	// 10:30 UTC = 14:00 Tehran, so the next 03:00 Tehran is 23:30 UTC the same day.
	if want := time.Date(2026, 1, 1, 23, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next.UTC(), want)
	}
	if n, err := NextRun("@every 6h", "", from); err != nil || n.Sub(from) != 6*time.Hour {
		t.Fatalf("@every: %v %v", n, err)
	}
	for _, bad := range []string{"", "* * *", "61 * * * *"} {
		if _, err := NextRun(bad, "UTC", from); err == nil {
			t.Errorf("cron %q accepted", bad)
		}
	}
	if _, err := NextRun("@daily", "Mars/Base", from); err == nil {
		t.Error("invalid timezone accepted")
	}
}

func TestSign(t *testing.T) {
	sig := Sign("secret", 1700000000, []byte(`{"a":1}`))
	if !strings.HasPrefix(sig, "t=1700000000,v1=") || len(sig) != len("t=1700000000,v1=")+64 {
		t.Fatalf("signature %q", sig)
	}
	if sig == Sign("other", 1700000000, []byte(`{"a":1}`)) {
		t.Fatal("signature does not depend on the secret")
	}
}

func TestWantsEvent(t *testing.T) {
	w := &models.WebhookConfig{URL: "https://x"}
	if !wantsEvent(w, EventJobCompleted) || wantsEvent(w, EventPageCrawled) || wantsEvent(w, EventJobProgress) {
		t.Fatal("default events wrong")
	}
	w.Events = []string{EventPageCrawled}
	if !wantsEvent(w, EventPageCrawled) || wantsEvent(w, EventJobCompleted) {
		t.Fatal("explicit events wrong")
	}
	w.Events = []string{"*"}
	if !wantsEvent(w, EventJobProgress) {
		t.Fatal("wildcard wrong")
	}
	if wantsEvent(nil, EventJobCompleted) || wantsEvent(&models.WebhookConfig{}, EventJobCompleted) {
		t.Fatal("no webhook must not want events")
	}
}

func TestWriteCSVFlattensExtraction(t *testing.T) {
	md := "# عنوان"
	recs := []exportRecord{{
		PageID: 1, URL: "https://x.ir/p/1", StatusCode: 200, Markdown: &md,
		Extracted: []byte(`{"name":"گوشی","price":{"amount":12500000,"currency":"IRT"},"tags":["a","b"],"specs":[{"k":"v"}]}`),
	}}
	var buf bytes.Buffer
	if err := writeCSV(&buf, recs); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if !bytes.HasPrefix(out, []byte("\xef\xbb\xbf")) {
		t.Fatal("missing UTF-8 BOM")
	}
	rows, err := csv.NewReader(bytes.NewReader(out[3:])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for i, col := range rows[0] {
		got[col] = rows[1][i]
	}
	want := map[string]string{
		"extracted.name": "گوشی", "extracted.price.amount": "12500000", "extracted.price.currency": "IRT",
		"extracted.tags": "a | b", "extracted.specs": `[{"k":"v"}]`, "markdown": md,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("column %s = %q, want %q", k, got[k], v)
		}
	}
	if rows[0][len(rows[0])-1] != "markdown" {
		t.Error("markdown should be the last column")
	}
}
