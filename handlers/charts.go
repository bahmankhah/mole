package handlers

import (
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"

	"github.com/resolver/crawler/jobs"
)

// Chart palette. The UI's design system is Tailwind on a white surface and
// ships no dark mode, so these are its steps, filling the slots the method
// asks for: one hue for the single tracked series, the two reserved status
// colours for direction, and recessive grays for chrome.
//
// Validated as a set rather than judged by eye: every slot clears the
// lightness band, the chroma floor and 3:1 against the surface. The
// closest adjacent pair is the two status colours, at ΔE 8.4 under
// deuteranopia and 6.4 under tritanopia — inside the floor band, which is
// only legal alongside a second encoding, so every direction indicator in
// these templates also carries a ▲/▼ glyph and words. Never let one of
// them stand on colour alone.
const (
	chartSeries    = "#4f46e5" // indigo-600: the tracked value
	chartSeriesDim = "#a5b4fc" // indigo-300: the series before the latest step
	chartGrid      = "#e5e7eb" // gray-200: hairline gridlines
	chartAxisText  = "#6b7280" // gray-500: axis and label text
	chartSurface   = "#ffffff" // the surface the rings and gaps are cut in
)

// numericPoints keeps the points that carry a number, with the index each
// held in the original series so the x axis stays evenly spaced in time
// order even where a run recorded only text.
type numericPoint struct {
	i     int
	point jobs.HistoryPoint
}

func numericPoints(points []jobs.HistoryPoint) []numericPoint {
	out := make([]numericPoint, 0, len(points))
	for i, p := range points {
		if p.Num != nil {
			out = append(out, numericPoint{i, p})
		}
	}
	return out
}

// dataRange is the raw span the data occupies, bands included.
func dataRange(pts []numericPoint) (lo, hi float64, ok bool) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		v := *p.point.Num
		lo, hi = math.Min(lo, v), math.Max(hi, v)
		if p.point.Low != nil {
			lo = math.Min(lo, *p.point.Low)
		}
		if p.point.High != nil {
			hi = math.Max(hi, *p.point.High)
		}
	}
	if math.IsInf(lo, 1) {
		return 0, 1, false
	}
	return lo, hi, true
}

// niceStep rounds a raw step up to 1, 2 or 5 times a power of ten, which
// is what makes an axis read 400M / 450M / 500M rather than 416.5M / 460M.
func niceStep(raw float64) float64 {
	if raw <= 0 || math.IsInf(raw, 0) || math.IsNaN(raw) {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	switch n := raw / mag; {
	case n <= 1:
		return mag
	case n <= 2:
		return 2 * mag
	case n <= 5:
		return 5 * mag
	}
	return 10 * mag
}

// axisScale fits the y axis to the data with a little air, then places
// gridlines on the clean values that fall inside it.
//
// Snapping the axis itself out to tick boundaries would be simpler, but it
// throws away plot height: a price between 391M and 491M would be drawn on
// a 350M–500M scale, flattening a 25% move into two thirds of the panel.
// Labelling only the ticks that land inside keeps both the tight scale and
// the readable numbers.
func axisScale(lo, hi float64, want int) (float64, float64, []float64) {
	if want < 1 {
		want = 1
	}
	span := hi - lo
	if span <= 0 {
		// A series that never moved still needs a scale to sit on.
		span = math.Max(math.Abs(hi)*0.1, 1)
		lo, hi = hi-span/2, hi+span/2
		span = hi - lo
	}
	pad := span * 0.08
	lo, hi = lo-pad, hi+pad
	// Fitting the axis to the data means some clean steps leave barely a
	// line inside it, and a chart with one gridline gives the eye nothing
	// to read the far end against. Step down through the clean values
	// until enough of them land in range.
	step := niceStep((hi - lo) / float64(want))
	ticks := ticksIn(lo, hi, step)
	for i := 0; i < 4 && len(ticks) < 3; i++ {
		step = smallerNiceStep(step)
		ticks = ticksIn(lo, hi, step)
	}
	return lo, hi, ticks
}

func ticksIn(lo, hi, step float64) []float64 {
	if step <= 0 {
		return nil
	}
	var out []float64
	for v := math.Ceil(lo/step) * step; v <= hi+step/1000 && len(out) < 12; v += step {
		out = append(out, v)
	}
	return out
}

// smallerNiceStep walks one place down the 1-2-5 sequence: 5→2, 2→1, 1→5
// of the decade below.
func smallerNiceStep(step float64) float64 {
	if step <= 0 {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(step)))
	switch n := math.Round(step / mag); n {
	case 5:
		return 2 * mag
	case 2:
		return 1 * mag
	default:
		return 5 * mag / 10
	}
}

