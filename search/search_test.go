package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/resolver/crawler/config"
)

func TestSearXNG(t *testing.T) {
	var langs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			http.Error(w, "bad request", 400)
			return
		}
		langs = append(langs, r.URL.Query().Get("language"))
		page := r.URL.Query().Get("pageno")
		fmt.Fprintf(w, `{"results":[
			{"url":"https://a.ir/%[1]s","title":"<b>A</b> %[1]s","content":"x &amp; y"},
			{"url":"https://a.ir/1","title":"dup","content":""},
			{"url":"javascript:alert(1)","title":"bad"}]}`, page)
	}))
	defer srv.Close()
	p, err := New(config.SearchConfig{Provider: "searxng", BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Search(context.Background(), Query{Query: "قیمت موبایل", Limit: 3, Lang: "fa", Country: "ir"})
	if err != nil {
		t.Fatal(err)
	}
	// Page 1 gives /1, page 2 gives /2 (its /1 is a duplicate), page 3 gives /3.
	if len(res) != 3 || res[0].URL != "https://a.ir/1" || res[1].URL != "https://a.ir/2" || res[2].Position != 3 {
		t.Fatalf("results = %+v", res)
	}
	if res[0].Title != "A 1" || res[0].Description != "x & y" {
		t.Errorf("markup not removed: %+v", res[0])
	}
	if langs[0] != "fa-IR" {
		t.Errorf("language = %q", langs[0])
	}
}

func TestBrave(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "k" {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		q := r.URL.Query()
		if q.Get("country") != "IR" || q.Get("search_lang") != "fa" || q.Get("count") != "2" {
			http.Error(w, "unexpected query "+r.URL.RawQuery, 400)
			return
		}
		off := q.Get("offset")
		fmt.Fprintf(w, `{"web":{"results":[{"url":"https://b.ir/%[1]s/1","title":"t","description":"<strong>d</strong>"},{"url":"https://b.ir/%[1]s/2","title":"t"}]}}`, off)
	}))
	defer srv.Close()
	p, _ := New(config.SearchConfig{Provider: "brave", APIKey: "k", BaseURL: srv.URL})
	res, err := p.Search(context.Background(), Query{Query: "x", Limit: 2, Lang: "fa", Country: "ir"})
	if err != nil || len(res) != 2 || res[0].Description != "d" {
		t.Fatalf("results = %+v, %v", res, err)
	}
	bad, _ := New(config.SearchConfig{Provider: "brave", APIKey: "wrong", BaseURL: srv.URL})
	if _, err := bad.Search(context.Background(), Query{Query: "x", Limit: 2}); err == nil {
		t.Fatal("HTTP 401 not reported")
	}
}

func TestSerper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if r.Method != http.MethodPost || r.Header.Get("X-API-KEY") != "k" || json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad", 400)
			return
		}
		if body["gl"] != "ir" || body["hl"] != "fa" || body["num"] != 5.0 {
			http.Error(w, fmt.Sprint(body), 400)
			return
		}
		fmt.Fprint(w, `{"organic":[{"link":"https://c.ir/","title":"C","snippet":"s","position":1}]}`)
	}))
	defer srv.Close()
	p, _ := New(config.SearchConfig{Provider: "serper", APIKey: "k", BaseURL: srv.URL})
	res, err := p.Search(context.Background(), Query{Query: "x", Limit: 5, Lang: "fa", Country: "IR"})
	if err != nil || len(res) != 1 || res[0].URL != "https://c.ir/" || res[0].Title != "C" {
		t.Fatalf("results = %+v, %v", res, err)
	}
}

func TestNew(t *testing.T) {
	if p, err := New(config.SearchConfig{}); p != nil || err != nil {
		t.Errorf("empty provider: %v %v", p, err)
	}
	if _, err := New(config.SearchConfig{Provider: "searxng"}); err == nil {
		t.Error("searxng without base_url accepted")
	}
	if _, err := New(config.SearchConfig{Provider: "brave", APIKey: "k", Proxy: "::bad"}); err == nil {
		t.Error("invalid proxy accepted")
	}
}
