package textual

import (
	"io"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// htmlFile reads an HTML page. Pages declare their encoding inconsistently, so
// the bytes are decoded the way plain text is, by mark and validity.
func htmlFile(r io.ReaderAt, size int64, limit int) (string, error) {
	b, err := readAt(r, min(size, maxEntry))
	o := newBuilder(limit)
	renderHTML(o, decode(b, size > maxEntry), true)
	return o.String(), err
}

// renderHTML writes a page's text into o, starting with its <title> as a heading
// when title is set.
func renderHTML(o *builder, src string, title bool) {
	// The parser repairs any markup the way a browser would, but it refuses a
	// page nested more than 512 elements deep, which unclosed tags in old pages
	// can reach. Such a page is parsed again with its deepest elements unwrapped,
	// and with every element unwrapped if that fails too.
	doc, err := html.Parse(strings.NewReader(src))
	for _, depth := range []int{64, 0} {
		if err == nil {
			break
		}
		doc, err = html.Parse(strings.NewReader(unnest(src, depth)))
	}
	if err != nil {
		return
	}
	if title {
		if t := pageTitle(doc); t != "" {
			o.title(t)
		}
	}
	h := &htmlRenderer{o: o}
	walk(doc, h.enter, h.exit)
}

// dropped are the elements whose content is not text a reader sees: code, styling,
// metadata, embedded media, drop-down options, and the navigation menus a scrape
// repeats on every page, which would otherwise fill the merged files with the
// same links. The title is dropped where it stands and written once, first.
var dropped = map[atom.Atom]bool{
	atom.Head: true, atom.Title: true, atom.Script: true, atom.Style: true,
	atom.Noscript: true, atom.Template: true, atom.Svg: true, atom.Math: true,
	atom.Iframe: true, atom.Object: true, atom.Nav: true, atom.Select: true,
	atom.Canvas: true, atom.Audio: true, atom.Video: true,
}

// spacing is how far a block element stands from its neighbours: 1 puts it on a
// line of its own, 2 leaves a blank line around it, as between paragraphs.
var spacing = map[atom.Atom]int{
	atom.P: 2, atom.Blockquote: 2, atom.Table: 2, atom.Dl: 2, atom.Hr: 2,
	atom.Section: 2, atom.Article: 2, atom.Header: 2, atom.Footer: 2,
	atom.Main: 2, atom.Aside: 2, atom.Figure: 2, atom.Address: 2,
	atom.Details: 2, atom.Fieldset: 2, atom.Form: 2, atom.Hgroup: 2,

	atom.Div: 1, atom.Li: 1, atom.Tr: 1, atom.Dt: 1, atom.Dd: 1,
	atom.Caption: 1, atom.Figcaption: 1, atom.Summary: 1, atom.Legend: 1,
	atom.Center: 1, atom.Thead: 1, atom.Tbody: 1, atom.Tfoot: 1,
}

var headingLevel = map[atom.Atom]int{
	atom.H1: 1, atom.H2: 2, atom.H3: 3, atom.H4: 4, atom.H5: 5, atom.H6: 6,
}

type htmlRenderer struct {
	o     *builder
	lists int // depth of nested lists, for indenting their items
}

// gap returns the spacing for a block: as asked, except inside a list, where a
// blank line would split the list in two.
func (h *htmlRenderer) gap(n int) int {
	if h.lists > 0 {
		return min(n, 1)
	}
	return n
}

// enter handles an element's start and reports whether to visit its children.
func (h *htmlRenderer) enter(n *html.Node) bool {
	o := h.o
	if o.full() {
		return false
	}
	switch n.Type {
	case html.DocumentNode:
		return true
	case html.TextNode:
		o.text(n.Data)
		return false
	case html.ElementNode:
	default:
		return false // comments and doctypes
	}
	// Foreign content is SVG or MathML, which hold drawing and layout, not prose.
	if n.Namespace != "" || dropped[n.DataAtom] {
		return false
	}
	switch a := n.DataAtom; a {
	case atom.Br:
		o.newline()
		return false
	case atom.Pre:
		o.code(preText(n))
		return false
	case atom.Td, atom.Th:
		o.cell()
		o.inline++
	case atom.Ul, atom.Ol, atom.Menu, atom.Dir:
		o.block(h.gap(2))
		h.lists++
	case atom.Li:
		o.block(1)
		o.mark = strings.Repeat("  ", max(h.lists-1, 0)) + "- "
	case atom.Dd:
		o.block(1)
		o.mark = ": "
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		o.block(h.gap(2))
		o.mark = strings.Repeat("#", headingLevel[a]) + " "
		o.inline++ // a heading is one line, whatever breaks it holds
	default:
		o.block(h.gap(spacing[a]))
	}
	return true
}

// exit handles an element's end.
func (h *htmlRenderer) exit(n *html.Node) {
	if n.Type != html.ElementNode {
		return
	}
	o := h.o
	switch a := n.DataAtom; a {
	case atom.Td, atom.Th:
		o.inline--
	case atom.Ul, atom.Ol, atom.Menu, atom.Dir:
		h.lists--
		o.block(h.gap(2))
	case atom.Li, atom.Dd:
		o.mark = ""
		o.block(1)
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		o.inline--
		o.mark = ""
		o.block(h.gap(2))
	default:
		o.block(h.gap(spacing[a]))
	}
}

// preText returns the text of a <pre> block with its whitespace as written,
// minus blank lines at either end.
func preText(pre *html.Node) string {
	var b strings.Builder
	walk(pre, func(n *html.Node) bool {
		switch {
		case n.Type == html.TextNode:
			b.WriteString(n.Data)
		case n.Type != html.ElementNode:
		case n.Namespace != "" || dropped[n.DataAtom]:
			return false
		case n.DataAtom == atom.Br:
			b.WriteByte('\n')
		}
		return true
	}, func(*html.Node) {})
	s := strings.ReplaceAll(b.String(), "\u00A0", " ")
	s = strings.TrimRight(s, " \t\n")
	// Drop leading blank lines, but keep the first line's indentation.
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 || strings.TrimSpace(s[:i]) != "" {
			return s
		}
		s = s[i+1:]
	}
}