// valueRange is the span a sparkline covers: padded, because it has no
// axis to anchor it and the line must not run along an edge. A series that
// never moved still gets a band, so a flat price reads as flat rather than
// as a divide-by-zero.
func valueRange(pts []numericPoint) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		v := *p.point.Num
		lo, hi = math.Min(lo, v), math.Max(hi, v)
		if p.point.Low != nil {
			lo = math.Min(lo, *p.point.Low)
		}
		if p.point.High != nil {
			hi = math.Max(hi, *p.point.High)
		}
	}
	if math.IsInf(lo, 1) {
		return 0, 1
	}
	if hi == lo {
		pad := math.Abs(hi) * 0.05
		if pad == 0 {
			pad = 1
		}
		return lo - pad, hi + pad
	}
	pad := (hi - lo) * 0.12
	return lo - pad, hi + pad
}

// compactNum renders a value for an axis or a label: in full below 10,000
// and compact above it, one style per axis as the tick rules require.
func compactNum(v float64, compact bool) string {
	neg := ""
	if v < 0 {
		neg, v = "-", -v
	}
	if !compact {
		return neg + groupThousands(v)
	}
	switch {
	case v >= 1e9:
		return neg + trimZero(fmt.Sprintf("%.1f", v/1e9)) + "B"
	case v >= 1e6:
		return neg + trimZero(fmt.Sprintf("%.1f", v/1e6)) + "M"
	case v >= 1e3:
		return neg + trimZero(fmt.Sprintf("%.1f", v/1e3)) + "K"
	}
	return neg + groupThousands(v)
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

func groupThousands(v float64) string {
	s := fmt.Sprintf("%.0f", v)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// sparkline draws a listing's recent trend inside a table cell: a bare 2px
// line with no axes and no markers, because the cell already states the
// current value in text beside it. The earlier span is drawn in the
// de-emphasis hue and the latest step in the accent, which marks "now"
// without spending a dot on it.
//
// Its detail is deliberately not on hover: the cell is a link to the
// listing's own page, where the series has a crosshair, direct labels and
// a table. The summary here is on the SVG's title so it is reachable
// without following the link.
func sparkline(points []jobs.HistoryPoint, w, h int) template.HTML {
	pts := numericPoints(points)
	if len(pts) < 2 {
		return "" // one value is not a trend
	}
	lo, hi := valueRange(pts)
	pad := 2.0 // room for the stroke's round cap
	innerW, innerH := float64(w)-2*pad, float64(h)-2*pad
	last := float64(len(points) - 1)
	if last <= 0 {
		last = 1
	}
	x := func(i int) float64 { return pad + float64(i)/last*innerW }
	y := func(v float64) float64 { return pad + (hi-v)/(hi-lo)*innerH }

	coords := make([]string, 0, len(pts))
	for _, p := range pts {
		coords = append(coords, fmt.Sprintf("%.1f,%.1f", x(p.i), y(*p.point.Num)))
	}
	first, lastV := *pts[0].point.Num, *pts[len(pts)-1].point.Num
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="cmp-spark" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s" focusable="false">`,
		w, h, w, h, template.HTMLEscapeString(sparkSummary(pts, first, lastV)))
	fmt.Fprintf(&b, `<title>%s</title>`, template.HTMLEscapeString(sparkSummary(pts, first, lastV)))
	fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" points="%s"/>`,
		chartSeriesDim, strings.Join(coords, " "))
	// The latest step in the accent hue.
	fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" points="%s"/>`,
		chartSeries, strings.Join(coords[len(coords)-2:], " "))
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func sparkSummary(pts []numericPoint, first, last float64) string {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		lo, hi = math.Min(lo, *p.point.Num), math.Max(hi, *p.point.Num)
	}
	compact := hi >= 10000
	dir := "unchanged"
	switch {
	case last > first:
		dir = "up"
	case last < first:
		dir = "down"
	}
	return fmt.Sprintf("%d recorded values, %s overall: now %s, between %s and %s",
		len(pts), dir, compactNum(last, compact), compactNum(lo, compact), compactNum(hi, compact))
}

