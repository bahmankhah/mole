package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

func jsonOutput() bool { return flags.output == "json" }

func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// table prints aligned columns to stdout.
type table struct{ w *tabwriter.Writer }

func newTable(headers ...string) *table {
	t := &table{w: tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)}
	t.row(toAny(headers)...)
	return t
}

func toAny(s []string) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func (t *table) row(cols ...interface{}) {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = fmt.Sprint(c)
	}
	fmt.Fprintln(t.w, strings.Join(parts, "\t"))
}

func (t *table) flush() { t.w.Flush() }

func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func fmtTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func fmtBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
