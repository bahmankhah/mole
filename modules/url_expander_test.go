package modules

import (
	"reflect"
	"testing"
)

func TestExpandTemplateJSONBody(t *testing.T) {
	tmpl := `{"query":"{{Q}}","page":{{PAGE}}}`
	vars := map[string][]string{
		"Q":    {"foo", "bar"},
		"PAGE": {"1", "2"},
	}
	got, err := ExpandTemplate(tmpl, vars, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"query":"foo","page":1}`,
		`{"query":"foo","page":2}`,
		`{"query":"bar","page":1}`,
		`{"query":"bar","page":2}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExtractTemplateVarsFromBody(t *testing.T) {
	got := ExtractTemplateVars(`{"id":"{{ID}}","token":"{{TOKEN}}","id2":"{{ID}}"}`)
	want := []string{"ID", "TOKEN"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRequestIdentityHashGETUnchanged(t *testing.T) {
	u := "https://example.com/page"
	a := RequestIdentityHash(u, "", "")
	b := RequestIdentityHash(u, "GET", "")
	if a != b {
		t.Fatal("GET and empty method should hash the URL the same way")
	}
	if RequestIdentityHash(u, "POST", `{"q":"1"}`) == a {
		t.Fatal("POST with body must not collapse onto the GET hash")
	}
	if RequestIdentityHash(u, "POST", `{"q":"1"}`) == RequestIdentityHash(u, "POST", `{"q":"2"}`) {
		t.Fatal("distinct POST bodies must produce distinct hashes")
	}
}
