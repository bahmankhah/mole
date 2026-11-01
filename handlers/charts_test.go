package handlers

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/resolver/crawler/jobs"
)

func f(v float64) *float64 { return &v }

func pt(day int, v float64) jobs.HistoryPoint {
	return jobs.HistoryPoint{At: time.Date(2026, 3, day, 9, 0, 0, 0, time.UTC), Num: f(v), Currency: "IRR"}
}

func TestGroupThousands(t *testing.T) {
	cases := map[float64]string{0: "0", 42: "42", 999: "999", 1000: "1,000", 433500000: "433,500,000"}
	for in, want := range cases {
		if got := groupThousands(in); got != want {
			t.Errorf("groupThousands(%v) = %q, want %q", in, got, want)
		}
	}
}

// One style per axis: in full below 10,000 and compact above it.
func TestCompactNum(t *testing.T) {
	cases := []struct {
		v       float64
		compact bool
		want    string
	}{
		{9999, false, "9,999"},
		{433500000, true, "433.5M"},
		{42000, true, "42K"},
		{2000000000, true, "2B"},
		{-42000, true, "-42K"},
		{500, true, "500"},
	}
	for _, c := range cases {
		if got := compactNum(c.v, c.compact); got != c.want {
			t.Errorf("compactNum(%v, %v) = %q, want %q", c.v, c.compact, got, c.want)
		}
	}
}

// A series that never moved still needs a band, or the scale divides by zero
// and the line is drawn at NaN.
func TestValueRangeHandlesAFlatSeries(t *testing.T) {
	lo, hi := valueRange(numericPoints([]jobs.HistoryPoint{pt(1, 100), pt(2, 100)}))
	if !(lo < 100 && hi > 100) {
		t.Errorf("range = %v..%v, want it to straddle 100", lo, hi)
	}
	if lo2, hi2 := valueRange(nil); lo2 >= hi2 {
		t.Errorf("empty range = %v..%v, want a usable span", lo2, hi2)
	}
}

// The band's extremes count toward the scale, or a compacted day's high
// would be drawn outside the plot.
func TestValueRangeIncludesTheBand(t *testing.T) {
	p := pt(1, 100)
	p.Rolled, p.Low, p.High = true, f(50), f(300)
	lo, hi := valueRange(numericPoints([]jobs.HistoryPoint{p, pt(2, 110)}))
	if lo > 50 || hi < 300 {
		t.Errorf("range = %v..%v, want it to contain the band 50..300", lo, hi)
	}
}

func TestSparklineNeedsTwoPoints(t *testing.T) {
	if got := sparkline([]jobs.HistoryPoint{pt(1, 100)}, 84, 20); got != "" {
		t.Errorf("a single value is not a trend, got %q", got)
	}
	if got := sparkline(nil, 84, 20); got != "" {
		t.Error("no values should draw nothing")
	}
	got := string(sparkline([]jobs.HistoryPoint{pt(1, 100), pt(2, 90)}, 84, 20))
	if !strings.Contains(got, "<polyline") {
		t.Error("two values should draw a line")
	}
	// The accent segment is drawn over the de-emphasised run, so both hues
	// appear: that is what marks "now" without spending a marker on it.
	if !strings.Contains(got, chartSeriesDim) || !strings.Contains(got, chartSeries) {
		t.Error("the latest step should be in the accent hue over the dimmed run")
	}
	if !strings.Contains(got, "<title>") {
		t.Error("the sparkline's summary should be reachable without following the link")
	}
}

// Points with no low/high must not be bridged into the wash, or a raw run
// would be drawn as though its range were known.
func TestBandSegmentsOnlyCoverCompactedRuns(t *testing.T) {
	rolled := func(day int, v, lo, hi float64) jobs.HistoryPoint {
		p := pt(day, v)
		p.Rolled, p.Low, p.High = true, f(lo), f(hi)
		return p
	}
	segs := bandSegments(numericPoints([]jobs.HistoryPoint{
		rolled(1, 100, 95, 105),
		rolled(2, 102, 99, 110),
		pt(3, 104), // a raw run breaks the band
		rolled(4, 106, 100, 112),
		rolled(5, 108, 101, 114),
	}))
	if len(segs) != 2 {
		t.Fatalf("segments = %d, want 2 either side of the raw run", len(segs))
	}
	for _, s := range segs {
		if len(s) != 2 {
			t.Errorf("segment length = %d, want 2", len(s))
		}
	}
	// A lone compacted day cannot form a polygon and must be dropped.
	if got := bandSegments(numericPoints([]jobs.HistoryPoint{rolled(1, 100, 95, 105), pt(2, 104)})); len(got) != 0 {
		t.Errorf("a single compacted day should not make a band, got %d", len(got))
	}
}

