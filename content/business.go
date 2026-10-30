package content

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/resolver/crawler/modules"
)

// Business is the organization or local business a page describes: a shop,
// company, clinic, restaurant, school, office. Contact details come only from
// what the business publishes about itself as structured data, never from
// phone numbers or emails scraped out of page text, so personal contact
// details of private people are not collected.
type Business struct {
	Name         string            `json:"name,omitempty"`
	LegalName    string            `json:"legal_name,omitempty"`
	Type         string            `json:"type,omitempty"` // schema.org type: Organization, Restaurant, Dentist, …
	Description  string            `json:"description,omitempty"`
	URL          string            `json:"url,omitempty"`
	Logo         string            `json:"logo,omitempty"`
	Image        string            `json:"image,omitempty"`
	Telephones   []string          `json:"telephones,omitempty"`
	Emails       []string          `json:"emails,omitempty"`
	Address      *PostalAddress    `json:"address,omitempty"`
	Geo          *GeoPoint         `json:"geo,omitempty"`
	OpeningHours []string          `json:"opening_hours,omitempty"` // e.g. "Saturday-Wednesday 09:00-18:00"
	PriceRange   string            `json:"price_range,omitempty"`
	Rating       float64           `json:"rating,omitempty"`
	ReviewCount  int               `json:"review_count,omitempty"`
	SocialLinks  []string          `json:"social_links,omitempty"` // schema.org sameAs: Instagram, Telegram, LinkedIn, …
	Sources      map[string]string `json:"sources,omitempty"`
}

// GeoPoint is a latitude/longitude pair.
type GeoPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// businessTypes are Organization, LocalBusiness and their common subtypes.
// Other typed items count as a business when they carry an address and a
// telephone or opening hours (see isBusiness).
var businessTypes = []string{
	"Organization", "Corporation", "LocalBusiness", "OnlineStore", "OnlineBusiness", "Store", "NewsMediaOrganization",
	"EducationalOrganization", "School", "CollegeOrUniversity", "GovernmentOrganization", "NGO", "MedicalOrganization",
	"Hospital", "Pharmacy", "Physician", "Dentist", "MedicalClinic", "MedicalBusiness", "Restaurant", "CafeOrCoffeeShop",
	"FoodEstablishment", "Bakery", "FastFoodRestaurant", "Hotel", "LodgingBusiness", "AutomotiveBusiness", "AutoRepair",
	"AutoDealer", "ProfessionalService", "LegalService", "Attorney", "AccountingService", "FinancialService", "BankOrCreditUnion",
	"InsuranceAgency", "RealEstateAgent", "TravelAgency", "HealthAndBeautyBusiness", "BeautySalon", "HairSalon",
	"SportsActivityLocation", "ExerciseGym", "HomeAndConstructionBusiness", "Electrician", "Plumber", "GeneralContractor",
	"EntertainmentBusiness", "ChildCare", "EmploymentAgency", "ShoppingCenter", "ElectronicsStore", "ClothingStore",
	"FurnitureStore", "GroceryStore", "BookStore", "ComputerStore", "MobilePhoneStore", "HardwareStore", "JewelryStore",
}

// Keys whose objects are about something else (a product's brand, an
// article's author, a job's employer, an event's organizer).
var businessSkipKeys = map[string]bool{
	"brand": true, "manufacturer": true, "seller": true, "offers": true, "review": true, "author": true,
	"creator": true, "hiringOrganization": true, "organizer": true, "performer": true, "sponsor": true, "funder": true,
	"itemReviewed": true, "provider": true,
}

func isBusiness(m map[string]interface{}) bool {
	if hasAnyType(m["@type"], []string{"Person", "Product", "Offer", "Event", "JobPosting", "PostalAddress", "WebSite", "WebPage"}) {
		return false
	}
	if hasAnyType(m["@type"], businessTypes) {
		return true
	}
	_, hasType := m["@type"]
	return hasType && m["address"] != nil && (m["telephone"] != nil || m["openingHours"] != nil || m["openingHoursSpecification"] != nil)
}

// detectBusiness finds the business a page describes. When a page has
// several (a site's Organization plus a branch), the most detailed one wins.
func detectBusiness(pg *pageData) *Business {
	out := &Business{Sources: map[string]string{}}
	found := false
	for _, src := range pg.schemaSources() {
		var best *Business
		bestScore := 0
		for _, m := range findItems(src.items, isBusiness, businessSkipKeys) {
			b := businessFromSchema(m, pg.base)
			if score := b.score(); score > bestScore {
				best, bestScore = b, score
			}
		}
		if best != nil {
			found = true
			mergeInto(out, best, src.name, out.Sources)
		}
	}
	if b := businessFromMeta(pg); b != nil {
		found = true
		mergeInto(out, b, SourceMeta, out.Sources)
	}
	if !found || (out.Name == "" && len(out.Telephones) == 0 && out.Address == nil) {
		return nil
	}
	return out
}

// score counts the filled fields, weighting contact details.
func (b *Business) score() int {
	s := 0
	for _, filled := range []bool{b.Name != "", b.URL != "", b.Logo != "", b.Description != "", b.Type != "" && b.Type != "Organization"} {
		if filled {
			s++
		}
	}
	for _, filled := range []bool{len(b.Telephones) > 0, len(b.Emails) > 0, b.Address != nil, len(b.OpeningHours) > 0, b.Geo != nil} {
		if filled {
			s += 2
		}
	}
	return s
}

