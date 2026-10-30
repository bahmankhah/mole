package content

import (
	"net/url"
	"strings"
)

// JobPosting is the job advertised on a page (schema.org JobPosting, which
// job boards publish for search engines).
type JobPosting struct {
	Title           string            `json:"title,omitempty"`
	Description     string            `json:"description,omitempty"`
	Organization    string            `json:"organization,omitempty"`
	OrganizationURL string            `json:"organization_url,omitempty"`
	Location        string            `json:"location,omitempty"` // city, province, country; several locations are joined with "; "
	Remote          bool              `json:"remote,omitempty"`
	EmploymentTypes []string          `json:"employment_types,omitempty"` // full_time, part_time, contractor, temporary, intern, …
	DatePosted      string            `json:"date_posted,omitempty"`      // RFC3339
	ValidThrough    string            `json:"valid_through,omitempty"`    // RFC3339
	Salary          *Salary           `json:"salary,omitempty"`
	Experience      string            `json:"experience,omitempty"`
	Education       string            `json:"education,omitempty"`
	Identifier      string            `json:"identifier,omitempty"`
	URL             string            `json:"url,omitempty"`
	Sources         map[string]string `json:"sources,omitempty"`
}

// Salary is a pay range; Min equals Max for a fixed amount.
type Salary struct {
	Min      float64 `json:"min,omitempty"`
	Max      float64 `json:"max,omitempty"`
	Currency string  `json:"currency,omitempty"`
	Unit     string  `json:"unit,omitempty"` // hour, day, week, month, year
}

const maxJobDescription = 5000

// detectJobPosting finds the job a page advertises; listing pages with
// several jobs return nil.
func detectJobPosting(pg *pageData) *JobPosting {
	out := &JobPosting{Sources: map[string]string{}}
	found := false
	for _, src := range pg.schemaSources() {
		if m := mainItem(findItems(src.items, ofTypes([]string{"JobPosting"}), nil), pg.base); m != nil {
			found = true
			mergeInto(out, jobFromSchema(m, pg.base), src.name, out.Sources)
		}
	}
	if !found || out.Title == "" {
		return nil
	}
	return out
}

func jobFromSchema(m map[string]interface{}, base *url.URL) *JobPosting {
	j := &JobPosting{
		Title:        cleanInline(schemaString(m["title"])),
		Description:  truncateRunes(cleanInline(schemaString(m["description"])), maxJobDescription),
		Organization: cleanInline(schemaName(m["hiringOrganization"])),
		DatePosted:   schemaDate(schemaString(m["datePosted"])),
		ValidThrough: schemaDate(schemaString(m["validThrough"])),
		Experience:   cleanInline(schemaName(m["experienceRequirements"])),
		Education:    cleanInline(schemaName(m["educationRequirements"])),
		URL:          resolveAny(base, schemaString(m["url"])),
		Salary:       schemaSalary(m["baseSalary"], schemaString(m["salaryCurrency"])),
	}
	if j.Title == "" {
		j.Title = cleanInline(schemaString(m["name"]))
	}
	if org, ok := m["hiringOrganization"].(map[string]interface{}); ok {
		j.OrganizationURL = resolveAny(base, schemaString(org["url"]))
	}
	if id, ok := m["identifier"].(map[string]interface{}); ok {
		j.Identifier = schemaString(id["value"])
	} else {
		j.Identifier = schemaString(m["identifier"])
	}
	j.Remote = strings.Contains(strings.ToUpper(schemaString(m["jobLocationType"])), "TELECOMMUTE")
	var places []string
	for _, loc := range schemaList(m["jobLocation"]) {
		lm, ok := loc.(map[string]interface{})
		if !ok {
			places = appendUnique(places, cleanInline(schemaString(loc)))
			continue
		}
		if a := schemaAddress(lm["address"]); a != nil {
			places = appendUnique(places, joinNonEmpty(", ", a.Locality, a.Region, a.Country))
		} else {
			places = appendUnique(places, cleanInline(schemaString(lm["name"])))
		}
	}
	j.Location = strings.Join(places, "; ")
	for _, t := range schemaStrings(m["employmentType"], true) {
		j.EmploymentTypes = appendUnique(j.EmploymentTypes, strings.ToLower(strings.ReplaceAll(strings.TrimSpace(t), "-", "_")))
	}
	return j
}

// schemaSalary reads a MonetaryAmount ({currency, value: QuantitativeValue})
// or a plain number.
func schemaSalary(v interface{}, currency string) *Salary {
	s := &Salary{Currency: strings.ToUpper(currency)}
	m, ok := v.(map[string]interface{})
	if !ok {
		s.Min = schemaNumber(v)
		s.Max = s.Min
	} else {
		if c := schemaString(m["currency"]); c != "" {
			s.Currency = strings.ToUpper(c)
		}
		val := m["value"]
		if qv, ok := val.(map[string]interface{}); ok {
			s.Min, s.Max = schemaNumber(qv["minValue"]), schemaNumber(qv["maxValue"])
			if exact := schemaNumber(qv["value"]); exact > 0 && s.Min == 0 && s.Max == 0 {
				s.Min, s.Max = exact, exact
			}
			s.Unit = strings.ToLower(schemaString(qv["unitText"]))
		} else {
			s.Min = schemaNumber(val)
			s.Max = s.Min
		}
		if s.Unit == "" {
			s.Unit = strings.ToLower(schemaString(m["unitText"]))
		}
	}
	if s.Min == 0 && s.Max == 0 {
		return nil
	}
	if s.Max < s.Min {
		s.Max = s.Min
	}
	return s
}
