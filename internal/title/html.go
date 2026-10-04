package title

import (
	"bytes"
	"html"
	"io"
	"strings"
	"unicode/utf8"
)

func htmlFile(r io.ReaderAt, size int64) string { return htmlTitle(head(r, size)) }

// htmlTitle returns a page's first <title>, or, when that is missing, empty or
// names nothing, the text of the first <h1> that has any.
//
// It scans rather than parses. Scraped pages are rarely well formed, and finding
// tags, skipping comments, scripts, styles and inline SVG, and reading two
// elements is all a title needs. A look ahead for an end tag that never comes
// happens at most once of each kind, so no part of the page is read more than a
// few times and the cost stays in proportion to its size.
func htmlTitle(b []byte) string {
	p := &page{b: b}
	var h1 string
	titleSeen, h1Seen := false, false
	for i := 0; i < len(b); {
		k := bytes.IndexByte(b[i:], '<')
		if k < 0 {
			break
		}
		t := readTag(b, i+k)
		if t.next < 0 {
			break // the rest of the page sits inside one tag or comment
		}
		i = t.next
		if t.end || t.name == "" {
			continue
		}
		switch {
		case t.name == "title" && !titleSeen:
			titleSeen = true
			end := -1
			if !t.self { // <title/> is an XHTML page's empty title
				end = endTag(b, i, "title")
			}
			if end < 0 {
				continue // no title after all; an h1 may still follow
			}
			if s := clean(p.text(b[i:end])); s != "" {
				return s
			}
			if h1Seen {
				return h1
			}
			i = end
		case t.self:
			// An empty element, in XHTML's spelling. Nothing to read or skip.
		case t.name == "h1" && !h1Seen:
			text, end := h1Text(b, i)
			if end < 0 {
				continue
			}
			i = end
			if s := normalize(p.text(text)); s != "" {
				h1Seen, h1 = true, heading(s)
				if titleSeen {
					return h1
				}
			}
		case rawText[t.name] || t.name == "title":
			if i = endTag(b, i, t.name); i < 0 {
				return h1 // the rest of the page is script, or the like
			}
		case foreign[t.name]:
			if i = skipElement(b, i, t.name); i < 0 {
				return h1
			}
		}
	}
	return h1
}

// page is the head of an HTML file, and its charset declaration once found.
type page struct {
	b       []byte
	charset string
	looked  bool
}

// text decodes text from the page: its bytes into UTF-8, then its character
// references. The charset declaration is looked up only when the bytes are not
// UTF-8 already, which on a modern page is never, and then only once: a page of
// many empty headings must not search itself once for each.
func (p *page) text(raw []byte) string {
	if !p.looked && !utf8.Valid(raw) {
		p.charset, p.looked = declaredCharset(p.b), true
	}
	return html.UnescapeString(decode(raw, p.charset))
}

// heading finishes an h1's text. Sphinx and MkDocs end each heading with a
// pilcrow that links to it; that is navigation, not part of the title.
func heading(s string) string {
	return clean(strings.TrimSuffix(s, "¶"))
}

// rawText elements hold text that is not markup: a <title> inside a script is a
// string, not a title. Such an element runs to its own end tag.
var rawText = map[string]bool{
	"script": true, "style": true, "textarea": true, "xmp": true, "iframe": true,
	"noembed": true, "noframes": true, "noscript": true, "template": true, "plaintext": true,
}

// foreign elements switch to another vocabulary. An SVG icon has a <title> of its
// own, a tooltip that names the icon and not the page.
var foreign = map[string]bool{"svg": true, "math": true}

// phrasing elements sit inside a line of text. Removing one joins the text on
// either side of it; removing any other tag, such as <br>, leaves a space.
var phrasing = map[string]bool{
	"a": true, "abbr": true, "b": true, "bdi": true, "bdo": true, "big": true,
	"cite": true, "code": true, "data": true, "del": true, "dfn": true, "em": true,
	"font": true, "i": true, "ins": true, "kbd": true, "mark": true, "nobr": true,
	"q": true, "s": true, "samp": true, "small": true, "span": true, "strike": true,
	"strong": true, "sub": true, "sup": true, "time": true, "tt": true, "u": true,
	"var": true, "wbr": true,
}

