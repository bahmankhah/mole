package content

import (
	"net/url"
	"strings"

	"github.com/resolver/crawler/modules"
)

// Event is the event a page announces: a concert, conference, webinar,
// exhibition, course session or sports match.
type Event struct {
	Name           string            `json:"name,omitempty"`
	Description    string            `json:"description,omitempty"`
	Type           string            `json:"type,omitempty"`       // schema.org type: MusicEvent, BusinessEvent, …
	StartDate      string            `json:"start_date,omitempty"` // RFC3339
	EndDate        string            `json:"end_date,omitempty"`
	Status         string            `json:"status,omitempty"`          // scheduled, cancelled, postponed, rescheduled, moved_online
	AttendanceMode string            `json:"attendance_mode,omitempty"` // offline, online, mixed
	Venue          string            `json:"venue,omitempty"`
	Address        *PostalAddress    `json:"address,omitempty"`
	OnlineURL      string            `json:"online_url,omitempty"`
	Organizer      string            `json:"organizer,omitempty"`
	Performers     []string          `json:"performers,omitempty"`
	Image          string            `json:"image,omitempty"`
	URL            string            `json:"url,omitempty"`
	Price          *modules.Price    `json:"price,omitempty"` // cheapest ticket
	Availability   string            `json:"availability,omitempty"`
	Sources        map[string]string `json:"sources,omitempty"`
}

var eventTypes = []string{
	"Event", "BusinessEvent", "ChildrensEvent", "ComedyEvent", "CourseInstance", "DanceEvent", "DeliveryEvent",
	"EducationEvent", "ExhibitionEvent", "Festival", "FoodEvent", "Hackathon", "LiteraryEvent", "MusicEvent",
	"PublicationEvent", "SaleEvent", "ScreeningEvent", "SocialEvent", "SportsEvent", "TheaterEvent", "VisualArtsEvent",
}

// detectEvent finds the event a page announces; listing pages with several
// events return nil.
func detectEvent(pg *pageData) *Event {
	out := &Event{Sources: map[string]string{}}
	found := false
	for _, src := range pg.schemaSources() {
		if m := mainItem(findItems(src.items, ofTypes(eventTypes), nil), pg.base); m != nil {
			found = true
			mergeInto(out, eventFromSchema(m, pg.base), src.name, out.Sources)
		}
	}
	if !found || out.Name == "" {
		return nil
	}
	return out
}

func eventFromSchema(m map[string]interface{}, base *url.URL) *Event {
	e := &Event{
		Name:        cleanInline(schemaString(m["name"])),
		Description: truncateRunes(cleanInline(schemaString(m["description"])), maxDescription),
		Type:        schemaEnum(schemaString(m["@type"])),
		StartDate:   schemaDate(schemaString(m["startDate"])),
		EndDate:     schemaDate(schemaString(m["endDate"])),
		Organizer:   cleanInline(schemaName(m["organizer"])),
		URL:         resolveAny(base, schemaString(m["url"])),
	}
	switch strings.ToLower(schemaEnum(schemaString(m["eventStatus"]))) {
	case "eventscheduled":
		e.Status = "scheduled"
	case "eventcancelled":
		e.Status = "cancelled"
	case "eventpostponed":
		e.Status = "postponed"
	case "eventrescheduled":
		e.Status = "rescheduled"
	case "eventmovedonline":
		e.Status = "moved_online"
	}
	switch strings.ToLower(schemaEnum(schemaString(m["eventAttendanceMode"]))) {
	case "offlineeventattendancemode":
		e.AttendanceMode = "offline"
	case "onlineeventattendancemode":
		e.AttendanceMode = "online"
	case "mixedeventattendancemode":
		e.AttendanceMode = "mixed"
	}
	for _, loc := range schemaList(m["location"]) {
		lm, ok := loc.(map[string]interface{})
		if !ok {
			if s := cleanInline(schemaString(loc)); s != "" && e.Venue == "" {
				e.Venue = s
			}
			continue
		}
		if hasJSONLDType(lm["@type"], "VirtualLocation") {
			if e.OnlineURL == "" {
				e.OnlineURL = resolveAny(base, schemaString(lm["url"]))
			}
			continue
		}
		if e.Venue == "" {
			e.Venue = cleanInline(schemaString(lm["name"]))
		}
		if e.Address == nil {
			e.Address = schemaAddress(lm["address"])
		}
	}
	for _, p := range schemaList(m["performer"]) {
		e.Performers = appendUnique(e.Performers, cleanInline(schemaName(p)))
	}
	if imgs := schemaImages(m["image"], base, 1); len(imgs) > 0 {
		e.Image = imgs[0]
	}
	offers := flattenOffers(m["offers"], 0)
	if low, _ := priceRange(offers); low != nil {
		e.Price = low
	}
	for _, o := range offers {
		if o.availability != "" {
			e.Availability = o.availability
			break
		}
	}
	return e
}
