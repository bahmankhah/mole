package content

import (
	"reflect"
	"testing"
)

func TestDetectArticle(t *testing.T) {
	t.Run("json-ld news article", func(t *testing.T) {
		page := `<html lang="fa"><head>
<meta property="og:type" content="article"><meta property="og:site_name" content="خبرگزاری نمونه">
<meta property="article:tag" content="اقتصاد"><meta property="article:tag" content="بورس">
<script type="application/ld+json">{"@context":"https://schema.org","@type":"NewsArticle",
 "headline":"رشد شاخص بورس","datePublished":"2024-05-04T10:30:00+03:30","dateModified":"2024-05-04T12:00:00+03:30",
 "author":[{"@type":"Person","name":"مریم احمدی"},{"@type":"Person","name":"https://site.ir/author/x"}],
 "publisher":{"@type":"Organization","name":"خبرگزاری نمونه","logo":{"@type":"ImageObject","url":"/logo.png"}},
 "articleSection":"اقتصادی","keywords":"بورس، شاخص, سهام","image":["/img/a.jpg"],
 "mainEntityOfPage":{"@type":"WebPage","@id":"https://news.ir/n/1"},"wordCount":"640"}</script>
</head><body><article><h1>رشد شاخص بورس</h1></article></body></html>`
		a := detectAll(t, "https://news.ir/n/1", page).Article
		if a == nil {
			t.Fatal("no article detected")
		}
		if a.Headline != "رشد شاخص بورس" || a.Type != "NewsArticle" || a.Sources["headline"] != SourceJSONLD {
			t.Errorf("headline/type = %q %q from %q", a.Headline, a.Type, a.Sources["headline"])
		}
		if !reflect.DeepEqual(a.Authors, []string{"مریم احمدی"}) || a.Publisher != "خبرگزاری نمونه" {
			t.Errorf("authors/publisher = %v %q", a.Authors, a.Publisher)
		}
		if a.PublishedAt != "2024-05-04T10:30:00+03:30" || a.ModifiedAt != "2024-05-04T12:00:00+03:30" {
			t.Errorf("dates = %q %q", a.PublishedAt, a.ModifiedAt)
		}
		if !reflect.DeepEqual(a.Keywords, []string{"بورس", "شاخص", "سهام"}) || a.Section != "اقتصادی" {
			t.Errorf("keywords/section = %v %q", a.Keywords, a.Section)
		}
		if a.URL != "https://news.ir/n/1" || a.Image != "https://news.ir/img/a.jpg" || a.Language != "fa" || a.WordCount != 640 {
			t.Errorf("url/image/lang/words = %q %q %q %d", a.URL, a.Image, a.Language, a.WordCount)
		}
	})
	t.Run("meta tags only, Jalali date", func(t *testing.T) {
		page := `<html lang="fa"><head><meta property="og:type" content="article">
<meta property="og:title" content="افتتاح خط جدید مترو"><meta name="author" content="علی رضایی">
<meta name="pubdate" content="۱۴۰۳/۰۲/۱۵ ۱۰:۳۰"><meta name="keywords" content="مترو,تهران">
</head><body><h1>افتتاح خط جدید مترو</h1></body></html>`
		a := detectAll(t, "https://news.ir/n/2", page).Article
		if a == nil || a.Headline != "افتتاح خط جدید مترو" || a.Sources["headline"] != SourceOpenGraph {
			t.Fatalf("article = %+v", a)
		}
		if a.PublishedAt == "" || a.PublishedAt[:10] != "2024-05-04" || a.Sources["published_at"] != SourceMeta {
			t.Errorf("published_at = %q from %q", a.PublishedAt, a.Sources["published_at"])
		}
		if !reflect.DeepEqual(a.Authors, []string{"علی رضایی"}) || !reflect.DeepEqual(a.Keywords, []string{"مترو", "تهران"}) {
			t.Errorf("authors/keywords = %v %v", a.Authors, a.Keywords)
		}
	})
	t.Run("not on product or plain pages", func(t *testing.T) {
		if a := detectAll(t, "https://shop.ir/product/a55/", wooPage).Article; a != nil {
			t.Errorf("article on a product page: %+v", a)
		}
		if a := detectAll(t, "https://x.ir/", `<html><body><h1>Home</h1></body></html>`).Article; a != nil {
			t.Errorf("article on a plain page: %+v", a)
		}
	})
}

