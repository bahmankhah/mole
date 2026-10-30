package jobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NextRuns returns the next n times a cron expression fires after from, in
// the schedule's time zone.
func NextRuns(expr, timezone string, from time.Time, n int) ([]time.Time, error) {
	out := make([]time.Time, 0, n)
	t := from
	for i := 0; i < n; i++ {
		next, err := NextRun(expr, timezone, t)
		if err != nil {
			return nil, err
		}
		out = append(out, next)
		t = next
	}
	return out, nil
}

var cronWeekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// DescribeCron puts the common cron shapes into words ("Daily at 03:00",
// "Every 6 hours", "Weekly on Saturday at 09:00"). It returns "" for
// expressions it does not recognise; show the expression itself then.
func DescribeCron(expr string) string {
	expr = strings.TrimSpace(expr)
	switch expr {
	case "@hourly":
		return "Every hour"
	case "@daily", "@midnight":
		return "Daily at 00:00"
	case "@weekly":
		return "Weekly on Sunday at 00:00"
	case "@monthly":
		return "Monthly on day 1 at 00:00"
	case "@yearly", "@annually":
		return "Yearly on 1 January at 00:00"
	}
	if d, ok := strings.CutPrefix(expr, "@every "); ok {
		if dur, err := time.ParseDuration(strings.TrimSpace(d)); err == nil {
			return "Every " + dur.String()
		}
		return ""
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return ""
	}
	minute, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]
	if month != "*" {
		return ""
	}
	if n, ok := cronStep(minute); ok && hour == "*" && dom == "*" && dow == "*" {
		return plural(n, "minute")
	}
	m, okM := cronNumber(minute, 0, 59)
	if !okM {
		return ""
	}
	if hour == "*" && dom == "*" && dow == "*" {
		if m == 0 {
			return "Every hour"
		}
		return fmt.Sprintf("Every hour at :%02d", m)
	}
	if n, ok := cronStep(hour); ok && dom == "*" && dow == "*" {
		s := plural(n, "hour")
		if m != 0 {
			s += fmt.Sprintf(" at :%02d", m)
		}
		return s
	}
	h, okH := cronNumber(hour, 0, 23)
	if !okH {
		return ""
	}
	at := fmt.Sprintf("%02d:%02d", h, m)
	switch {
	case dom == "*" && dow == "*":
		return "Daily at " + at
	case dom == "*":
		if d, ok := cronWeekday(dow); ok {
			return "Weekly on " + cronWeekdays[d] + " at " + at
		}
	case dow == "*":
		if d, ok := cronNumber(dom, 1, 31); ok {
			return fmt.Sprintf("Monthly on day %d at %s", d, at)
		}
	}
	return ""
}

func plural(n int, unit string) string {
	if n == 1 {
		return "Every " + unit
	}
	return fmt.Sprintf("Every %d %ss", n, unit)
}

func cronNumber(s string, min, max int) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= min && n <= max
}

// cronStep reads "*/N".
func cronStep(s string) (int, bool) {
	rest, ok := strings.CutPrefix(s, "*/")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n > 0
}

// cronWeekday reads one weekday: 0–7 (0 and 7 are Sunday) or SUN–SAT.
func cronWeekday(s string) (int, bool) {
	if n, ok := cronNumber(s, 0, 7); ok {
		return n % 7, true
	}
	for i, name := range cronWeekdays {
		if strings.EqualFold(s, name[:3]) {
			return i, true
		}
	}
	return 0, false
}
