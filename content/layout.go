package content

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/resolver/crawler/modules"
	"golang.org/x/net/html"
)

// layout is what one walk over a page's HTML found: repeated item cards,
// buy buttons, prices, article text and the marks shop and blog platforms
// put on their templates. The classifier scores it; the HTML product reader
// uses its name and prices.
type layout struct {
	bodyID      string
	bodyClasses []string

	productCards int // repeated cards with a link, an image or heading, and a price
	postCards    int // repeated cards with a link and a heading, no price

	cardLinks     map[string]bool // host+path of links inside cards
	contactSeller bool            // a classified ad's "show contact info" button

	buyButtons    int // add-to-cart controls outside cards
	cardButtons   int // add-to-cart controls inside cards
	notifyStock   bool
	quantityInput bool
	prices        []priceText // prices outside cards, in page order

	paragraphWords int // words in <p> outside cards and page chrome
	articleWords   int // the same, inside <article>
	hasTime        bool
	hasAuthor      bool
	pagination     bool
	noResults      bool // "no results found": an empty listing

	h1 string // first main heading

	wordpress  bool              // wp-content assets or WordPress body classes
	prestashop bool              // the prestashop JS object
	scriptPage map[string]string // platform → page kind named in a script (prestashop page_name, Shopify pageType, Next.js route)

	features []string // class tokens and template marks, for template similarity
}

// priceText is a price shown outside item cards.
type priceText struct {
	text string
	old  bool // struck through or labelled as the old/regular price
}

const (
	minCardGroup  = 3  // repeated siblings needed to call them cards
	maxPriceChars = 60 // longer text is a sentence mentioning a price, not a price
	maxFeatures   = 1500
	maxButtonText = 40
)

// chromeTags hold navigation and site-wide parts, not the page's own content.
var chromeTags = map[string]bool{"nav": true, "header": true, "footer": true, "aside": true}

var invisibleTags = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true}

// Classes that mark a sibling's state, not its kind ("active", "slick-slide").
var stateClasses = map[string]bool{"active": true, "selected": true, "current": true, "first": true, "last": true,
	"odd": true, "even": true, "hidden": true, "show": true, "open": true, "swiper-slide-active": true,
	"swiper-slide-next": true, "swiper-slide-prev": true, "slick-active": true, "slick-current": true,
	"slick-cloned": true, "swiper-slide-duplicate": true, "is-selected": true, "is-active": true}

var buyAttrTokens = []string{"add-to-cart", "add_to_cart", "addtocart", "add-to-basket", "add_to_basket", "addtobasket",
	"button-cart", "btn-cart", "buy-now", "buy_now", "buynow", "tocart", "single_add_to_cart_button", "product-buy",
	"buy-button", "buy_button", "btn-buy", "btn_buy", "add-cart", "addcart", "add-basket"}

var buyTextContains = []string{"افزودن به سبد", "اضافه به سبد", "اضافه کردن به سبد", "add to cart", "add to bag",
	"add to basket", "buy now", "buy it now", "افزودن به کارت"}

var buyTextEquals = map[string]bool{"خرید": true, "خرید کنید": true, "همین حالا بخرید": true, "خرید آنلاین": true,
	"buy": true, "خرید محصول": true, "ثبت سفارش": true, "سفارش دهید": true}

// isNoResults reports text saying a listing or search found nothing:
// "نتیجهٔ دقیقی پیدا نشد", "محصولی یافت نشد", "No products were found".
// "Not found" alone is not enough: product pages say "no reviews found".
func isNoResults(t string) bool {
	low := strings.ToLower(strings.ReplaceAll(modules.NormalizePersian(t), "\u200c", " "))
	for _, w := range []string{"no results", "no products were found", "no products found", "nothing found", "no items found", "0 results"} {
		if strings.Contains(low, w) {
			return true
		}
	}
	if !strings.Contains(low, "یافت نشد") && !strings.Contains(low, "پیدا نشد") && !strings.Contains(low, "وجود ندارد") {
		return false
	}
	for _, noun := range []string{"نتیجه", "محصول", "کالا", "آگهی", "مورد"} {
		if strings.Contains(low, noun) {
			return true
		}
	}
	return false
}

