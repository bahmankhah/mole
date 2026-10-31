package jobs

import (
	"net/url"
	"strings"

	"github.com/resolver/crawler/modules"
)

// maxSearchTerms bounds the words of a page search; each adds a condition.
const maxSearchTerms = 8

// pageSearchCondition builds the WHERE clause for a title/URL search of a
// job's pages. Every word must appear (in any order) in the title, the URL
// or the final URL, so "گوشی سامسونگ" finds "گوشی موبایل سامسونگ" and
// the slug "goshi-samsung" style URLs alike. A word is matched in its Persian
// and Arabic letter forms (ی/ي, ک/ك) and, for URLs, also percent-encoded,
// since sites like Divar store non-ASCII slugs as %D9%85…. It returns "" for
// an empty query.
func pageSearchCondition(q string) (string, []interface{}) {
	words := strings.Fields(q)
	if len(words) > maxSearchTerms {
		words = words[:maxSearchTerms]
	}
	var clauses []string
	var args []interface{}
	for _, w := range words {
		var ors []string
		for _, form := range letterForms(w) {
			p := likePattern(form)
			ors = append(ors, "title LIKE ?", "url LIKE ?", "final_url LIKE ?")
			args = append(args, p, p, p)
			if enc := url.PathEscape(form); enc != form {
				ors = append(ors, "url LIKE ?", "final_url LIKE ?")
				args = append(args, likePattern(enc), likePattern(enc))
			}
		}
		clauses = append(clauses, "("+strings.Join(ors, " OR ")+")")
	}
	return strings.Join(clauses, " AND "), args
}

// letterForms returns w with Persian letters and with their Arabic look-alikes.
func letterForms(w string) []string {
	persian := modules.NormalizePersian(w)
	if persian == "" {
		persian = w
	}
	arabic := strings.NewReplacer("ی", "ي", "ک", "ك").Replace(persian)
	if arabic == persian {
		return []string{persian}
	}
	return []string{persian, arabic}
}

// likePattern makes a "contains" LIKE pattern, escaping LIKE's wildcards.
func likePattern(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
}
