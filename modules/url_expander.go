package modules

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// varPattern matches template variables like {{NAME}}
var varPattern = regexp.MustCompile(`\{\{(\w+)\}\}`)

// ExtractTemplateVars returns the distinct variable names found in a URL template.
// E.g. "https://example.com/page/{{N}}/{{cat}}" → ["N","cat"]
func ExtractTemplateVars(urlTemplate string) []string {
	matches := varPattern.FindAllStringSubmatch(urlTemplate, -1)
	seen := make(map[string]struct{})
	var names []string
	for _, m := range matches {
		name := m[1]
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	return names
}

// HasTemplateVars returns true if the URL contains any {{VAR}} placeholders.
func HasTemplateVars(urlTemplate string) bool {
	return varPattern.MatchString(urlTemplate)
}

// ExpandValueExpr expands a single value expression into individual values.
//
// Supported syntax (comma-separated, each token can be a literal or a range):
//
//	"1,hamster,5-10"  → ["1","hamster","5","6","7","8","9","10"]
//	"3-5"             → ["3","4","5"]
//	"hello"           → ["hello"]
//
// Ranges are inclusive on both ends: "2-5" = 2,3,4,5.
// Negative numbers or non-numeric tokens containing "-" are treated as literals.
func ExpandValueExpr(expr string) ([]string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}

	parts := strings.Split(expr, ",")
	var result []string

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Try to parse as a numeric range "A-B" where A and B are non-negative integers.
		if vals, ok := tryExpandRange(part); ok {
			result = append(result, vals...)
		} else {
			result = append(result, part)
		}
	}

	return result, nil
}

// tryExpandRange attempts to interpret s as "start-end" integer range.
// Returns (values, true) if successful, or (nil, false) if not a valid range.
func tryExpandRange(s string) ([]string, bool) {
	// Must contain exactly one "-" that is not at position 0 (not a negative number)
	idx := strings.LastIndex(s, "-")
	if idx <= 0 || idx == len(s)-1 {
		return nil, false
	}

	startStr := strings.TrimSpace(s[:idx])
	endStr := strings.TrimSpace(s[idx+1:])

	start, err1 := strconv.Atoi(startStr)
	end, err2 := strconv.Atoi(endStr)
	if err1 != nil || err2 != nil {
		return nil, false
	}

	if start > end {
		// Allow reverse ranges: 10-5 → 10,9,8,7,6,5
		var vals []string
		for i := start; i >= end; i-- {
			vals = append(vals, strconv.Itoa(i))
		}
		return vals, true
	}

	// Safety: cap at 10000 values to prevent accidental huge expansions
	if end-start > 10000 {
		return nil, false
	}

	vals := make([]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		vals = append(vals, strconv.Itoa(i))
	}
	return vals, true
}

// ExpandTemplateURL takes a URL template and a map of variable→values, and
// returns all expanded URLs by computing the Cartesian product of all variable
// values.
//
// Example:
//
//	template: "https://example.com/page/{{N}}/{{cat}}"
//	vars:     {"N": ["1","2"], "cat": ["a","b"]}
//	→ ["https://example.com/page/1/a",
//	   "https://example.com/page/1/b",
//	   "https://example.com/page/2/a",
//	   "https://example.com/page/2/b"]
//
// Returns an error if any referenced variable has no values, or if expansion
// would exceed maxURLs (safety limit).
func ExpandTemplateURL(template string, vars map[string][]string, maxURLs int) ([]string, error) {
	if maxURLs <= 0 {
		maxURLs = 50000
	}

	names := ExtractTemplateVars(template)
	if len(names) == 0 {
		return []string{template}, nil
	}

	// Validate all var names have values
	for _, name := range names {
		vals, ok := vars[name]
		if !ok || len(vals) == 0 {
			return nil, fmt.Errorf("variable {{%s}} has no values defined", name)
		}
	}

	// Compute total combinations
	total := 1
	for _, name := range names {
		total *= len(vars[name])
		if total > maxURLs {
			return nil, fmt.Errorf("expansion would produce %d+ URLs (limit %d)", total, maxURLs)
		}
	}

	// Cartesian product expansion
	results := make([]string, 0, total)
	var expand func(tpl string, varIdx int)
	expand = func(tpl string, varIdx int) {
		if varIdx >= len(names) {
			results = append(results, tpl)
			return
		}
		name := names[varIdx]
		placeholder := "{{" + name + "}}"
		for _, val := range vars[name] {
			replaced := strings.ReplaceAll(tpl, placeholder, val)
			expand(replaced, varIdx+1)
		}
	}
	expand(template, 0)

	return results, nil
}