// pageTitle returns the text of the page's first HTML <title>; an SVG's titles
// name drawings, not the page.
func pageTitle(doc *html.Node) string {
	var t string
	found := false
	walk(doc, func(n *html.Node) bool {
		if found {
			return false
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Title && n.Namespace == "" {
			found = true
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					t += c.Data
				}
			}
			return false
		}
		return true
	}, func(*html.Node) {})
	return strings.TrimSpace(t)
}

// unnest rewrites a page with every element nested deeper than depth unwrapped:
// its tags go and its content stays. A block's tags become <br>, so its text
// keeps a line of its own. Depth is counted from the tags as written. That
// overstates it where end tags are implied, which errs toward unwrapping.
//
// Raw-text elements keep their tags at any depth. Unwrapped, a <script>'s code
// would read as text.
func unnest(src string, depth int) string {
	z := html.NewTokenizer(strings.NewReader(src))
	var b strings.Builder
	b.Grow(len(src))
	var open []string
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return b.String()
		}
		raw, _ := z.TagName()
		name := string(raw)
		a := atom.Lookup(raw)
		switch tt {
		// HTML ignores the slash in <div/>: the element stays open.
		case html.StartTagToken, html.SelfClosingTagToken:
			if rawText[a] || (!void[a] && len(open) < depth) {
				open = append(open, name)
			} else if !void[a] {
				if isBlock(a) {
					b.WriteString("<br>")
				}
				continue
			}
		case html.EndTagToken:
			i := len(open) - 1
			for i >= 0 && open[i] != name {
				i--
			}
			if i < 0 {
				if isBlock(a) {
					b.WriteString("<br>")
				}
				continue // the end of an unwrapped element, or a stray
			}
			open = open[:i]
		}
		b.Write(z.Raw())
	}
}

// rawText are the elements whose content the tokenizer takes as text up to the
// matching end tag.
var rawText = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Textarea: true, atom.Title: true,
	atom.Xmp: true, atom.Iframe: true, atom.Noembed: true, atom.Noframes: true,
	atom.Noscript: true, atom.Plaintext: true,
}

// void are the elements that never hold content, so never nest.
var void = map[atom.Atom]bool{
	atom.Area: true, atom.Base: true, atom.Br: true, atom.Col: true, atom.Embed: true,
	atom.Hr: true, atom.Img: true, atom.Input: true, atom.Link: true, atom.Meta: true,
	atom.Param: true, atom.Source: true, atom.Track: true, atom.Wbr: true,
}

func isBlock(a atom.Atom) bool {
	switch a {
	case atom.Ul, atom.Ol, atom.Menu, atom.Dir, atom.Pre, atom.Td, atom.Th:
		return true
	}
	return spacing[a] > 0 || headingLevel[a] > 0
}

// walk visits root's subtree in document order. enter reports whether to visit a
// node's children; exit runs once they are done, for every node enter descended
// into. It follows the tree's links instead of recursing, so the renderer's state
// lives in one place.
func walk(root *html.Node, enter func(*html.Node) bool, exit func(*html.Node)) {
	n := root
	for {
		if enter(n) {
			if n.FirstChild != nil {
				n = n.FirstChild
				continue
			}
			exit(n)
		}
		for n != root && n.NextSibling == nil {
			n = n.Parent
			exit(n)
		}
		if n == root {
			return
		}
		n = n.NextSibling
	}
}
