package handlers

import (
	"html/template"
	"testing"
)

// The templates are parsed at boot with this FuncMap; a typo in one is a
// 500 on the page, so parse them all here instead.
func TestTemplatesParse(t *testing.T) {
	if _, err := template.New("").Funcs(TemplateFuncs).ParseGlob("../templates/*.html"); err != nil {
		t.Fatalf("templates do not parse: %v", err)
	}
}
