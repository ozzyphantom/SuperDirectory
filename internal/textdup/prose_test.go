package textdup

import (
	"math/rand"
	"regexp"
	"slices"
	"strings"
)

// subject is the vocabulary of one topic. Documents on one subject share words and
// stock phrases, as real ones do, which makes them the hard case to tell apart.
type subject struct {
	slug                     string
	nouns, verbs, adjectives []string
	openers                  []string
}

var subjects = []subject{
	{
		slug:       "garden",
		nouns:      strings.Fields("soil bed seed plant tomato root leaf compost mulch water hose spade weed frost shade row stem flower fruit bulb pot tray seedling fence path border hedge lawn rake trowel bucket worm slug crop harvest greenhouse cutting bean onion"),
		verbs:      strings.Fields("water plant dig cover feed prune trim check move lift sow thin stake turn protect harvest drain spread loosen rake"),
		adjectives: strings.Fields("young dry wet heavy light rich sandy shallow deep sunny cold warm late early tall small healthy"),
		openers:    []string{"in most gardens", "after the last frost", "as a rule", "early in the season", "on a dry day", "in heavy clay", "once the soil warms", "for a better crop"},
	},
	{
		slug:       "network",
		nouns:      strings.Fields("router switch packet port address subnet gateway firewall cable server client protocol interface link frame route table rule host name lease channel antenna signal bridge device log setting password update backup tunnel certificate key request reply"),
		verbs:      strings.Fields("check restart assign block forward route reset update configure test record disable enable renew replace monitor trace allow drop accept"),
		adjectives: strings.Fields("local remote static dynamic primary secondary wireless wired default slow busy new old private public spare"),
		openers:    []string{"in most networks", "by default", "after an outage", "on a small office network", "as a rule", "before any change", "during the maintenance window", "in our setup"},
	},
	{
		slug:       "kitchen",
		nouns:      strings.Fields("flour butter oven pan pot knife onion garlic salt sauce dough bread stock lid spoon bowl tray rice egg sugar milk cream pepper heat crust oil vinegar recipe batch board lemon herb cheese"),
		verbs:      strings.Fields("stir chop bake simmer season whisk pour roast fold slice taste cover rest melt knead toast mix drain heat serve"),
		adjectives: strings.Fields("hot cold fresh soft crisp thick thin sweet salty golden warm heavy small large shallow ripe"),
		openers:    []string{"in most kitchens", "for a weeknight dinner", "as a rule", "if the dough is sticky", "while the oven heats", "for the best flavor", "the day before", "once the pan is hot"},
	},
	{
		slug:       "car",
		nouns:      strings.Fields("engine oil filter brake tire battery belt coolant plug pump hose wheel light fuse mirror seat door clutch gear axle pedal valve sensor tank cap gauge jack bolt nut mileage warranty dealer manual"),
		verbs:      strings.Fields("check replace drain tighten inspect clean rotate charge test top adjust loosen remove fit flush bleed start park service record"),
		adjectives: strings.Fields("front rear spare worn new old loose tight low high cold hot dirty clean flat"),
		openers:    []string{"in most cars", "every few thousand miles", "as a rule", "before a long trip", "in cold weather", "once the engine cools", "according to the manual", "on older models"},
	},
	{
		slug:       "money",
		nouns:      strings.Fields("budget account invoice tax payment expense receipt balance bill loan rate fee card bank statement deposit refund salary pension saving fund share price cost income debt credit claim form deadline record quarter"),
		verbs:      strings.Fields("pay file record track check claim save transfer review reduce compare cancel open close sign keep submit report owe spend"),
		adjectives: strings.Fields("monthly annual high low fixed variable late early joint separate small large net gross unpaid"),
		openers:    []string{"in most households", "at the end of the month", "as a rule", "before the deadline", "for tax purposes", "once a year", "in our experience", "for a small business"},
	},
	{
		slug:       "travel",
		nouns:      strings.Fields("flight hotel luggage passport train ticket map museum station gate seat bag guide tour booking room beach city border visa airport bus ferry route schedule platform market view coast village trail weather"),
		verbs:      strings.Fields("book pack check carry visit catch miss confirm cancel change board reach explore walk rent buy pay plan follow leave"),
		adjectives: strings.Fields("early late cheap expensive crowded quiet local foreign long short direct small busy old famous"),
		openers:    []string{"in most cities", "in the high season", "as a rule", "before you leave", "on arrival", "for a short trip", "in our experience", "off the main road"},
	},
}