// listingChart draws one listing's tracked value over time: the series as a
// 2px line, a 10%% wash between the low and high of any day that raw runs
// were compacted into, hairline gridlines, and direct labels on just the
// endpoint and the cheapest point.
//
// Every point also gets a transparent, full-height hit band at least 24px
// wide, carrying its value on data attributes. The template's script turns
// those into a crosshair and a tooltip on hover and on keyboard focus;
// nothing is gated behind them, because the same values are in the table
// below the chart.
func listingChart(h *jobs.ListingHistory, width, height int) template.HTML {
	pts := numericPoints(h.Points)
	if len(pts) < 2 {
		return ""
	}
	const padL, padR, padT, padB = 64, 18, 14, 30
	plotW := float64(width - padL - padR)
	plotH := float64(height - padT - padB)
	rawLo, rawHi, _ := dataRange(pts)
	lo, hi, ticks := axisScale(rawLo, rawHi, 4)
	compact := hi >= 10000
	last := float64(len(h.Points) - 1)
	if last <= 0 {
		last = 1
	}
	x := func(i int) float64 { return padL + float64(i)/last*plotW }
	y := func(v float64) float64 { return padT + (hi-v)/(hi-lo)*plotH }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="lst-chart" width="100%%" viewBox="0 0 %d %d" role="img" aria-label="%s">`,
		width, height, template.HTMLEscapeString(chartSummary(h, pts, compact)))
	fmt.Fprintf(&b, `<title>%s</title>`, template.HTMLEscapeString(chartSummary(h, pts, compact)))

	// Gridlines, hairline and solid, with the ticks they carry.
	for _, v := range ticks {
		gy := y(v)
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1"/>`,
			padL, gy, padL+plotW, gy, chartGrid)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" text-anchor="end" font-size="10" fill="%s">%s</text>`,
			padL-8, gy+3, chartAxisText, template.HTMLEscapeString(compactNum(v, compact)))
	}

	// The low–high wash, over each unbroken run of compacted days.
	for _, seg := range bandSegments(pts) {
		var fwd, back []string
		for _, p := range seg {
			fwd = append(fwd, fmt.Sprintf("%.1f,%.1f", x(p.i), y(*p.point.High)))
		}
		for i := len(seg) - 1; i >= 0; i-- {
			back = append(back, fmt.Sprintf("%.1f,%.1f", x(seg[i].i), y(*seg[i].point.Low)))
		}
		fmt.Fprintf(&b, `<polygon fill="%s" fill-opacity="0.1" points="%s"/>`,
			chartSeries, strings.Join(append(fwd, back...), " "))
	}

	coords := make([]string, 0, len(pts))
	for _, p := range pts {
		coords = append(coords, fmt.Sprintf("%.1f,%.1f", x(p.i), y(*p.point.Num)))
	}
	fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" points="%s"/>`,
		chartSeries, strings.Join(coords, " "))

	// Direct labels, selectively: the endpoint, and the cheapest point when
	// it is not the endpoint. Everything else is carried by the ticks, the
	// hover readout and the table.
	lowest := pts[0]
	for _, p := range pts {
		if *p.point.Num < *lowest.point.Num {
			lowest = p
		}
	}
	end := pts[len(pts)-1]
	// The end marker: 8px across, ringed in the surface colour so it stays
	// legible where it sits on the line.
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="4" fill="%s" stroke="%s" stroke-width="2"/>`,
		x(end.i), y(*end.point.Num), chartSeries, chartSurface)
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="end" font-size="11" font-weight="600" fill="%s">%s</text>`,
		x(end.i)-8, y(*end.point.Num)-8, chartAxisText, template.HTMLEscapeString(compactNum(*end.point.Num, compact)))
	if lowest.i != end.i {
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="3" fill="%s" stroke="%s" stroke-width="2"/>`,
			x(lowest.i), y(*lowest.point.Num), chartSeries, chartSurface)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle" font-size="10" fill="%s">low %s</text>`,
			x(lowest.i), y(*lowest.point.Num)+14, chartAxisText, template.HTMLEscapeString(compactNum(*lowest.point.Num, compact)))
	}

	// Dates: the first, the middle and the last, so the labels never collide.
	for _, i := range []int{0, len(h.Points) / 2, len(h.Points) - 1} {
		anchor := "middle"
		if i == 0 {
			anchor = "start"
		} else if i == len(h.Points)-1 {
			anchor = "end"
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" text-anchor="%s" font-size="10" fill="%s">%s</text>`,
			x(i), height-10, anchor, chartAxisText, h.Points[i].At.Format("2 Jan"))
	}

	// The hover and focus layer: one band per point, never narrower than
	// 24px, so the pointer only has to be nearest rather than dead on.
	step := plotW / last
	bandW := math.Max(step, 24)
	for _, p := range pts {
		cx := x(p.i)
		unit := h.Currency
		fmt.Fprintf(&b,
			`<rect class="lst-hit" x="%.1f" y="%d" width="%.1f" height="%.1f" fill="transparent" tabindex="0" role="img"`+
				` data-x="%.1f" data-at="%s" data-value="%s" data-unit="%s" data-note="%s" aria-label="%s: %s %s"><title>%s: %s %s</title></rect>`,
			cx-bandW/2, padT, bandW, plotH,
			cx,
			template.HTMLEscapeString(p.point.At.Format("2 Jan 2006")),
			template.HTMLEscapeString(compactNum(*p.point.Num, compact)),
			template.HTMLEscapeString(unit),
			template.HTMLEscapeString(pointNote(p.point, compact)),
			template.HTMLEscapeString(p.point.At.Format("2 Jan 2006")),
			template.HTMLEscapeString(compactNum(*p.point.Num, compact)),
			template.HTMLEscapeString(unit),
			template.HTMLEscapeString(p.point.At.Format("2 Jan 2006")),
			template.HTMLEscapeString(compactNum(*p.point.Num, compact)),
			template.HTMLEscapeString(unit),
		)
	}
	fmt.Fprintf(&b, `<line class="lst-cross" x1="0" y1="%d" x2="0" y2="%.1f" stroke="%s" stroke-width="1" style="display:none"/>`,
		padT, padT+plotH, chartAxisText)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// pointNote says what a compacted point stands for, so a day folded from
