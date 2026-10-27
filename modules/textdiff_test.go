package modules

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func lines(n int, prefix string) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "%s%d\n", prefix, i)
	}
	return sb.String()
}

func TestLineDiff(t *testing.T) {
	tests := []struct {
		name           string
		old, new       string
		ctx            int
		added, removed int
		unified        string
	}{
		{
			name: "identical",
			old:  "a\nb\nc\n", new: "a\nb\nc\n", ctx: 3,
		},
		{
			name: "crlf and trailing newline ignored",
			old:  "a\r\nb\r\n", new: "a\nb", ctx: 3,
		},
		{
			name: "both empty",
			old:  "", new: "", ctx: 3,
		},
		{
			name: "empty old",
			old:  "", new: "a\nb\n", ctx: 3,
			added:   2,
			unified: "@@ -0,0 +1,2 @@\n+a\n+b\n",
		},
		{
			name: "empty new",
			old:  "a\nb\n", new: "", ctx: 3,
			removed: 2,
			unified: "@@ -1,2 +0,0 @@\n-a\n-b\n",
		},
		{
			name: "pure add at end",
			old:  "a\nb\n", new: "a\nb\nc\n", ctx: 1,
			added:   1,
			unified: "@@ -2,1 +2,2 @@\n b\n+c\n",
		},
		{
			name: "pure remove in middle",
			old:  "a\nb\nc\nd\ne\n", new: "a\nb\nd\ne\n", ctx: 1,
			removed: 1,
			unified: "@@ -2,3 +2,2 @@\n b\n-c\n d\n",
		},
		{
			name: "modification with context",
			old:  lines(10, "l"), new: strings.Replace(lines(10, "l"), "l5\n", "X\n", 1), ctx: 2,
			added: 1, removed: 1,
			unified: "@@ -3,5 +3,5 @@\n l3\n l4\n-l5\n+X\n l6\n l7\n",
		},
		{
			name: "zero context",
			old:  "a\nb\nc\n", new: "a\nB\nc\n", ctx: 0,
			added: 1, removed: 1,
			unified: "@@ -2,1 +2,1 @@\n-b\n+B\n",
		},
		{
			name:  "hunks merge when contexts overlap",
			old:   lines(10, "l"),
			new:   strings.NewReplacer("l3\n", "A\n", "l6\n", "B\n").Replace(lines(10, "l")),
			ctx:   1,
			added: 2, removed: 2,
			unified: "@@ -2,6 +2,6 @@\n l2\n-l3\n+A\n l4\n l5\n-l6\n+B\n l7\n",
		},
		{
			name:  "separate hunks",
			old:   lines(20, "l"),
			new:   strings.NewReplacer("l3\n", "A\n", "l15\n", "B\n").Replace(lines(20, "l")),
			ctx:   1,
			added: 2, removed: 2,
			unified: "@@ -2,3 +2,3 @@\n l2\n-l3\n+A\n l4\n" +
				"@@ -14,3 +14,3 @@\n l14\n-l15\n+B\n l16\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LineDiff(tt.old, tt.new, tt.ctx, 0)
			if got.Added != tt.added || got.Removed != tt.removed {
				t.Errorf("added/removed = %d/%d, want %d/%d", got.Added, got.Removed, tt.added, tt.removed)
			}
			if got.Unified != tt.unified {
				t.Errorf("unified =\n%s\nwant\n%s", got.Unified, tt.unified)
			}
			if got.Truncated {
				t.Errorf("unexpected truncation")
			}
		})
	}
}

func TestLineDiffInterleaved(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\n"
	new := "a\nx\nc\ny\nz\nf\ng\n"
	got := LineDiff(old, new, 3, 0)
	if got.Added != 4 || got.Removed != 3 {
		t.Errorf("added/removed = %d/%d, want 4/3", got.Added, got.Removed)
	}
	// Applying the unified output must reconstruct both sides.
	var o, n []string
	for _, l := range strings.Split(strings.TrimSuffix(got.Unified, "\n"), "\n") {
		switch l[0] {
		case ' ':
			o, n = append(o, l[1:]), append(n, l[1:])
		case '-':
			o = append(o, l[1:])
		case '+':
			n = append(n, l[1:])
		}
	}
	if strings.Join(o, "\n")+"\n" != old || strings.Join(n, "\n")+"\n" != new {
		t.Errorf("hunks do not reconstruct inputs:\n%s", got.Unified)
	}
}

func TestLineDiffOutputCap(t *testing.T) {
	got := LineDiff("", lines(100, "n"), 3, 10)
	if !got.Truncated {
		t.Error("expected Truncated")
	}
	if n := strings.Count(got.Unified, "\n"); n != 10 {
		t.Errorf("output has %d lines, want 10", n)
	}
	if got.Added != 100 {
		t.Errorf("Added = %d, want 100 (counts must not be capped)", got.Added)
	}
}

func TestLineDiffLargeFallback(t *testing.T) {
	tests := []struct {
		name     string
		old, new string
	}{
		{"too many lines", lines(25000, "a"), lines(25001, "a")},
		{"edit distance over budget", lines(15000, "a"), lines(15000, "b")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			got := LineDiff(tt.old, tt.new, 3, 0)
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("took %v", d)
			}
			if !got.Truncated {
				t.Error("expected Truncated")
			}
			if got.Added == 0 {
				t.Error("expected approximate counts")
			}
		})
	}
}

func TestLineDiffLargeSmallChange(t *testing.T) {
	old := lines(19000, "x")
	new := strings.Replace(old, "x9000\n", "changed\n", 1)
	start := time.Now()
	got := LineDiff(old, new, 3, 0)
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v", d)
	}
	if got.Truncated || got.Added != 1 || got.Removed != 1 {
		t.Errorf("got %+v", got)
	}
}