var notifyTexts = []string{"موجود شد خبر", "موجود شد اطلاع", "موجود شد به من", "اطلاع از موجود", "خبرم کن",
	"notify me", "sold out", "out of stock"}

var (
	nextDataPageRe  = regexp.MustCompile(`"page"\s*:\s*"([^"]*)"`)
	prestaPageRe    = regexp.MustCompile(`"page_name"\s*:\s*"([a-z_-]+)"`)
	shopifyPageRe   = regexp.MustCompile(`"pageType"\s*:\s*"([a-zA-Z_-]+)"`)
	oldPriceClassRe = regexp.MustCompile(`old|regular|before|compare|strike|line-through|del\b|was`)
	authorClassRe   = regexp.MustCompile(`(^|[-_])(author|byline|writer)([-_]|$)`)
	paginationRe    = regexp.MustCompile(`pagination|pager|page-numbers|paging`)
)

// analyzeLayout walks the body once (twice for cards) and fills a layout.
func analyzeLayout(root *html.Node) *layout {
	l := &layout{scriptPage: map[string]string{}, cardLinks: map[string]bool{}}
	body := findElement(root, "body")
	if body == nil {
		body = root
	}
	l.bodyID = strings.ToLower(attrVal(body, "id"))
	l.bodyClasses = classTokens(body)
	for _, c := range l.bodyClasses {
		if strings.HasPrefix(c, "wp-") || strings.HasPrefix(c, "postid-") || strings.HasPrefix(c, "page-id-") || c == "woocommerce" || c == "woocommerce-page" {
			l.wordpress = true
		}
	}
	l.scripts(root)

	cards := map[*html.Node]string{} // card root → "product" | "post" | "tile"
	l.findCards(body, cards)

	seen := map[string]bool{}
	addFeature := func(f string) {
		if !seen[f] && len(l.features) < maxFeatures {
			seen[f] = true
			l.features = append(l.features, f)
		}
	}
	for _, c := range l.bodyClasses {
		if !hasDigitRune(c) {
			addFeature("b:" + c)
		}
	}
	if l.bodyID != "" {
		addFeature("bid:" + l.bodyID)
	}
	if r := l.scriptPage["next"]; r != "" {
		addFeature("r:" + r)
	}

	var walk func(n *html.Node, inCard string, inChrome, inArticle, inP, inOld bool)
	walk = func(n *html.Node, inCard string, inChrome, inArticle, inP, inOld bool) {
		switch n.Type {
		case html.TextNode:
			if inCard != "" || inChrome {
				return
			}
			t := strings.TrimSpace(n.Data)
			if t == "" {
				return
			}
			if inP {
				words := len(strings.Fields(t))
				l.paragraphWords += words
				if inArticle {
					l.articleWords += words
				}
			}
			if hasDigit(t) && (hasCurrency(t) || priceClassAbove(n, 3)) {
				l.addPrice(n.Parent, inOld)
			}
			if !l.noResults && utf8.RuneCountInString(t) < 80 {
				l.noResults = isNoResults(t)
			}
			if !l.notifyStock && inControl(n, 4) {
				low := strings.ToLower(modules.NormalizePersian(t))
				for _, w := range notifyTexts {
					if strings.Contains(low, w) && utf8.RuneCountInString(t) < 80 {
						l.notifyStock = true
					}
				}
			}
			return
		case html.ElementNode:
		default:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c, inCard, inChrome, inArticle, inP, inOld)
			}
			return
		}
		tag := n.Data
		if invisibleTags[tag] {
			return
		}
		if kind, ok := cards[n]; ok && inCard == "" {
			inCard = kind
		}
		if chromeTags[tag] || hasRole(n, "navigation", "banner", "contentinfo") {
			inChrome = true
		}
		classes := classTokens(n)
		for _, c := range classes {
			if !hasDigitRune(c) && len(c) >= 3 && len(c) <= 40 {
				addFeature("c:" + c)
			}
		}
		switch tag {
		case "article":
			inArticle = true
		case "p":
			inP = true
		case "del", "s", "strike":
			inOld = true
		case "h1":
			if l.h1 == "" && !inChrome && inCard == "" {
				l.h1 = strings.Join(strings.Fields(visibleText(n)), " ")
			}
		case "time":
			if !inChrome && inCard == "" && attrVal(n, "datetime") != "" {
				l.hasTime = true
			}
		case "a", "link":
			if inCard != "" && tag == "a" && len(l.cardLinks) < 500 {
				if k := linkKey(attrVal(n, "href")); k != "" {
					l.cardLinks[k] = true
				}
			}
			if rel := strings.ToLower(attrVal(n, "rel")); rel == "next" || rel == "prev" {
				l.pagination = true
			} else if rel == "author" && inCard == "" {
				l.hasAuthor = true
			}
		}
		if prop := attrVal(n, "itemprop"); inCard == "" && !inChrome {
			switch prop {
			case "datePublished":
				l.hasTime = true
			case "author":
				l.hasAuthor = true
			}
		}
		for _, c := range classes {
			if !l.pagination && paginationRe.MatchString(c) {
				l.pagination = true
			}
			if inCard == "" && !inChrome && authorClassRe.MatchString(c) {
				l.hasAuthor = true
			}
			if !inOld && strings.Contains(c, "price") && oldPriceClassRe.MatchString(c) {
				inOld = true
			}
		}
		if !inChrome {
			if inCard == "" && !l.contactSeller && isContactSeller(n) {
				l.contactSeller = true
			}
			if isBuyControl(n, classes) {
				if inCard != "" {
					l.cardButtons++
				} else {
					l.buyButtons++
				}
			}
			if inCard == "" && tag == "input" && isQuantityInput(n) {
				l.quantityInput = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inCard, inChrome, inArticle, inP, inOld)
		}
	}
	walk(body, "", false, false, false, false)

	for _, kind := range cards {
		switch kind {
		case "product":
			l.productCards++
		case "post":
			l.postCards++
		}
	}
	sort.Strings(l.features)
	return l
}