func TestDetectBusiness(t *testing.T) {
	page := `<html><head><script type="application/ld+json">{"@context":"https://schema.org","@graph":[
 {"@type":"Organization","@id":"https://clinic.ir/#org","name":"کلینیک نمونه","url":"https://clinic.ir/",
  "logo":{"@type":"ImageObject","url":"https://clinic.ir/logo.png"},
  "sameAs":["https://instagram.com/clinic","https://t.me/clinic"]},
 {"@type":"Dentist","name":"کلینیک نمونه - شعبه ونک","telephone":["۰۲۱-۸۸۱۲۳۴۵۶","+98 912 000 0000"],
  "email":"mailto:Info@Clinic.ir","priceRange":"$$",
  "address":{"@type":"PostalAddress","streetAddress":"خیابان ونک، پلاک ۱۰","addressLocality":"تهران","addressRegion":"تهران","postalCode":"۱۹۹۱۷۴۵۶۷۸","addressCountry":{"@type":"Country","name":"IR"}},
  "geo":{"@type":"GeoCoordinates","latitude":"35.7575","longitude":51.41},
  "openingHoursSpecification":[{"@type":"OpeningHoursSpecification","dayOfWeek":["https://schema.org/Saturday","https://schema.org/Sunday"],"opens":"09:00:00","closes":"18:00:00"}],
  "aggregateRating":{"ratingValue":"4.8","reviewCount":"120"}},
 {"@type":"Product","name":"x","brand":{"@type":"Organization","name":"Brand Co","telephone":"021-1111111"}}
]}</script></head><body></body></html>`
	b := detectAll(t, "https://clinic.ir/vanak", page).Business
	if b == nil {
		t.Fatal("no business detected")
	}
	if b.Name != "کلینیک نمونه - شعبه ونک" || b.Type != "Dentist" {
		t.Errorf("the branch with contact details must win: %q %q", b.Name, b.Type)
	}
	if !reflect.DeepEqual(b.Telephones, []string{"02188123456", "+989120000000"}) || !reflect.DeepEqual(b.Emails, []string{"info@clinic.ir"}) {
		t.Errorf("telephones/emails = %v %v", b.Telephones, b.Emails)
	}
	if b.Address == nil || b.Address.Locality != "تهران" || b.Address.PostalCode != "1991745678" || b.Address.Country != "IR" {
		t.Errorf("address = %+v", b.Address)
	}
	if b.Geo == nil || b.Geo.Latitude != 35.7575 || b.Geo.Longitude != 51.41 {
		t.Errorf("geo = %+v", b.Geo)
	}
	if !reflect.DeepEqual(b.OpeningHours, []string{"Saturday, Sunday 09:00-18:00"}) || b.Rating != 4.8 || b.ReviewCount != 120 {
		t.Errorf("hours/rating = %v %v %d", b.OpeningHours, b.Rating, b.ReviewCount)
	}

	t.Run("a product's brand is not the page's business", func(t *testing.T) {
		if b := detectAll(t, "https://shop.ir/product/a55/", wooPage).Business; b != nil && b.Name == "Samsung" {
			t.Errorf("brand detected as business: %+v", b)
		}
	})
	t.Run("facebook business tags", func(t *testing.T) {
		page := `<head><meta property="og:site_name" content="Cafe Sahar">
<meta property="business:contact_data:street_address" content="Valiasr St 12"><meta property="business:contact_data:locality" content="Tehran">
<meta property="business:contact_data:phone_number" content="+98 21 2222 3333">
<meta property="place:location:latitude" content="35.7"><meta property="place:location:longitude" content="51.4"></head>`
		b := detectAll(t, "https://cafe.ir/", page).Business
		if b == nil || b.Name != "Cafe Sahar" || b.Address == nil || b.Address.Text != "Valiasr St 12, Tehran" ||
			!reflect.DeepEqual(b.Telephones, []string{"+982122223333"}) || b.Geo == nil || b.Sources["telephones"] != SourceMeta {
			t.Errorf("business = %+v", b)
		}
	})
}

func TestDetectJobPosting(t *testing.T) {
	page := `<script type="application/ld+json">{"@context":"https://schema.org/","@type":"JobPosting",
 "title":"برنامه‌نویس Go","description":"<p>توسعه <b>سرویس‌ها</b></p>","datePosted":"2024-05-01","validThrough":"2024-06-01T00:00",
 "employmentType":["FULL_TIME","CONTRACTOR"],"jobLocationType":"TELECOMMUTE",
 "hiringOrganization":{"@type":"Organization","name":"شرکت نمونه","sameAs":"https://co.ir"},
 "jobLocation":{"@type":"Place","address":{"@type":"PostalAddress","addressLocality":"تهران","addressCountry":"IR"}},
 "baseSalary":{"@type":"MonetaryAmount","currency":"IRT","value":{"@type":"QuantitativeValue","minValue":40000000,"maxValue":60000000,"unitText":"MONTH"}},
 "identifier":{"@type":"PropertyValue","name":"co","value":"J-77"}}</script>`
	j := detectAll(t, "https://jobs.ir/j/77", page).JobPosting
	if j == nil {
		t.Fatal("no job detected")
	}
	if j.Title != "برنامه‌نویس Go" || j.Organization != "شرکت نمونه" || j.Description != "توسعه سرویس‌ها" {
		t.Errorf("title/org/description = %q %q %q", j.Title, j.Organization, j.Description)
	}
	if j.Location != "تهران, IR" || !j.Remote || !reflect.DeepEqual(j.EmploymentTypes, []string{"full_time", "contractor"}) {
		t.Errorf("location/remote/types = %q %v %v", j.Location, j.Remote, j.EmploymentTypes)
	}
	if j.Salary == nil || j.Salary.Min != 40000000 || j.Salary.Max != 60000000 || j.Salary.Currency != "IRT" || j.Salary.Unit != "month" {
		t.Errorf("salary = %+v", j.Salary)
	}
	if j.DatePosted[:10] != "2024-05-01" || j.ValidThrough == "" || j.Identifier != "J-77" {
		t.Errorf("dates/id = %q %q %q", j.DatePosted, j.ValidThrough, j.Identifier)
	}
}

