package crawler

import (
	"testing"

	"github.com/resolver/crawler/models"
)

func TestExpandSeedRequestsGETDefault(t *testing.T) {
	method, bodies, err := expandSeedRequests(nil)
	if err != nil {
		t.Fatal(err)
	}
	if method != "GET" || bodies != nil {
		t.Fatalf("method=%s bodies=%v", method, bodies)
	}
}

func TestExpandSeedRequestsStaticPOST(t *testing.T) {
	m := "POST"
	b := `{"q":"hello"}`
	method, bodies, err := expandSeedRequests(&models.JobSettings{
		RequestMethod: &m,
		RequestBody:   &b,
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != "POST" || len(bodies) != 1 || bodies[0] != b {
		t.Fatalf("method=%s bodies=%v", method, bodies)
	}
}

func TestExpandSeedRequestsPOSTVars(t *testing.T) {
	m := "POST"
	b := `{"q":"{{Q}}"}`
	method, bodies, err := expandSeedRequests(&models.JobSettings{
		RequestMethod:   &m,
		RequestBody:     &b,
		RequestBodyVars: map[string]string{"Q": "foo,bar,1-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != "POST" {
		t.Fatalf("method=%s", method)
	}
	want := []string{
		`{"q":"foo"}`,
		`{"q":"bar"}`,
		`{"q":"1"}`,
		`{"q":"2"}`,
	}
	if len(bodies) != len(want) {
		t.Fatalf("got %v", bodies)
	}
	for i := range want {
		if bodies[i] != want[i] {
			t.Fatalf("idx %d got %s want %s", i, bodies[i], want[i])
		}
	}
}

func TestExpandSeedRequestsMissingVar(t *testing.T) {
	m := "POST"
	b := `{"q":"{{Q}}"}`
	_, _, err := expandSeedRequests(&models.JobSettings{
		RequestMethod: &m,
		RequestBody:   &b,
	})
	if err == nil {
		t.Fatal("expected error for missing payload variable")
	}
}

func TestIsTextualContentType(t *testing.T) {
	save := []string{
		"application/json",
		"application/json; charset=utf-8",
		"application/ld+json",
		"text/html; charset=utf-8",
		"text/plain",
		"application/xml",
		"application/vnd.api+json",
	}
	for _, ct := range save {
		if !isTextualContentType(ct) {
			t.Errorf("expected to save %q", ct)
		}
	}
	skip := []string{
		"image/png",
		"application/octet-stream",
		"application/pdf",
	}
	for _, ct := range skip {
		if isTextualContentType(ct) {
			t.Errorf("did not expect to save %q", ct)
		}
	}
}