// h1Text returns the text of the h1 element whose content starts at b[i], with
// its tags taken out, and the index where the element ends. An h1 ends at </h1>,
// or where a browser would end it: at the tag of another heading, or at </body>.
// end is -1 when the element runs past the end of b.
func h1Text(b []byte, i int) (text []byte, end int) {
	for i < len(b) {
		k := bytes.IndexByte(b[i:], '<')
		if k < 0 {
			return nil, -1
		}
		text = append(text, b[i:i+k]...)
		i += k
		t := readTag(b, i)
		switch {
		case t.next < 0:
			return nil, -1
		case t.text:
			text = append(text, '<')
		case isHeading(t.name), t.end && (t.name == "body" || t.name == "html"):
			return text, i
		case t.end, t.self, t.name == "":
			// An end tag, an empty element, a comment: nothing to skip.
		case rawText[t.name] || t.name == "title":
			if i = endTag(b, t.next, t.name); i < 0 {
				return nil, -1
			}
			continue
		case foreign[t.name]:
			if i = skipElement(b, t.next, t.name); i < 0 {
				return nil, -1
			}
			continue
		}
		if t.name != "" && !phrasing[t.name] {
			text = append(text, ' ')
		}
		i = t.next
	}
	return nil, -1
}

// isHeading reports whether name is h1 through h6.
func isHeading(name string) bool {
	return len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6'
}

// tag is one piece of markup.
type tag struct {
	name string // element name in lower case; "" for a comment, a doctype or text
	end  bool   // an end tag, </name>
	self bool   // closed in place, <name/>
	text bool   // a '<' that opens nothing and is only text
	next int    // index just past the markup; -1 when it runs off the end of b
}

// readTag reads the markup that starts with the '<' at b[i], as a browser's
// tokenizer would: a '>' inside a quoted attribute value does not end the tag,
// and a '<' before anything but a letter is text.
func readTag(b []byte, i int) tag {
	rest := b[i:]
	switch {
	case bytes.HasPrefix(rest, []byte("<!-->")):
		return tag{next: i + 5}
	case bytes.HasPrefix(rest, []byte("<!--->")):
		return tag{next: i + 6}
	case bytes.HasPrefix(rest, []byte("<!--")):
		k := bytes.Index(rest[4:], []byte("-->"))
		if k < 0 {
			return tag{next: -1}
		}
		return tag{next: i + 4 + k + 3}
	case len(rest) > 1 && (rest[1] == '!' || rest[1] == '?'):
		return bogus(b, i+2) // a doctype, a CDATA section, a processing instruction
	}
	var t tag
	j := i + 1
	if j < len(b) && b[j] == '/' {
		t.end = true
		j++
		if j < len(b) && !asciiLetter(b[j]) {
			return bogus(b, j) // "</>" or "</ " is no end tag
		}
	}
	if j >= len(b) || !asciiLetter(b[j]) {
		return tag{text: true, next: i + 1}
	}
	start := j
	for j < len(b) && !htmlSpace(b[j]) && b[j] != '/' && b[j] != '>' {
		j++
	}
	t.name = lower(b[start:j])
	for j < len(b) {
		switch c := b[j]; {
		case c == '>':
			t.next = j + 1
			return t
		case c == '/':
			j++
			if j < len(b) && b[j] == '>' {
				t.self = true
				t.next = j + 1
				return t
			}
		case htmlSpace(c):
			j++
		default:
			if j = attribute(b, j); j < 0 {
				return tag{next: -1}
			}
		}
	}
	return tag{next: -1}
}

// bogus reads markup a browser treats as a comment that ends at the next '>'.
func bogus(b []byte, j int) tag {
	k := bytes.IndexByte(b[j:], '>')
	if k < 0 {
		return tag{next: -1}
	}
	return tag{next: j + k + 1}
}