func businessFromSchema(m map[string]interface{}, base *url.URL) *Business {
	b := &Business{
		Name:        cleanInline(schemaString(m["name"])),
		LegalName:   cleanInline(schemaString(m["legalName"])),
		Description: truncateRunes(cleanInline(schemaString(m["description"])), maxDescription),
		URL:         resolveAny(base, schemaString(m["url"])),
		Address:     schemaAddress(m["address"]),
		PriceRange:  schemaString(m["priceRange"]),
	}
	if t := schemaEnum(schemaString(m["@type"])); t != "" {
		b.Type = t
	}
	if logos := schemaImages(m["logo"], base, 1); len(logos) > 0 {
		b.Logo = logos[0]
	}
	if imgs := schemaImages(m["image"], base, 1); len(imgs) > 0 {
		b.Image = imgs[0]
	}
	phones, emails := schemaList(m["telephone"]), schemaList(m["email"])
	for _, cp := range schemaList(m["contactPoint"]) {
		if cm, ok := cp.(map[string]interface{}); ok {
			phones = append(phones, schemaList(cm["telephone"])...)
			emails = append(emails, schemaList(cm["email"])...)
		}
	}
	for _, p := range phones {
		b.Telephones = appendUnique(b.Telephones, normalizePhone(schemaString(p)))
	}
	for _, e := range emails {
		b.Emails = appendUnique(b.Emails, normalizeEmail(schemaString(e)))
	}
	if g, ok := m["geo"].(map[string]interface{}); ok {
		lat, lng := schemaNumber(g["latitude"]), schemaNumber(g["longitude"])
		if lat != 0 || lng != 0 {
			b.Geo = &GeoPoint{Latitude: lat, Longitude: lng}
		}
	}
	for _, h := range schemaList(m["openingHours"]) {
		b.OpeningHours = appendUnique(b.OpeningHours, cleanInline(schemaString(h)))
	}
	for _, spec := range schemaList(m["openingHoursSpecification"]) {
		b.OpeningHours = appendUnique(b.OpeningHours, openingHoursText(spec))
	}
	for _, link := range schemaList(m["sameAs"]) {
		if u := resolveAny(base, schemaString(link)); looksLikeURL(u) {
			b.SocialLinks = appendUnique(b.SocialLinks, u)
		}
	}
	b.Rating, _, b.ReviewCount = schemaRating(m["aggregateRating"])
	return b
}

// openingHoursText turns an OpeningHoursSpecification into
// "Saturday, Sunday 09:00-18:00".
func openingHoursText(v interface{}) string {
	m, ok := v.(map[string]interface{})
	if !ok {
		return ""
	}
	var days []string
	for _, d := range schemaList(m["dayOfWeek"]) {
		if day := schemaEnum(schemaString(d)); day != "" {
			days = append(days, day)
		}
	}
	opens, closes := hhmm(schemaString(m["opens"])), hhmm(schemaString(m["closes"]))
	hours := ""
	if opens != "" || closes != "" {
		hours = opens + "-" + closes
	}
	return joinNonEmpty(" ", strings.Join(days, ", "), hours)
}

// hhmm shortens "09:00:00" to "09:00".
func hhmm(s string) string {
	s = modules.ToLatinDigits(strings.TrimSpace(s))
	if len(s) == 8 && s[2] == ':' && s[5] == ':' {
		return s[:5]
	}
	return s
}

// normalizePhone keeps a dialable number: Latin digits, a leading +, no
// "tel:" prefix or decoration.
func normalizePhone(s string) string {
	s = modules.ToLatinDigits(strings.TrimSpace(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "tel:"), "TEL:")
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		}
	}
	if b.Len() < 5 {
		return ""
	}
	return b.String()
}

func normalizeEmail(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "mailto:"))
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	if !strings.Contains(s, "@") || strings.ContainsAny(s, " <>") {
		return ""
	}
	return strings.ToLower(s)
}

// businessFromMeta reads Facebook's business:contact_data and place tags.
func businessFromMeta(pg *pageData) *Business {
	get := pg.metaFirst
	street := get("business:contact_data:street_address")
	phone := get("business:contact_data:phone_number")
	if street == "" && phone == "" && get("business:contact_data:locality") == "" {
		return nil
	}
	b := &Business{
		Name: cleanInline(get("og:site_name", "og:title")),
		URL:  resolveAny(pg.base, get("business:contact_data:website", "og:url")),
	}
	if p := normalizePhone(phone); p != "" {
		b.Telephones = []string{p}
	}
	if e := normalizeEmail(get("business:contact_data:email")); e != "" {
		b.Emails = []string{e}
	}
	a := &PostalAddress{
		Street:     street,
		Locality:   get("business:contact_data:locality"),
		Region:     get("business:contact_data:region"),
		PostalCode: modules.ToLatinDigits(get("business:contact_data:postal_code")),
		Country:    get("business:contact_data:country_name"),
	}
	if a.Text = joinNonEmpty(", ", a.Street, a.Locality, a.Region, a.Country); a.Text != "" {
		b.Address = a
	}
	lat, err1 := strconv.ParseFloat(get("place:location:latitude"), 64)
	lng, err2 := strconv.ParseFloat(get("place:location:longitude"), 64)
	if err1 == nil && err2 == nil {
		b.Geo = &GeoPoint{Latitude: lat, Longitude: lng}
	}
	return b
}