// several runs is not read as a single reading.
func pointNote(p jobs.HistoryPoint, compact bool) string {
	if !p.Rolled {
		return ""
	}
	if p.Low != nil && p.High != nil && *p.High > *p.Low {
		return fmt.Sprintf("day's close · ranged %s–%s over %d runs",
			compactNum(*p.Low, compact), compactNum(*p.High, compact), p.Samples)
	}
	return fmt.Sprintf("day's close · %d runs", p.Samples)
}

// bandSegments groups the compacted points into unbroken runs, so the wash
// is drawn only where a low and a high are actually known and never
// bridges a gap of raw points.
func bandSegments(pts []numericPoint) [][]numericPoint {
	var out [][]numericPoint
	var cur []numericPoint
	for _, p := range pts {
		if p.point.Low != nil && p.point.High != nil {
			cur = append(cur, p)
			continue
		}
		if len(cur) > 1 {
			out = append(out, cur)
		}
		cur = nil
	}
	if len(cur) > 1 {
		out = append(out, cur)
	}
	return out
}

func chartSummary(h *jobs.ListingHistory, pts []numericPoint, compact bool) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("%s at %s over %d recorded values",
		h.Field, h.Listing.SiteLabel, len(pts)))
	if h.Current != nil {
		parts = append(parts, "now "+compactNum(*h.Current, compact)+" "+h.Currency)
	}
	if h.Low != nil && h.High != nil {
		parts = append(parts, fmt.Sprintf("between %s and %s", compactNum(*h.Low, compact), compactNum(*h.High, compact)))
	}
	return strings.Join(parts, ", ")
}

// chartFuncs are the visualization helpers the templates use.
var chartFuncs = template.FuncMap{
	"sparkline":    sparkline,
	"listingChart": listingChart,
	"compactNum":   func(v float64) string { return compactNum(v, v >= 10000 || v <= -10000) },
	"money": func(v *float64, currency string) string {
		if v == nil {
			return "—"
		}
		s := compactNum(*v, math.Abs(*v) >= 10000)
		if currency != "" {
			return s + " " + currency
		}
		return s
	},
	// spread labels a low–high range. Compact notation can round both ends
	// to the same text ("1.6B–1.6B"), which reads as a mistake rather than
	// as a range, so it falls back to the full numbers when that happens.
	"spread": func(low, high *float64) string {
		if low == nil || high == nil {
			return ""
		}
		compact := math.Abs(*high) >= 10000
		l, h := compactNum(*low, compact), compactNum(*high, compact)
		if l != h {
			return l + "–" + h
		}
		if full, fullHigh := groupThousands(*low), groupThousands(*high); full != fullHigh {
			return full + "–" + fullHigh
		}
		return l
	},
	"day": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Format("2 Jan 2006, 15:04")
	},
	// ago_t is "ago" for a time value rather than a pointer: the chart and
	// cell structs carry times by value.
	"ago_t": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		if d := time.Since(t); d >= 0 {
			return humanDuration(d) + " ago"
		}
		return "just now"
	},
	"since": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		if d := time.Since(t); d >= 0 {
			return humanDuration(d) + " ago"
		}
		return "just now"
	},
	// deltaAbs and deltaPct are always shown beside a ▲/▼ glyph and words,
	// never as colour alone — the status pair's CVD separation sits in the
	// floor band, where a second encoding is required.
	"deltaAbs": func(from, to *float64) float64 {
		if from == nil || to == nil {
			return 0
		}
		return *to - *from
	},
	"deltaPct": func(from, to *float64) string {
		if from == nil || to == nil || *from == 0 {
			return ""
		}
		return fmt.Sprintf("%+.1f%%", (*to-*from)/math.Abs(*from)*100)
	},
}