// addPrice records the short text holding a price, once per element.
func (l *layout) addPrice(el *html.Node, old bool) {
	if el == nil {
		return
	}
	// "۱,۵۰۰,۰۰۰" and "تومان" often sit in sibling elements.
	text := ""
	for e := el; e != nil && e.Type == html.ElementNode; e = e.Parent {
		t := strings.Join(strings.Fields(visibleText(e)), " ")
		if utf8.RuneCountInString(t) > maxPriceChars {
			break
		}
		text = t
		if hasCurrency(t) {
			break
		}
	}
	if text == "" || len(l.prices) >= 50 {
		return
	}
	for _, p := range l.prices {
		if p.text == text {
			return
		}
	}
	l.prices = append(l.prices, priceText{text: text, old: old})
}

// scripts reads the page kind platforms write into inline scripts.
func (l *layout) scripts(root *html.Node) {
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script":
				if n.FirstChild == nil {
					if strings.Contains(attrVal(n, "src"), "/wp-content/") || strings.Contains(attrVal(n, "src"), "/wp-includes/") {
						l.wordpress = true
					}
					return
				}
				text := n.FirstChild.Data
				if attrVal(n, "id") == "__NEXT_DATA__" {
					var nd struct {
						Page string `json:"page"`
					}
					if json.Unmarshal([]byte(text), &nd) == nil && nd.Page != "" {
						l.scriptPage["next"] = nd.Page
					} else if m := nextDataPageRe.FindStringSubmatch(text); m != nil {
						l.scriptPage["next"] = m[1]
					}
					return
				}
				if strings.Contains(text, "prestashop") {
					if m := prestaPageRe.FindStringSubmatch(text); m != nil {
						l.prestashop = true
						l.scriptPage["prestashop"] = m[1]
					} else if strings.Contains(text, "var prestashop") {
						l.prestashop = true
					}
				}
				if strings.Contains(text, "Shopify") || strings.Contains(text, "ShopifyAnalytics") {
					if m := shopifyPageRe.FindStringSubmatch(text); m != nil {
						l.scriptPage["shopify"] = strings.ToLower(m[1])
					}
				}
				return
			case "link":
				if strings.Contains(attrVal(n, "href"), "/wp-content/") {
					l.wordpress = true
				}
			case "meta":
				if strings.EqualFold(attrVal(n, "name"), "generator") {
					g := strings.ToLower(attrVal(n, "content"))
					if strings.Contains(g, "wordpress") || strings.Contains(g, "woocommerce") {
						l.wordpress = true
					}
					if strings.Contains(g, "prestashop") {
						l.prestashop = true
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
}

// cardStats describes one sibling as a possible item card.
type cardStats struct {
	link, img, price, heading bool
	big                       bool // too many links or too much text: a page section, not a card
}

// A card holds a few links (image, title, buttons) and little text.
const (
	maxCardLinks = 6
	maxCardChars = 600
)

func (st cardStats) isCard() bool {
	return st.link && !st.big && (st.img || st.price || st.heading)
}

// findCards marks groups of repeated siblings that look like item cards: at
// least minCardGroup children with the same tag and classes, most of them
// holding a link plus an image, price or heading. Cards are not searched for
// nested cards (a card's colour swatches are not more cards).
func (l *layout) findCards(n *html.Node, cards map[*html.Node]string) {
	if n.Type != html.ElementNode && n.Type != html.DocumentNode {
		return
	}
	if n.Type == html.ElementNode && (invisibleTags[n.Data] || chromeTags[n.Data] || hasRole(n, "navigation", "banner", "contentinfo")) {
		return
	}
	groups := map[string][]*html.Node{}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && !invisibleTags[c.Data] {
			sig := siblingSignature(c)
			groups[sig] = append(groups[sig], c)
		}
	}
	for _, g := range groups {
		if len(g) < minCardGroup {
			continue
		}
		var stats []cardStats
		cardsIn := 0
		prices, headings := 0, 0
		for _, c := range g {
			st := scanCard(c)
			stats = append(stats, st)
			if st.isCard() {
				cardsIn++
				if st.price {
					prices++
				}
				if st.heading {
					headings++
				}
			}
		}
		if cardsIn < minCardGroup || cardsIn*10 < len(g)*6 {
			continue
		}
		kind := "tile"
		switch {
		case prices*2 >= cardsIn:
			kind = "product"
		case headings*2 >= cardsIn:
			kind = "post"
		}
		for i, c := range g {
			if stats[i].isCard() {
				cards[c] = kind
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if _, isCard := cards[c]; !isCard {
			l.findCards(c, cards)
		}
	}
}

// siblingSignature is a child's tag and classes, minus state classes and
// classes with digits ("product-123", "col-md-3").
func siblingSignature(n *html.Node) string {
	var keep []string
	for _, c := range classTokens(n) {
		if !stateClasses[c] && !hasDigitRune(c) {
			keep = append(keep, c)
		}
	}
	sort.Strings(keep)
	return n.Data + "." + strings.Join(keep, ".")
}

func scanCard(n *html.Node) cardStats {
	var st cardStats
	currency, digits, priceClass := false, false, false
	links := map[string]bool{}
	chars := 0
	var walk func(n *html.Node, depth int)
	walk = func(n *html.Node, depth int) {
		if depth > 25 || st.big {
			return
		}
		switch n.Type {
		case html.TextNode:
			t := n.Data
			if chars += utf8.RuneCountInString(strings.TrimSpace(t)); chars > maxCardChars {
				st.big = true
			}
			if !currency && hasCurrency(t) {
				currency = true
			}
			if !digits && hasDigit(t) {
				digits = true
				if priceClassAbove(n, 3) {
					priceClass = true
				}
			}
			return
		case html.ElementNode:
			if invisibleTags[n.Data] {
				return
			}
			switch n.Data {
			case "a":
				if h := attrVal(n, "href"); h != "" && !strings.HasPrefix(h, "#") && !strings.HasPrefix(strings.ToLower(h), "javascript:") {
					st.link = true
					if links[h] = true; len(links) > maxCardLinks {
						st.big = true
					}
				}
			case "img", "picture":
				st.img = true
			case "h2", "h3", "h4", "h5", "h6":
				st.heading = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth+1)
		}
	}
	walk(n, 0)
	st.price = digits && (currency || priceClass)
	return st
}

func isBuyControl(n *html.Node, classes []string) bool {
	switch n.Data {
	case "button", "a", "input":
	case "form":
		action := strings.ToLower(attrVal(n, "action"))
		return strings.Contains(action, "/cart/add") || strings.Contains(action, "add-to-cart") ||
			attrVal(n, "id") == "product_addtocart_form"
	default:
		return false
	}
	if n.Data == "input" {
		switch strings.ToLower(attrVal(n, "type")) {
		case "submit", "button", "image":
		default:
			return false
		}
	}
	attrs := strings.ToLower(strings.Join([]string{attrVal(n, "id"), strings.Join(classes, " "), attrVal(n, "name"),
		attrVal(n, "data-button-action"), attrVal(n, "data-action")}, " "))
	for _, t := range buyAttrTokens {
		if strings.Contains(attrs, t) {
			return true
		}
	}
	text := attrVal(n, "value")
	if n.Data != "input" {
		text = visibleText(n)
	}
	text = strings.ToLower(strings.Join(strings.Fields(modules.NormalizePersian(text)), " "))
	if text == "" || utf8.RuneCountInString(text) > maxButtonText {
		return false
	}
	if buyTextEquals[text] {
		return true
	}
	for _, t := range buyTextContains {
		if strings.Contains(text, t) {
			return true
		}
	}
	return false
}

// contactSellerTexts label the button that reveals a classified ad's phone number.
var contactSellerTexts = setOf("اطلاعات تماس", "تماس با فروشنده", "نمایش شماره", "نمایش شماره تماس", "شماره تماس",
	"تماس با آگهی دهنده", "تماس با مالک", "show phone", "show number", "contact seller", "call seller", "contact advertiser")

func isContactSeller(n *html.Node) bool {
	if n.Data != "button" && n.Data != "a" && attrVal(n, "role") != "button" {
		return false
	}
	text := visibleText(n)
	if utf8.RuneCountInString(text) > maxButtonText {
		return false
	}
	text = strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(modules.NormalizePersian(text), "\u200c", " ")), " "))
	return contactSellerTexts[text]
}