// attribute skips the attribute that starts at b[j], its name and any value, and
// returns the index after it, or -1 when a quoted value never closes.
func attribute(b []byte, j int) int {
	j++ // the name's first character, which may even be '='
	for j < len(b) && !htmlSpace(b[j]) && b[j] != '/' && b[j] != '>' && b[j] != '=' {
		j++
	}
	k := skipSpace(b, j)
	if k >= len(b) || b[k] != '=' {
		return j
	}
	k = skipSpace(b, k+1)
	if k < len(b) && (b[k] == '"' || b[k] == '\'') {
		q := bytes.IndexByte(b[k+1:], b[k])
		if q < 0 {
			return -1
		}
		return k + 1 + q + 1
	}
	for k < len(b) && !htmlSpace(b[k]) && b[k] != '>' {
		k++
	}
	return k
}

// endTag returns the index of the first </name> at or after b[i], in any case,
// or -1.
func endTag(b []byte, i int, name string) int {
	for {
		k := bytes.Index(b[i:], []byte("</"))
		if k < 0 {
			return -1
		}
		i += k
		j := i + 2 + len(name)
		if j < len(b) && equalFold(b[i+2:j], name) && (htmlSpace(b[j]) || b[j] == '/' || b[j] == '>') {
			return i
		}
		i += 2
	}
}

// skipElement returns the index just past the end of the element named name
// whose content starts at b[i], counting nested elements of the same name, or -1
// when it never ends.
func skipElement(b []byte, i int, name string) int {
	for depth := 1; i < len(b); {
		k := bytes.IndexByte(b[i:], '<')
		if k < 0 {
			return -1
		}
		t := readTag(b, i+k)
		if t.next < 0 {
			return -1
		}
		i = t.next
		if t.name != name || t.self {
			continue
		}
		if t.end {
			depth--
		} else {
			depth++
		}
		if depth == 0 {
			return i
		}
	}
	return -1
}

// declaredCharset returns the charset a page declares in a <meta> tag, either as
// charset="..." or inside an http-equiv content="text/html; charset=...", or "".
func declaredCharset(b []byte) string {
	for i := 0; ; {
		k := indexFold(b[i:], "<meta")
		if k < 0 {
			return ""
		}
		t := readTag(b, i+k)
		if t.next < 0 {
			return ""
		}
		if t.name == "meta" {
			if cs := charsetIn(b[i+k : t.next]); cs != "" {
				return cs
			}
		}
		i = t.next
	}
}

// charsetIn finds charset=value in the text of one tag.
func charsetIn(tag []byte) string {
	for i := 0; ; {
		k := indexFold(tag[i:], "charset")
		if k < 0 {
			return ""
		}
		j := skipSpace(tag, i+k+len("charset"))
		i += k + 1
		if j >= len(tag) || tag[j] != '=' {
			continue
		}
		j = skipSpace(tag, j+1)
		if j < len(tag) && (tag[j] == '"' || tag[j] == '\'') {
			j++
		}
		e := j
		for e < len(tag) && !htmlSpace(tag[e]) && strings.IndexByte(`"';>/`, tag[e]) < 0 {
			e++
		}
		if e > j {
			return string(tag[j:e])
		}
	}
}

// lower returns b as a string with its ASCII letters in lower case. Tag names
// ignore case, and only ASCII ones matter here.
func lower(b []byte) string {
	s := make([]byte, len(b))
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		s[i] = c
	}
	return string(s)
}

// equalFold reports whether b spells s, which is lower case, ignoring the case of
// ASCII letters. Unlike bytes.EqualFold, it does not fold "ſ" to "s".
func equalFold(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range len(s) {
		c := b[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != s[i] {
			return false
		}
	}
	return true
}

// indexFold returns the index of the first instance of s, which is lower case,
// in b, ignoring the case of ASCII letters, or -1.
func indexFold(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		if !asciiLetter(s[0]) {
			k := bytes.IndexByte(b[i:], s[0])
			if k < 0 {
				return -1
			}
			if i += k; i+len(s) > len(b) {
				return -1
			}
		}
		if equalFold(b[i:i+len(s)], s) {
			return i
		}
	}
	return -1
}

func asciiLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// htmlSpace reports whether c is whitespace to HTML.
func htmlSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' }

func skipSpace(b []byte, i int) int {
	for i < len(b) && htmlSpace(b[i]) {
		i++
	}
	return i
}
