package modules

import (
	"fmt"
	"strings"
)

const (
	diffMaxLines      = 20000
	diffMaxEditDist   = 2000
	diffDefaultMaxOut = 2000
	diffOpEqual       = 0
	diffOpDelete      = 1
	diffOpInsert      = 2
)

// DiffResult summarizes a line diff.
type DiffResult struct {
	Added     int    `json:"added"`     // number of added lines
	Removed   int    `json:"removed"`   // number of removed lines
	Unified   string `json:"unified"`   // unified-style hunks ("@@ -a,b +c,d @@", lines prefixed with ' ', '+', '-')
	Truncated bool   `json:"truncated"` // true when the output was cut at maxOutputLines or inputs were too large for a full diff
}

type diffOp struct {
	kind   int
	oi, ni int // line index in old/new; for inserts oi is the insertion point, for deletes ni is
}

// LineDiff compares oldText and newText line by line. contextLines is the number of
// unchanged lines shown around each change (like diff -U). maxOutputLines caps the
// Unified output (0 = 2000).
func LineDiff(oldText, newText string, contextLines, maxOutputLines int) DiffResult {
	if contextLines < 0 {
		contextLines = 0
	}
	if maxOutputLines <= 0 {
		maxOutputLines = diffDefaultMaxOut
	}
	a, b := splitDiffLines(oldText), splitDiffLines(newText)
	if len(a) == len(b) {
		same := true
		for i := range a {
			if a[i] != b[i] {
				same = false
				break
			}
		}
		if same {
			return DiffResult{}
		}
	}
	if len(a) > diffMaxLines || len(b) > diffMaxLines {
		return approximateDiff(a, b)
	}
	ops, ok := myersDiff(a, b)
	if !ok {
		return approximateDiff(a, b)
	}

	var res DiffResult
	for _, op := range ops {
		switch op.kind {
		case diffOpInsert:
			res.Added++
		case diffOpDelete:
			res.Removed++
		}
	}
	res.Unified, res.Truncated = formatUnified(ops, a, b, contextLines, maxOutputLines)
	return res
}

func splitDiffLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// approximateDiff estimates added/removed counts from line multisets when a full
// diff would be too expensive.
func approximateDiff(a, b []string) DiffResult {
	counts := make(map[string]int, len(a))
	for _, l := range a {
		counts[l]++
	}
	for _, l := range b {
		counts[l]--
	}
	var res DiffResult
	for _, c := range counts {
		if c > 0 {
			res.Removed += c
		} else {
			res.Added -= c
		}
	}
	res.Truncated = true
	res.Unified = fmt.Sprintf("@@ diff too large (old %d lines, new %d lines); counts are approximate @@\n", len(a), len(b))
	return res
}

// myersDiff computes a shortest edit script with Myers' O(ND) algorithm. It returns
// false if the edit distance exceeds diffMaxEditDist.
func myersDiff(aLines, bLines []string) ([]diffOp, bool) {
	// Intern lines so comparisons are integer compares.
	ids := make(map[string]int32, len(aLines)+len(bLines))
	intern := func(lines []string) []int32 {
		out := make([]int32, len(lines))
		for i, l := range lines {
			id, ok := ids[l]
			if !ok {
				id = int32(len(ids))
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	a, b := intern(aLines), intern(bLines)

	// Strip common prefix and suffix.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	n, m := len(ma), len(mb)

	ops := make([]diffOp, 0, len(a)+len(b)-pre-suf)
	for i := 0; i < pre; i++ {
		ops = append(ops, diffOp{diffOpEqual, i, i})
	}

	maxD := n + m
	if maxD > diffMaxEditDist {
		maxD = diffMaxEditDist
	}
	offset := n + m + 1
	v := make([]int32, 2*offset+1)
	var trace [][]int32 // trace[d][k+d] = furthest x on diagonal k after step d
	found := n == 0 && m == 0
	for d := 0; d <= maxD && !found; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = int(v[offset+k+1])
			} else {
				x = int(v[offset+k-1]) + 1
			}
			y := x - k
			for x < n && y < m && ma[x] == mb[y] {
				x++
				y++
			}
			v[offset+k] = int32(x)
			if x >= n && y >= m {
				found = true
			}
		}
		snap := make([]int32, 2*d+1)
		copy(snap, v[offset-d:offset+d+1])
		trace = append(trace, snap)
	}
	if !found {
		return nil, false
	}

	// Backtrack from (n, m) to build the middle section in reverse.
	var mid []diffOp
	x, y := n, m
	for d := len(trace) - 1; d > 0; d-- {
		prev := trace[d-1]
		k := x - y
		var prevK int
		if k == -d || (k != d && prev[k-1+d-1] < prev[k+1+d-1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := int(prev[prevK+d-1])
		prevY := prevX - prevK
		for x > prevX && y > prevY && (prevK == k-1 && x > prevX+1 || prevK == k+1 && y > prevY+1) {
			x--
			y--
			mid = append(mid, diffOp{diffOpEqual, pre + x, pre + y})
		}
		if prevK == k+1 {
			mid = append(mid, diffOp{diffOpInsert, pre + prevX, pre + prevY})
		} else {
			mid = append(mid, diffOp{diffOpDelete, pre + prevX, pre + prevY})
		}
		x, y = prevX, prevY
	}
	for x > 0 && y > 0 {
		x--
		y--
		mid = append(mid, diffOp{diffOpEqual, pre + x, pre + y})
	}
	for i := len(mid) - 1; i >= 0; i-- {
		ops = append(ops, mid[i])
	}
	for i := 0; i < suf; i++ {
		ops = append(ops, diffOp{diffOpEqual, pre + n + i, pre + m + i})
	}
	return ops, true
}

// formatUnified renders ops as unified hunks, returning true if output was capped.
func formatUnified(ops []diffOp, a, b []string, ctx, maxLines int) (string, bool) {
	var sb strings.Builder
	written := 0
	for i := 0; i < len(ops); {
		if ops[i].kind == diffOpEqual {
			i++
			continue
		}
		// Extend the hunk while the gap between changes is at most 2*ctx.
		start := i - ctx
		if start < 0 {
			start = 0
		}
		end := i
		for j := i; j < len(ops); j++ {
			if ops[j].kind != diffOpEqual {
				end = j
			} else if j-end > 2*ctx {
				break
			}
		}
		stop := end + ctx + 1
		if stop > len(ops) {
			stop = len(ops)
		}

		oldCount, newCount := 0, 0
		for _, op := range ops[start:stop] {
			if op.kind != diffOpInsert {
				oldCount++
			}
			if op.kind != diffOpDelete {
				newCount++
			}
		}
		oldStart, newStart := ops[start].oi, ops[start].ni
		if oldCount > 0 {
			oldStart++
		}
		if newCount > 0 {
			newStart++
		}
		if written >= maxLines {
			return sb.String(), true
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		written++
		for _, op := range ops[start:stop] {
			if written >= maxLines {
				return sb.String(), true
			}
			switch op.kind {
			case diffOpEqual:
				sb.WriteString(" " + a[op.oi] + "\n")
			case diffOpDelete:
				sb.WriteString("-" + a[op.oi] + "\n")
			case diffOpInsert:
				sb.WriteString("+" + b[op.ni] + "\n")
			}
			written++
		}
		i = stop
	}
	return sb.String(), false
}