// The words every subject shares, most common first.
var (
	determiners  = strings.Fields("the a this each every your our that any one")
	prepositions = strings.Fields("in on with near under behind after before from into over along around against through")
	adverbs      = []string{"carefully", "again", "slowly", "every week", "at once", "by hand", "twice a year", "first", "later", "as needed", "now and then", "without fail"}
	conjunctions = strings.Fields("and but so while because")
	subordinates = strings.Fields("before after when until while")
	modals       = []string{"you can", "you should", "it helps to", "remember to", "try to", "do not", "always", "never"}
)

// doc is a document as paragraphs of sentences, so a test can edit it the way a
// writer does.
type doc [][]string

// text renders a document as plain text: a blank line between paragraphs.
func (d doc) text() string {
	ps := make([]string, len(d))
	for i, p := range d {
		ps[i] = strings.Join(p, " ")
	}
	return strings.Join(ps, "\n\n")
}

// writer writes natural-looking prose on one subject from a seeded source, so a
// test can rebuild any document exactly.
type writer struct {
	r *rand.Rand
	s *subject
}

func newWriter(seed int64, s int) *writer {
	return &writer{rand.New(rand.NewSource(seed)), &subjects[uint(s)%uint(len(subjects))]}
}

// pick draws from a list, favoring its first entries the way word frequencies
// favor common words.
func (w *writer) pick(list []string) string { return list[w.r.Intn(w.r.Intn(len(list))+1)] }

func (w *writer) noun() string {
	det, n := w.pick(determiners), w.pick(w.s.nouns)
	if w.r.Intn(3) == 0 {
		n = w.pick(w.s.adjectives) + " " + n
	}
	if det == "a" && strings.ContainsRune("aeiou", rune(n[0])) {
		det = "an"
	}
	return det + " " + n
}

func (w *writer) clause() string {
	c := w.noun() + " " + thirdPerson(w.pick(w.s.verbs)) + " " + w.noun()
	if w.r.Intn(2) == 0 {
		c += " " + w.pick(prepositions) + " " + w.noun()
	}
	if w.r.Intn(4) == 0 {
		c += " " + w.pick(adverbs)
	}
	return c
}

