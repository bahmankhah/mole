package content

// FAQItem is one question and its answer from a page's FAQ.
type FAQItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

const (
	maxFAQItems  = 100
	maxFAQAnswer = 4000
)

// detectFAQ reads the questions of a schema.org FAQPage (published by most
// SEO plugins for FAQ blocks), from the first syntax that has them. Yoast
// lists the questions as separate graph nodes referenced by @id.
func detectFAQ(pg *pageData) []FAQItem {
	for _, src := range pg.schemaSources() {
		var items []FAQItem
		seen := map[string]bool{}
		ids := indexIDs(src.items)
		for _, page := range findItems(src.items, ofTypes([]string{"FAQPage"}), nil) {
			for _, q := range schemaList(page["mainEntity"]) {
				qm, ok := ids.resolve(q).(map[string]interface{})
				if !ok || !hasJSONLDType(qm["@type"], "Question") {
					continue
				}
				question := cleanInline(schemaString(qm["name"]))
				answer := ""
				for _, key := range []string{"acceptedAnswer", "suggestedAnswer"} {
					if am, ok := firstMap(qm[key]); ok {
						if full, ok := ids.resolve(am).(map[string]interface{}); ok {
							am = full
						}
						if answer = cleanInline(schemaString(am["text"])); answer != "" {
							break
						}
					}
				}
				if question == "" || answer == "" || seen[question] {
					continue
				}
				seen[question] = true
				items = append(items, FAQItem{Question: question, Answer: truncateRunes(answer, maxFAQAnswer)})
				if len(items) == maxFAQItems {
					return items
				}
			}
		}
		if len(items) > 0 {
			return items
		}
	}
	return nil
}

func firstMap(v interface{}) (map[string]interface{}, bool) {
	for _, item := range schemaList(v) {
		if m, ok := item.(map[string]interface{}); ok {
			return m, true
		}
	}
	return nil, false
}
