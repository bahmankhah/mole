package handlers

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
)

// TestWritePreview renders the new charts with realistic data so they can be
// screenshotted and eyeballed. Not an assertion; skipped unless asked for.
func TestWritePreview(t *testing.T) {
	out := os.Getenv("PREVIEW_OUT")
	if out == "" {
		t.Skip("set PREVIEW_OUT to render the preview")
	}
	fp := func(v float64) *float64 { return &v }
	start := time.Date(2026, 1, 5, 6, 0, 0, 0, time.UTC)

	// A believable Iranian phone price in rials: a long flat spell, a
	// discount, a recovery, then a steep run-up. The first stretch is
	// compacted days (with a low–high band); the recent stretch is raw runs.
	var points []jobs.HistoryPoint
	price := 418500000.0
	for i := 0; i < 46; i++ {
		switch {
		case i == 12:
			price *= 0.93
		case i == 21:
			price *= 1.04
		case i > 30:
			price *= 1.012
		case i%5 == 0:
			price *= 1.002
		}
		p := jobs.HistoryPoint{
			At: start.AddDate(0, 0, i), Num: fp(math.Round(price/100000) * 100000), Currency: "IRR",
		}
		if i < 28 { // compacted: these days were folded from several runs
			p.Rolled, p.Samples = true, 4
			p.Low, p.High = fp(*p.Num*0.985), fp(*p.Num*1.012)
		}
		points = append(points, p)
	}
	hist := &jobs.ListingHistory{
		Listing: models.Listing{
			ID: 42, SiteKey: "digikala.com", SiteLabel: "digikala.com",
			Title:     "گوشی موبایل اپل iPhone 15 Pro Max ظرفیت 256 گیگابایت",
			KeyMethod: models.ListingBySKU, Runs: 46,
		},
		Field: "price", FieldKind: "number", Currency: "IRR",
		Fields: []string{"price", "regular_price", "availability"},
		Points: points, Runs: 46,
		First: points[0].Num, Current: points[len(points)-1].Num,
		KeyStable: true, Tracks: true,
	}
	for _, p := range points {
		if hist.Low == nil || *p.Num < *hist.Low {
			hist.Low = p.Num
		}
		if hist.High == nil || *p.Num > *hist.High {
			hist.High = p.Num
		}
	}

	// Three shops' sparklines, as they appear in the comparison table.
	shops := []struct {
		name  string
		seed  float64
		drift float64
	}{
		{"digikala.com", 418500000, 1.004},
		{"technolife.ir", 431000000, 0.997},
		{"mobile140.com", 409900000, 1.001},
	}
	var cells strings.Builder
	for _, sh := range shops {
		var sp []jobs.HistoryPoint
		v := sh.seed
		for i := 0; i < 14; i++ {
			v *= sh.drift
			if i == 7 {
				v *= 0.96
			}
			sp = append(sp, jobs.HistoryPoint{At: start.AddDate(0, 0, i), Num: fp(v), Currency: "IRR"})
		}
		first, last := *sp[0].Num, *sp[len(sp)-1].Num
		dir, cls := "▼", "cmp-down"
		if last > first {
			dir, cls = "▲", "cmp-up"
		}
		fmt.Fprintf(&cells, `<td class="cmp-cell %s">
			<a href="#" class="cmp-val text-gray-800">%s IRR</a>
			<div class="text-xs %s">%s %s since the previous run</div>
			%s
			<div class="text-xs text-gray-400">3 hours ago · 14 runs</div>
		</td>`,
			map[bool]string{true: "cmp-best", false: ""}[sh.name == "mobile140.com"],
			compactNum(last, true), cls, dir, compactNum(math.Abs(last-first), true),
			sparkline(sp, 84, 20))
	}

	page := fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset="utf-8">
<script src="/static/js/tailwind.js"></script>
<style>
body{font-family:ui-sans-serif,system-ui,sans-serif;background:#f3f4f6;margin:0;padding:24px}
.card{background:#fff;border-radius:.5rem;box-shadow:0 1px 3px rgba(0,0,0,.08);padding:24px;margin-bottom:24px;max-width:960px}
.cmp-cell{padding:.5rem .75rem;vertical-align:top;font-size:14px}
.cmp-best{background:#ecfdf5}.cmp-best .cmp-val{color:#047857;font-weight:700}
.cmp-up{color:#b91c1c}.cmp-down{color:#047857}
.cmp-spark{display:block;margin-top:.25rem;overflow:visible}
.text-xs{font-size:11px}.text-gray-400{color:#9ca3af}.text-gray-800{color:#1f2937}
.text-gray-500{color:#6b7280}.text-gray-900{color:#111827}
table{border-collapse:collapse;width:100%%}th{font-size:10px;text-transform:uppercase;color:#6b7280;text-align:left;padding:8px 12px;background:#f9fafb}
h2{font-size:14px;font-weight:600;color:#1f2937;margin:0 0 4px}
.stat{display:inline-block;min-width:150px;margin-right:16px}
.stat .l{font-size:11px;color:#6b7280}.stat .v{font-size:20px;font-weight:600;color:#111827}
.lst-key{display:inline-block;width:10px;height:2px;background:#4f46e5;vertical-align:middle;margin-right:4px}
</style></head><body>

<div class="card">
  <h2>Comparison — reading across: who sells it, and for how much</h2>
  <p class="text-xs text-gray-500">Each cell is one shop's cheapest live offer; the sparkline is that offer over time.</p>
  <table style="margin-top:12px">
    <tr><th>Product</th><th>digikala.com</th><th>technolife.ir</th><th>mobile140.com</th></tr>
    <tr>
      <td class="cmp-cell" style="max-width:220px">
        <div style="font-weight:500;color:#1f2937">iPhone 15 Pro Max 256GB</div>
        <div class="text-xs text-gray-400">Apple · 256GB Natural Titanium · gtin 100%% · 3 offers</div>
      </td>
      %s
    </tr>
  </table>
</div>

<div class="card">
  <h2><span class="lst-key"></span>Tracking — reading along: price at digikala.com, over time</h2>
  <p class="text-xs text-gray-500" style="margin-bottom:16px">One point per run. The shaded band is a day compacted from several runs.</p>
  <div class="stat"><div class="l">Now</div><div class="v">%s IRR</div><div class="text-xs cmp-up">▲ %s (+%.1f%%) since first seen</div></div>
  <div class="stat"><div class="l">Lowest recorded</div><div class="v">%s</div></div>
  <div class="stat"><div class="l">Highest recorded</div><div class="v">%s</div></div>
  <div style="margin-top:20px">%s</div>
</div>
</body></html>`,
		cells.String(),
		compactNum(*hist.Current, true),
		compactNum(*hist.Current-*hist.First, true),
		(*hist.Current-*hist.First)/(*hist.First)*100,
		compactNum(*hist.Low, true), compactNum(*hist.High, true),
		listingChart(hist, 880, 260))

	if err := os.WriteFile(out, []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", out)
}