func TestListingChartRendersTheHoverAndFocusLayer(t *testing.T) {
	h := &jobs.ListingHistory{
		Field: "price", Currency: "IRR", FieldKind: "number",
		Points: []jobs.HistoryPoint{pt(1, 100), pt(2, 90), pt(3, 95)},
	}
	got := string(listingChart(h, 880, 240))
	if got == "" {
		t.Fatal("three points should draw a chart")
	}
	for _, want := range []string{"lst-hit", `tabindex="0"`, "lst-cross", "<title>", "data-value"} {
		if !strings.Contains(got, want) {
			t.Errorf("chart is missing %q — the hover layer must work on keyboard focus too", want)
		}
	}
	// The cheapest point is directly labelled, since it is not the endpoint.
	if !strings.Contains(got, "low ") {
		t.Error("the cheapest point should carry a direct label")
	}
	if strings.Count(got, "<circle") != 2 {
		t.Errorf("markers = %d, want the endpoint and the low", strings.Count(got, "<circle"))
	}
}

func TestListingChartNeedsTwoPoints(t *testing.T) {
	h := &jobs.ListingHistory{Field: "price", Points: []jobs.HistoryPoint{pt(1, 100)}}
	if got := listingChart(h, 880, 240); got != "" {
		t.Error("one point is not a series")
	}
}

// Titles come from crawled pages, so they are untrusted and must never
// reach the SVG unescaped.
func TestChartsEscapeUntrustedText(t *testing.T) {
	h := &jobs.ListingHistory{
		Field: `price" onload="alert(1)`, Currency: `<script>`, FieldKind: "number",
		Points: []jobs.HistoryPoint{pt(1, 100), pt(2, 90)},
	}
	got := string(listingChart(h, 880, 240))
	if strings.Contains(got, "<script>") || strings.Contains(got, `onload="alert`) {
		t.Error("untrusted text reached the SVG unescaped")
	}
}

// The axis fits the data, but never so tightly that the gridlines run out.
func TestAxisScaleKeepsCleanTicksAndEnoughOfThem(t *testing.T) {
	// The case that drove this: a price between 391M and 491M. Snapping the
	// axis out to clean bounds would waste a third of the panel; a clean
	// step of 50M would leave only two lines inside a fitted one.
	lo, hi, ticks := axisScale(385_600_000, 490_900_000, 4)
	if lo > 385_600_000 || hi < 490_900_000 {
		t.Errorf("scale %v..%v does not contain the data", lo, hi)
	}
	if hi-lo > (490_900_000-385_600_000)*1.5 {
		t.Errorf("scale %v..%v is far wider than the data it holds", lo, hi)
	}
	if len(ticks) < 3 {
		t.Errorf("ticks = %v, want at least 3 so the far end has a reference", ticks)
	}
	// Every tick is inside the scale and labelled with a round number —
	// no 416.5M among them.
	for _, v := range ticks {
		if v < lo || v > hi {
			t.Errorf("tick %v falls outside the scale %v..%v", v, lo, hi)
		}
		if math.Mod(v, 1e7) != 0 {
			t.Errorf("tick %v is not a round number (labelled %q)", v, compactNum(v, true))
		}
	}
}

func TestSmallerNiceStepWalksTheSequence(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{50, 20}, {20, 10}, {10, 5}, {5, 2}, {2, 1}, {1, 0.5},
	}
	for _, c := range cases {
		if got := smallerNiceStep(c.in); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("smallerNiceStep(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A price that never moved still needs a usable scale.
func TestAxisScaleHandlesAFlatSeries(t *testing.T) {
	lo, hi, ticks := axisScale(42000, 42000, 4)
	if !(lo < 42000 && hi > 42000) {
		t.Errorf("flat scale = %v..%v, want it to straddle the value", lo, hi)
	}
	if len(ticks) == 0 {
		t.Error("a flat series should still get gridlines")
	}
}

// A real range must never be labelled as though both ends were the same
// number: compact notation rounds 1,610,000,000 and 1,640,000,000 to the
// same "1.6B", and "1.6B–1.6B" reads as a bug rather than as a spread.
func TestSpreadFallsBackWhenCompactHidesTheRange(t *testing.T) {
	fn := chartFuncs["spread"].(func(*float64, *float64) string)
	if got := fn(f(1_610_000_000), f(1_640_000_000)); strings.Contains(got, "1.6B–1.6B") {
		t.Errorf("spread = %q, want the range to be visible", got)
	}
	if got := fn(f(100), f(500)); got != "100–500" {
		t.Errorf("spread = %q, want 100–500", got)
	}
	if got := fn(f(42_000), f(91_000)); got != "42K–91K" {
		t.Errorf("spread = %q, want 42K–91K", got)
	}
	// Genuinely equal ends collapse to one number rather than "x–x".
	if got := fn(f(500), f(500)); got != "500" {
		t.Errorf("spread = %q, want a single 500", got)
	}
	if got := fn(nil, f(500)); got != "" {
		t.Errorf("spread = %q, want empty when an end is missing", got)
	}
}

// Durations read at one unit. Duration.String() pads out every smaller
// one, so twenty hours used to render as "20h0m0s" on every page.
func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{20 * time.Minute, "20m"},
		// Truncated, not rounded: these read as "at least this long", so
		// 59m59s must not become "60m".
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h"},
		{20 * time.Hour, "20h"},
		{47 * time.Hour, "47h"},
		{48 * time.Hour, "2d"},
		{9 * 24 * time.Hour, "9d"},
		// A negative is shown as its magnitude; the callers add the
		// direction ("in …" / "… ago").
		{-20 * time.Hour, "20h"},
	}
	for _, c := range cases {
		if got := humanDuration(c.d); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
