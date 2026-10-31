package content

import (
	"fmt"
	"math"
	"sync"
)

// TemplateIndex learns, during one crawl, what the site's page templates
// look like. Pages built from the same template share their markup (class
// names, body classes, framework route) even when their URLs share nothing,
// so a page with weak signals, such as an out-of-stock product without a buy
// button, takes the type of the confidently classified pages it resembles.
//
// Features present on almost every page (header, footer, menus) say nothing
// about the template and are ignored. It is safe for concurrent use.
type TemplateIndex struct {
	mu        sync.Mutex
	df        map[string]int // pages each feature appeared on
	pages     int
	exemplars map[string][][]string // page type → sorted feature sets of confident pages
	next      map[string]int        // ring position per type
}

const (
	maxExemplars      = 30    // per type
	maxTemplateDF     = 50000 // distinct features tracked
	minTemplatePages  = 8     // pages seen before features can be judged sitewide
	sitewideShare     = 0.85  // features on more pages than this are ignored
	minTemplateSim    = 0.5
	minTemplateMargin = 0.1
	maxTemplatePoints = 10
)

// NewTemplateIndex returns an empty index.
func NewTemplateIndex() *TemplateIndex {
	return &TemplateIndex{df: map[string]int{}, exemplars: map[string][][]string{}, next: map[string]int{}}
}

// Observe counts a page's features and keeps a confidently classified page as
// an exemplar of its type. features must be sorted (Page.Features is).
func (t *TemplateIndex) Observe(features []string, c Classification) {
	if t == nil || len(features) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pages++
	for _, f := range features {
		if _, ok := t.df[f]; ok || len(t.df) < maxTemplateDF {
			t.df[f]++
		}
	}
	if c.Confidence != ConfidenceHigh {
		return
	}
	list := t.exemplars[c.Type]
	if len(list) < maxExemplars {
		t.exemplars[c.Type] = append(list, features)
		return
	}
	i := t.next[c.Type] % maxExemplars
	list[i] = features
	t.next[c.Type] = i + 1
}

// Match returns the type whose exemplars the features resemble most and the
// similarity (Jaccard over non-sitewide features, 0..1), or "" when there
// is too little data or no type stands out.
func (t *TemplateIndex) Match(features []string) (string, float64) {
	if t == nil || len(features) == 0 {
		return "", 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pages < minTemplatePages {
		return "", 0
	}
	limit := int(math.Ceil(sitewideShare * float64(t.pages)))
	distinctive := func(f string) bool { return t.df[f] <= limit }
	best, second := 0.0, 0.0
	bestType := ""
	for typ, list := range t.exemplars {
		top := 0.0
		for _, ex := range list {
			if sim := jaccard(features, ex, distinctive); sim > top {
				top = sim
			}
		}
		switch {
		case top > best:
			second, best, bestType = best, top, typ
		case top > second:
			second = top
		}
	}
	if best < minTemplateSim || best-second < minTemplateMargin {
		return "", best
	}
	return bestType, best
}

// jaccard compares two sorted feature lists, counting only kept features.
func jaccard(a, b []string, keep func(string) bool) float64 {
	inter, union := 0, 0
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j >= len(b) || (i < len(a) && a[i] < b[j]):
			if keep(a[i]) {
				union++
			}
			i++
		case i >= len(a) || b[j] < a[i]:
			if keep(b[j]) {
				union++
			}
			j++
		default:
			if keep(a[i]) {
				union++
				inter++
			}
			i++
			j++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// ClassifyWith classifies the page and, with an index, adds the evidence of
// similar pages of the same crawl; the page is then recorded in the index.
// Only the page's own evidence decides whether it becomes an exemplar, so a
// template guess never teaches the index.
func (p *Page) ClassifyWith(idx *TemplateIndex) Classification {
	s := p.score()
	own := s.result()
	if idx == nil {
		return own
	}
	features := p.Features()
	if typ, sim := idx.Match(features); typ != "" {
		points := int(math.Round(sim * maxTemplatePoints))
		s.add(typ, points, fmt.Sprintf("template:%s(%.2f)", typ, sim))
	}
	idx.Observe(features, own)
	return s.result()
}