// linkKey reduces an href to host+path for comparing with schema item URLs;
// relative links keep only their path.
func linkKey(href string) string {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil || u.Path == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.") + strings.ToLower(strings.TrimSuffix(u.Path, "/"))
}

func isQuantityInput(n *html.Node) bool {
	switch strings.ToLower(attrVal(n, "type")) {
	case "hidden", "checkbox", "radio", "submit":
		return false
	}
	s := strings.ToLower(attrVal(n, "name") + " " + attrVal(n, "id") + " " + attrVal(n, "class"))
	return strings.Contains(s, "qty") || strings.Contains(s, "quantity")
}

// inControl reports whether one of the n nearest ancestors is a button or link.
func inControl(n *html.Node, levels int) bool {
	for e := n.Parent; e != nil && levels > 0; e, levels = e.Parent, levels-1 {
		if e.Type == html.ElementNode && (e.Data == "button" || e.Data == "a" || attrVal(e, "role") == "button") {
			return true
		}
	}
	return false
}

// priceClassAbove reports whether one of the n nearest ancestors of a node has
// a class containing "price".
func priceClassAbove(n *html.Node, levels int) bool {
	for e := n.Parent; e != nil && levels > 0; e, levels = e.Parent, levels-1 {
		if e.Type == html.ElementNode && strings.Contains(strings.ToLower(attrVal(e, "class")), "price") {
			return true
		}
	}
	return false
}

func hasRole(n *html.Node, roles ...string) bool {
	r := attrVal(n, "role")
	for _, want := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func hasCurrency(s string) bool {
	if hasIranianCurrencyWord(s) {
		return true
	}
	return strings.ContainsAny(s, "$€£₺") || strings.Contains(strings.ToLower(s), "usd") || strings.Contains(s, "درهم")
}

func hasDigitRune(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func classTokens(n *html.Node) []string {
	return strings.Fields(strings.ToLower(attrVal(n, "class")))
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func findElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findElement(c, tag); f != nil {
			return f
		}
	}
	return nil
}