func TestDetectEvent(t *testing.T) {
	page := `<script type="application/ld+json">{"@context":"https://schema.org","@type":"MusicEvent","name":"کنسرت بهار",
 "startDate":"2024-05-10T20:00:00+03:30","endDate":"2024-05-10T22:00:00+03:30",
 "eventStatus":"https://schema.org/EventRescheduled","eventAttendanceMode":"https://schema.org/MixedEventAttendanceMode",
 "location":[{"@type":"Place","name":"تالار وحدت","address":{"@type":"PostalAddress","addressLocality":"تهران"}},
             {"@type":"VirtualLocation","url":"https://live.example/spring"}],
 "organizer":{"@type":"Organization","name":"بنیاد رودکی"},"performer":[{"@type":"MusicGroup","name":"گروه نمونه"}],
 "offers":[{"@type":"Offer","price":"1500000","priceCurrency":"IRT","availability":"https://schema.org/InStock"},
           {"@type":"Offer","price":"900000","priceCurrency":"IRT"}]}</script>`
	e := detectAll(t, "https://tickets.ir/e/1", page).Event
	if e == nil {
		t.Fatal("no event detected")
	}
	if e.Name != "کنسرت بهار" || e.Type != "MusicEvent" || e.Status != "rescheduled" || e.AttendanceMode != "mixed" {
		t.Errorf("name/type/status/mode = %q %q %q %q", e.Name, e.Type, e.Status, e.AttendanceMode)
	}
	if e.Venue != "تالار وحدت" || e.Address == nil || e.Address.Locality != "تهران" || e.OnlineURL != "https://live.example/spring" {
		t.Errorf("venue/address/online = %q %+v %q", e.Venue, e.Address, e.OnlineURL)
	}
	if e.Organizer != "بنیاد رودکی" || !reflect.DeepEqual(e.Performers, []string{"گروه نمونه"}) || e.Availability != "in_stock" {
		t.Errorf("organizer/performers/availability = %q %v %q", e.Organizer, e.Performers, e.Availability)
	}
	if e.Price == nil || e.Price.Amount != 900000 || e.Price.Currency != "IRT" {
		t.Errorf("price = %+v (the cheapest ticket)", e.Price)
	}

	listing := `<script type="application/ld+json">[{"@type":"Event","name":"A","startDate":"2024-01-01"},{"@type":"Event","name":"B","startDate":"2024-01-02"}]</script>`
	if e := detectAll(t, "https://tickets.ir/", listing).Event; e != nil {
		t.Errorf("event on a listing page: %+v", e)
	}
}

func TestDetectFAQ(t *testing.T) {
	t.Run("yoast graph with references", func(t *testing.T) {
		page := `<script type="application/ld+json">{"@context":"https://schema.org","@graph":[
 {"@type":["WebPage","FAQPage"],"@id":"https://s.ir/faq/","mainEntity":[{"@id":"https://s.ir/faq/#q1"},{"@id":"https://s.ir/faq/#q2"}]},
 {"@type":"Question","@id":"https://s.ir/faq/#q1","name":"ارسال چقدر طول می‌کشد؟","acceptedAnswer":{"@type":"Answer","text":"<p>۲ تا ۳ روز کاری</p>"}},
 {"@type":"Question","@id":"https://s.ir/faq/#q2","name":"Can I return items?","acceptedAnswer":{"@type":"Answer","text":"Within 7 days."}}]}</script>`
		got := detectAll(t, "https://s.ir/faq/", page).FAQ
		want := []FAQItem{{"ارسال چقدر طول می‌کشد؟", "۲ تا ۳ روز کاری"}, {"Can I return items?", "Within 7 days."}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("faq = %+v", got)
		}
	})
	t.Run("microdata", func(t *testing.T) {
		page := `<div itemscope itemtype="https://schema.org/FAQPage">
 <div itemprop="mainEntity" itemscope itemtype="https://schema.org/Question"><h3 itemprop="name">Q1?</h3>
  <div itemprop="acceptedAnswer" itemscope itemtype="https://schema.org/Answer"><div itemprop="text">A1.</div></div></div>
</div>`
		if got := detectAll(t, "https://s.ir/", page).FAQ; !reflect.DeepEqual(got, []FAQItem{{"Q1?", "A1."}}) {
			t.Errorf("faq = %+v", got)
		}
	})
}

func TestDetectOnlyRequestedFormats(t *testing.T) {
	d, err := Detect("https://shop.ir/product/a55/", []byte(wooPage), map[string]bool{"article": true})
	if err != nil {
		t.Fatal(err)
	}
	if d.Product != nil || !d.IsEmpty() {
		t.Errorf("unrequested detectors ran: %+v", d)
	}
}
