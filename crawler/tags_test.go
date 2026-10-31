package crawler

import (
	"reflect"
	"strings"
	"testing"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/models"
)

func TestTagPage(t *testing.T) {
	rules, err := CompileTagRules([]models.TagRule{
		{Pattern: `/gsp-\d+/`, Tag: "product"},
		{Pattern: `/garden-light/`, Tag: "Garden Light"},
		{Pattern: `/blog/`, Tag: "article"},
		{Pattern: `\?sale=1`, Tag: "sale"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		url, final, detected string
		wantType             string
		wantTags             []string
	}{
		// Detection alone: the type is the tag.
		{"https://shop.ir/about", "", models.PageTypeBusiness, models.PageTypeBusiness, []string{"business"}},
		// "other" and unknown types are not tags.
		{"https://shop.ir/x", "", models.PageTypeOther, models.PageTypeOther, nil},
		{"https://shop.ir/x", "", "", "", nil},
		// A page-type rule overrides detection; custom tags add up.
		{"https://shop.ir/garden-light/gsp-7110/x?sale=1", "", models.PageTypeOther, models.PageTypeProduct, []string{"product", "garden-light", "sale"}},
		// The detected type stays a tag when no rule changes it.
		{"https://shop.ir/garden-light/12554-x.html", "", models.PageTypeProduct, models.PageTypeProduct, []string{"product", "garden-light"}},
		// The final URL after a redirect matches too.
		{"https://shop.ir/p?id=1", "https://shop.ir/blog/post", models.PageTypeListing, models.PageTypeArticle, []string{"article"}},
	}
	for _, c := range cases {
		typ, tags := TagPage(rules, c.url, c.final, c.detected)
		if typ != c.wantType || !reflect.DeepEqual(tags, c.wantTags) {
			t.Errorf("TagPage(%s, %s) = %q %v, want %q %v", c.url, c.detected, typ, tags, c.wantType, c.wantTags)
		}
	}
}

func TestTagRuleValidation(t *testing.T) {
	for _, bad := range [][]models.TagRule{
		{{Pattern: "(", Tag: "x"}},
		{{Pattern: "/x/", Tag: ""}},
		{{Pattern: "", Tag: "x"}},
		{{Pattern: "/x/", Tag: "a,b"}},
		{{Pattern: "/x/", Tag: strings.Repeat("x", models.MaxTagLength+1)}},
	} {
		if _, err := CompileTagRules(bad); err == nil {
			t.Errorf("rules %+v accepted", bad)
		}
	}
}

func TestKeepTags(t *testing.T) {
	cfg := &config.Config{}
	build := func(s *models.JobSettings) (*RunSettings, error) { return BuildRunSettings(cfg, s, nil, nil) }

	rs, err := build(&models.JobSettings{})
	if err != nil || !rs.Keeps(nil) {
		t.Fatalf("no keep_tags keeps everything: %v", err)
	}

	// "auto" keeps the types of the enabled detectors.
	rs, err = build(&models.JobSettings{Formats: []string{"markdown", "product", "article", "faq"}, KeepTags: []string{"auto", "Summer Sale"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"product": true, "article": true, "summer-sale": true}
	if !reflect.DeepEqual(rs.KeepTags, want) {
		t.Errorf("keep tags = %v, want %v", rs.KeepTags, want)
	}
	if !rs.Keeps([]string{"listing", "summer-sale"}) || rs.Keeps([]string{"listing"}) || rs.Keeps(nil) {
		t.Error("Keeps")
	}
	if _, err := build(&models.JobSettings{KeepTags: []string{"auto"}}); err == nil {
		t.Error(`"auto" without a detector accepted`)
	}
	if _, err := build(&models.JobSettings{MaxKeptPages: intp(-1)}); err == nil {
		t.Error("negative max_kept_pages accepted")
	}
}

func intp(v int) *int { return &v }