func (w *writer) sentence() string {
	var s string
	switch w.r.Intn(7) {
	case 0:
		s = w.pick(w.s.verbs) + " " + w.noun() + " " + w.pick(subordinates) + " you " + w.pick(w.s.verbs) + " " + w.noun()
	case 1:
		s = w.pick(w.s.openers) + ", " + w.clause()
	case 2:
		s = w.clause() + ", " + w.pick(conjunctions) + " " + w.clause()
	case 3:
		s = w.pick(subordinates) + " " + w.clause() + ", " + w.clause()
	case 4:
		s = w.pick(modals) + " " + w.pick(w.s.verbs) + " " + w.noun() + " " + w.pick(prepositions) + " " + w.noun()
	default:
		s = w.clause()
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// paragraph is four to eight sentences.
func (w *writer) paragraph() []string {
	p := make([]string, 4+w.r.Intn(5))
	for i := range p {
		p[i] = w.sentence()
	}
	return p
}

func (w *writer) document(paragraphs int) doc {
	d := make(doc, paragraphs)
	for i := range d {
		d[i] = w.paragraph()
	}
	return d
}

// title names a document the way a guide is named.
func (w *writer) title() string {
	return "A guide to the " + w.pick(w.s.adjectives) + " " + w.pick(w.s.nouns)
}

func thirdPerson(verb string) string {
	for _, end := range []string{"s", "sh", "ch", "x", "z", "o"} {
		if strings.HasSuffix(verb, end) {
			return verb + "es"
		}
	}
	if n := len(verb); verb[n-1] == 'y' && !strings.ContainsRune("aeiou", rune(verb[n-2])) {
		return verb[:n-1] + "ies"
	}
	return verb + "s"
}

// edited revises one paragraph the way a second draft does: one sentence
// rewritten, one added, the rest untouched.
func (w *writer) edited(d doc) doc {
	out := slices.Clone(d)
	k := w.r.Intn(len(out))
	p := slices.Clone(out[k])
	p[w.r.Intn(len(p))] = w.sentence()
	out[k] = slices.Insert(p, w.r.Intn(len(p)+1), w.sentence())
	return out
}

// rewritten replaces one paragraph with a new one on the same subject.
func (w *writer) rewritten(d doc) doc {
	out := slices.Clone(d)
	out[w.r.Intn(len(out))] = w.paragraph()
	return out
}

// reordered shuffles the paragraphs.
func (w *writer) reordered(d doc) doc {
	out := slices.Clone(d)
	w.r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// navItems are what a website puts around every page.
var navItems = []string{
	"Home", "Shop", "Products", "Support", "Contact us", "About us", "Blog", "Careers",
	"Sign in", "Create an account", "Cart (0)", "Wish list", "Search", "Skip to main content",
	"Menu", "Deals of the week", "New arrivals", "Gift cards", "Store locator", "Help center",
	"Order status", "Shipping and delivery", "Returns and exchanges", "Privacy policy",
	"Terms of use", "Cookie settings", "Accessibility", "Sitemap", "Sign up for our newsletter",
	"Follow us", "Facebook", "Instagram", "YouTube", "Pinterest", "LinkedIn", "Press room",
	"Investor relations", "Affiliate program", "Do not sell my personal information",
	"Back to top", "Share this page", "Print", "Email a friend", "Related articles",
	"Popular posts", "Categories", "Archives", "Previous article", "Next article",
	"Free shipping on orders over $50", "Download our app", "Customer reviews",
	"Frequently asked questions", "Live chat", "Call us toll free", "Track your order",
}

var siteNames = []string{
	"Northwind Supply", "Bluebird Guides", "Harbor Lane Press", "Copperfield Home",
	"Maple and Stone", "Tidewater Outfitters", "Granite Peak Media", "Willow Creek Co.",
}

// chrome is a website's navigation as a scraper saves it around a page: a menu
// above the article, then teasers for related articles and a legal line below,
// about the given number of words in all. A site's pages share it word for word,
// up to its length.
func chrome(site int64, words int) (header, footer string) {
	r := rand.New(rand.NewSource(site))
	name := siteNames[r.Intn(len(siteNames))]
	items := slices.Clone(navItems)
	r.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	menu := []string{name}
	n := wordCount(name)
	for _, item := range items {
		if n >= words/2 {
			break
		}
		menu = append(menu, item)
		n += wordCount(item)
	}
	// The teasers' writer is seeded apart from any document's, so they never
	// repeat the page they surround.
	w := newWriter(site+1<<40, int(site))
	legal := "© 2026 " + name + ". All rights reserved."
	below := []string{"Related articles"}
	for n += wordCount(below[0]) + wordCount(legal); n < words; {
		s := w.sentence()
		below = append(below, s)
		n += wordCount(s)
	}
	return strings.Join(menu, " | "), strings.Join(append(below, legal), "\n")
}

// wrapped puts a site's navigation above and below a page's text.
func wrapped(text, header, footer string) string {
	return header + "\n\n" + text + "\n\n" + footer
}

// asHTML renders a document as a saved web page: a head, the title twice, an
// element around each paragraph, a link in every third, and a non-breaking space
// in every fourth. Its plain-text twin is the title, then the text.
func asHTML(title string, d doc, slug string) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<title>" + title + "</title>\n</head>\n<body>\n<article>\n<h1>" + title + "</h1>\n")
	for i, p := range d {
		s := slices.Clone(p)
		if i%3 == 1 && len(s) > 1 {
			s[1] = `<a href="/guides/` + slug + `">` + s[1] + "</a>"
		}
		text := strings.Join(s, " ")
		if i%4 == 2 { // keeps the last word off a line of its own
			if k := strings.LastIndex(text, " "); k > 0 {
				text = text[:k] + "&nbsp;" + text[k+1:]
			}
		}
		b.WriteString("<p>" + text + "</p>\n")
	}
	b.WriteString("</article>\n</body>\n</html>\n")
	return b.String()
}

// tags matches an HTML tag.
var tags = regexp.MustCompile(`<[^>]*>`)

// stripped is HTML with its tags blanked out and its entities left in, as a
// quick extractor leaves it.
func stripped(html string) string { return tags.ReplaceAllString(html, " ") }

func wordCount(text string) int {
	w := words{text: text}
	n := 0
	for w.next() {
		n++
	}
	return n
}

// shingleSet is a text's distinct shingles, for measuring true Jaccard similarity.
func shingleSet(text string) map[uint64]bool {
	set := map[uint64]bool{}
	s := shingles{words: words{text: text}}
	for h, ok := s.next(); ok; h, ok = s.next() {
		set[h] = true
	}
	return set
}

// jaccard is the true Jaccard similarity of two shingle sets, which Similarity
// estimates.
func jaccard(a, b map[uint64]bool) float64 {
	shared := 0
	for h := range a {
		if b[h] {
			shared++
		}
	}
	return float64(shared) / float64(len(a)+len(b)-shared)
}
