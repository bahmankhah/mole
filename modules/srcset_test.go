package modules

import (
	"strings"
	"testing"
)

func TestSrcsetURLs(t *testing.T) {
	cases := []struct {
		name, srcset string
		want         []string
	}{
		{
			"plain candidates",
			"a.jpg 1x, b.jpg 2x",
			[]string{"a.jpg", "b.jpg"},
		},
		{
			"width descriptors",
			"small.jpg 100w, large.jpg 200w",
			[]string{"small.jpg", "large.jpg"},
		},
		{
			"single url, no descriptor",
			"only.jpg",
			[]string{"only.jpg"},
		},
		{
			// The Digikala case: commas inside the image-processing query
			// must not split the URL into fragments.
			"commas inside the url",
			"https://cdn.example/img.jpg?x-oss-process=image/resize,w_800/quality,q_80/format,webp 800w, " +
				"https://cdn.example/img.jpg?x-oss-process=image/resize,w_400/quality,q_80 400w",
			[]string{
				"https://cdn.example/img.jpg?x-oss-process=image/resize,w_800/quality,q_80/format,webp",
				"https://cdn.example/img.jpg?x-oss-process=image/resize,w_400/quality,q_80",
			},
		},
		{
			"comma directly after a url, no descriptor",
			"a.jpg,b.jpg",
			[]string{"a.jpg,b.jpg"}, // one token: no whitespace, so one candidate
		},
		{
			"trailing comma after url then next candidate",
			"a.jpg, b.jpg",
			[]string{"a.jpg", "b.jpg"},
		},
		{"empty", "   ", nil},
	}
	for _, c := range cases {
		got := srcsetURLs(c.srcset)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %d urls %v, want %d %v", c.name, len(got), got, len(c.want), c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: url %d = %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// TestSrcsetNoRelativeFragments is the regression this fixes: the pieces of
// a comma-bearing image URL must never be offered as relative links, or
// they resolve against the page and crawl the same page again and again.
func TestSrcsetNoRelativeFragments(t *testing.T) {
	srcset := "https://dkstatics-public.digikala.com/digikala-products/x.jpg?x-oss-process=image/resize,w_800/quality,q_80/format,webp 800w"
	for _, u := range srcsetURLs(srcset) {
		if !strings.HasPrefix(u, "http") {
			t.Errorf("relative fragment %q extracted from a srcset URL", u)
		}
	}
}
