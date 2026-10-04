package title

import (
	"bytes"
	"html"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

func markdownFile(r io.ReaderAt, size int64) string { return markdownTitle(head(r, size)) }

// markdownTitle returns the title in a Markdown file's front matter, or its first
// level-one heading when the front matter has none or it names nothing.
func markdownTitle(b []byte) string {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")) // a byte order mark
	body := b
	if fm, rest, toml, ok := frontMatter(b); ok {
		if s := clean(fmTitle(decode(fm, ""), toml)); s != "" {
			return s
		}
		body = rest
	}
	return firstHeading(body)
}

// frontMatter splits off the metadata block that can open a Markdown file: YAML
// between "---" lines, as Jekyll and most generators write it, or TOML between
// "+++" lines, as Hugo does. ok is false when there is none, and also when the
// block never closes: then its first line was a horizontal rule.
func frontMatter(b []byte) (fm, body []byte, toml, ok bool) {
	first, i := line(b, 0)
	first = bytes.TrimRight(first, " \t")
	if string(first) != "---" && string(first) != "+++" {
		return nil, nil, false, false
	}
	toml = first[0] == '+'
	for start := i; i < len(b); {
		l, next := line(b, i)
		l = bytes.TrimRight(l, " \t")
		if bytes.Equal(l, first) || !toml && string(l) == "..." {
			return b[start:i], b[next:], toml, true
		}
		i = next
	}
	return nil, nil, false, false
}

// fmTitle returns the value of the top-level title key in a front matter block,
// or "". Keys nested under another, such as a title in a list of authors, are
// not the document's.
func fmTitle(fm string, toml bool) string {
	for rest := fm; rest != ""; {
		var l string
		l, rest = cutLine(rest)
		if toml {
			l = strings.TrimLeft(l, " \t")
			if strings.HasPrefix(l, "[") {
				return "" // a table begins: the top-level keys are behind us
			}
			if key, v, ok := strings.Cut(l, "="); ok && strings.EqualFold(unquoteKey(key), "title") {
				return scalar(v, rest, true)
			}
			continue
		}
		if l == "" || l[0] == ' ' || l[0] == '\t' || l[0] == '#' {
			continue // not a top-level key
		}
		key, v, ok := strings.Cut(l, ":")
		if ok && (v == "" || v[0] == ' ' || v[0] == '\t') && strings.EqualFold(unquoteKey(key), "title") {
			return scalar(v, rest, false)
		}
	}
	return ""
}

// unquoteKey returns a key without the spaces or quotes around it.
func unquoteKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) >= 2 && (k[0] == '"' || k[0] == '\'') && k[len(k)-1] == k[0] {
		k = k[1 : len(k)-1]
	}
	return k
}

// scalar reads a YAML or TOML string. v is what follows the key's separator, and
// more is the rest of the block, for a value that goes on over later lines: a
// quoted string spanning lines, a YAML block scalar (| or >), or a plain YAML
// string folded over indented lines. A value that is not a string, such as a
// list, a number or null, gives "".
func scalar(v, more string, toml bool) string {
	v = strings.TrimLeft(v, " \t")
	switch {
	case toml && (strings.HasPrefix(v, `"""`) || strings.HasPrefix(v, `'''`)):
		body := v[3:] + "\n" + more
		end := closeTriple(body, v[:3])
		if end < 0 {
			return ""
		}
		if v[0] == '"' {
			return unescape(body[:end])
		}
		return body[:end]
	case strings.HasPrefix(v, `"`):
		body := v[1:] + "\n" + more
		end := closeQuote(body, '"')
		if end < 0 {
			return ""
		}
		return unescape(body[:end])
	case strings.HasPrefix(v, "'"):
		body := v[1:] + "\n" + more
		end := closeQuote(body, '\'')
		if end < 0 {
			return ""
		}
		return strings.ReplaceAll(body[:end], "''", "'")
	case toml, strings.HasPrefix(v, "["), strings.HasPrefix(v, "{"):
		return "" // a bare TOML value, a list or a table: not a title
	case strings.HasPrefix(v, "|"), strings.HasPrefix(v, ">"):
		return indented(more, false)
	}
	switch v = stripComment(v); v {
	case "":
		// The string starts on the next line, unless a list or a mapping does.
		v = indented(more, true)
		if strings.HasPrefix(v, "- ") || strings.Contains(v, ": ") {
			return ""
		}
		return v
	case "~", "null", "Null", "NULL":
		return ""
	}
	return v + " " + indented(more, true)
}

// indented joins the indented lines at the start of more: the body of a YAML
// block scalar, or the rest of a plain string folded over several lines. A
// comment line ends a plain string.
func indented(more string, plain bool) string {
	var b strings.Builder
	for more != "" {
		var l string
		l, more = cutLine(more)
		t := strings.TrimSpace(l)
		if t != "" && l[0] != ' ' && l[0] != '\t' || plain && strings.HasPrefix(t, "#") {
			break
		}
		b.WriteString(t)
		b.WriteByte(' ')
	}
	return b.String()
}

// stripComment drops a comment from a plain YAML value: a '#' that starts the
// value or follows whitespace.
func stripComment(v string) string {
	for i := range len(v) {
		if v[i] == '#' && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimSpace(v[:i])
		}
	}
	return strings.TrimSpace(v)
}

// closeQuote returns the index of the quote that ends a string opened with q, or
// -1 when it never ends. Inside double quotes a backslash escapes a quote;
// inside single quotes, a doubled quote stands for one.
func closeQuote(s string, q byte) int {
	for i := 0; i < len(s); i++ {
		switch {
		case q == '"' && s[i] == '\\':
			i++
		case s[i] == q && q == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
		case s[i] == q:
			return i
		}
	}
	return -1
}

// closeTriple returns the index of the three quotes, q, that end a TOML
// multi-line string, or -1.
func closeTriple(s, q string) int {
	for i := 0; i+len(q) <= len(s); i++ {
		if q[0] == '"' && s[i] == '\\' {
			i++
			continue
		}
		if s[i:i+len(q)] == q {
			return i
		}
	}
	return -1
}

// unescape decodes the backslash escapes of a YAML double-quoted or TOML basic
// string. An escaped line break, tab or other control becomes a space, as it
// would in a title; an escape it does not know stands for the character after
// the backslash.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch e := s[i]; e {
		case '0', 'a', 'b', 'e', 'f', 'n', 'r', 't', 'v', 'N', 'L', 'P':
			b.WriteByte(' ')
		case '_':
			b.WriteRune(0x00A0) // a no-break space
		case '\r', '\n':
			// An escaped line break joins two lines without a space.
			for i+1 < len(s) && strings.IndexByte(" \t\r\n", s[i+1]) >= 0 {
				i++
			}
		case 'x', 'u', 'U':
			n := 2
			switch e {
			case 'u':
				n = 4
			case 'U':
				n = 8
			}
			r, ok := hexRune(s[i+1:], n)
			if !ok {
				b.WriteByte(e)
				continue
			}
			i += n
			if utf16.IsSurrogate(r) && strings.HasPrefix(s[i+1:], `\u`) {
				// JSON spells a character beyond the BMP as two escapes.
				if lo, ok := hexRune(s[i+3:], 4); ok && utf16.DecodeRune(r, lo) != utf8.RuneError {
					r = utf16.DecodeRune(r, lo)
					i += 6
				}
			}
			if utf8.ValidRune(r) {
				b.WriteRune(r)
			}
		default:
			b.WriteByte(e)
		}
	}
	return b.String()
}

// hexRune reads the n hex digits at the start of s as a character.
func hexRune(s string, n int) (rune, bool) {
	if len(s) < n {
		return 0, false
	}
	v, err := strconv.ParseUint(s[:n], 16, 32)
	return rune(v), err == nil
}

// firstHeading returns the first level-one heading in a Markdown body: a line
// that starts "# ", a paragraph underlined with "=", or an HTML <h1>. Fenced code
// and HTML comments are passed over, since a comment in a shell sample starts
// with "#" too. The first heading with any text decides: when it names nothing,
// the file has no title.
func firstHeading(b []byte) string {
	var (
		fence     byte // the fence character, inside a fenced code block
		fenceLen  int
		comment   bool // inside an HTML comment
		para      = -1 // where the paragraph being read starts, or -1
		paraEnd   int
		htmlTries int // HTML headings tried; see maxHTMLTries
	)
	for i := 0; i < len(b); {
		text, next := line(b, i)
		start := i
		i = next
		cols, rest := indent(text)
		switch {
		case comment:
			comment = !bytes.Contains(text, []byte("-->"))
			continue
		case fence != 0:
			if cols < 4 && closesFence(rest, fence, fenceLen) {
				fence = 0
			}
			continue
		case len(rest) == 0:
			para = -1
			continue
		case cols >= 4:
			if para < 0 {
				continue // indented code
			}
		default:
			if c, n := openFence(rest); n > 0 {
				fence, fenceLen, para = c, n, -1
				continue
			}
			if bytes.HasPrefix(rest, []byte("<!--")) {
				comment = !bytes.Contains(rest[4:], []byte("-->"))
				para = -1
				continue
			}
			if level, h := atx(rest); level > 0 {
				para = -1
				if level == 1 {
					if s := markdownText(h); s != "" {
						return clean(s)
					}
				}
				continue
			}
			if para >= 0 && underline(rest, '=') {
				if s := markdownText(b[para:paraEnd]); s != "" {
					return clean(s)
				}
				para = -1
				continue
			}
			if (para >= 0 && underline(rest, '-')) || thematicBreak(rest) {
				para = -1 // a level-two heading, or a rule
				continue
			}
			if h1Tag(rest) && htmlTries < maxHTMLTries {
				htmlTries++
				at := start + len(text) - len(rest)
				if s, end := htmlH1(b[:min(len(b), at+htmlH1Limit)], at); end >= 0 {
					if s != "" {
						return heading(s)
					}
					_, i = line(b, end)
					para = -1
					continue
				}
			}
			if rest[0] == '>' || listItem(rest) {
				para = -1 // a quotation or a list item: not a heading's paragraph
				continue
			}
		}
		if para < 0 {
			para = start
		}
		paraEnd = start + len(text)
	}
	return ""
}

// line returns the line that starts at b[i], without its line break, and the
// index of the next line. A break is "\n", "\r\n", or a lone "\r".
func line(b []byte, i int) (text []byte, next int) {
	k := bytes.IndexAny(b[i:], "\r\n")
	if k < 0 {
		return b[i:], len(b)
	}
	next = i + k + 1
	if b[i+k] == '\r' && next < len(b) && b[next] == '\n' {
		next++
	}
	return b[i : i+k], next
}

// cutLine is line for a string: the first line of s, and what follows its break.
func cutLine(s string) (line, rest string) {
	k := strings.IndexAny(s, "\r\n")
	if k < 0 {
		return s, ""
	}
	rest = s[k+1:]
	if s[k] == '\r' && strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	return s[:k], rest
}

// indent measures a line's indentation in columns, with a tab stop every four,
// and returns the rest of the line.
func indent(s []byte) (int, []byte) {
	cols := 0
	for i, c := range s {
		switch c {
		case ' ':
			cols++
		case '\t':
			cols += 4 - cols%4
		default:
			return cols, s[i:]
		}
	}
	return cols, nil
}

// openFence returns the character and length of the run that opens a fenced
// code block: three or more backticks or tildes. A backtick fence's info string
// may not hold a backtick, or the line is inline code instead.
func openFence(s []byte) (byte, int) {
	if len(s) == 0 || s[0] != '`' && s[0] != '~' {
		return 0, 0
	}
	n := run(s, 0, s[0])
	if n < 3 || s[0] == '`' && bytes.IndexByte(s[n:], '`') >= 0 {
		return 0, 0
	}
	return s[0], n
}

// closesFence reports whether s, a line inside a fenced code block, closes it: a
// run of the fence character at least as long as the one that opened it, and
// nothing after but spaces.
func closesFence(s []byte, c byte, n int) bool {
	k := run(s, 0, c)
	return k >= n && len(bytes.Trim(s[k:], " \t")) == 0
}

// atx reads an ATX heading, given a line without its indentation: its level, 1
// to 6, and its text without a closing run of #s. level is 0 when the line is no
// heading; "#hashtag" is not one.
func atx(s []byte) (level int, text []byte) {
	n := run(s, 0, '#')
	if n == 0 || n > 6 || n < len(s) && s[n] != ' ' && s[n] != '\t' {
		return 0, nil
	}
	text = bytes.Trim(s[n:], " \t")
	k := len(text)
	for k > 0 && text[k-1] == '#' {
		k--
	}
	if k == 0 {
		return n, nil
	}
	if k < len(text) && (text[k-1] == ' ' || text[k-1] == '\t') {
		text = bytes.TrimRight(text[:k], " \t")
	}
	return n, text
}

// underline reports whether s is a Setext underline: a run of c, then nothing
// but spaces.
func underline(s []byte, c byte) bool {
	n := run(s, 0, c)
	return n > 0 && len(bytes.Trim(s[n:], " \t")) == 0
}

// thematicBreak reports whether s is a horizontal rule: three or more of one of
// "-", "*" or "_", with nothing but spaces between them.
func thematicBreak(s []byte) bool {
	if len(s) == 0 || s[0] != '-' && s[0] != '*' && s[0] != '_' {
		return false
	}
	n := 0
	for _, c := range s {
		switch c {
		case s[0]:
			n++
		case ' ', '\t':
		default:
			return false
		}
	}
	return n >= 3
}

// listItem reports whether s starts a list item: a bullet, or a number and a
// period or parenthesis, then a space or the end of the line.
func listItem(s []byte) bool {
	n := 0
	if len(s) > 0 && (s[0] == '-' || s[0] == '*' || s[0] == '+') {
		n = 1
	} else {
		for n < len(s) && n < 9 && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		if n == 0 || n == len(s) || s[n] != '.' && s[n] != ')' {
			return false
		}
		n++
	}
	return n == len(s) || s[n] == ' ' || s[n] == '\t'
}

// markdownText returns a heading's text as a reader sees it, normalized. Only its
// first inlineLimit bytes are read.
func markdownText(raw []byte) string {
	return normalize(inline(decode(clip(raw, inlineLimit), "")))
}

const (
	// htmlH1Limit bounds how far an HTML <h1> in a Markdown file is followed. A
	// README's centered heading closes within a few lines.
	htmlH1Limit = 4 << 10
	// maxHTMLTries bounds how many <h1> lines are tried. A tag that never closes
	// sends the scan to htmlH1Limit; a file of such lines must not do it for each.
	maxHTMLTries = 8
)

// h1Tag reports whether a line opens with an HTML <h1> tag, as READMEs do to set
// a centered heading under a logo.
func h1Tag(s []byte) bool {
	return len(s) > 3 && equalFold(s[:3], "<h1") && (htmlSpace(s[3]) || s[3] == '>' || s[3] == '/')
}

// htmlH1 reads the HTML h1 element that starts at b[at] in a Markdown file, and
// returns its normalized text and the index where it ends. end is -1 when b[at]
// does not start a whole h1.
func htmlH1(b []byte, at int) (text string, end int) {
	t := readTag(b, at)
	if t.next < 0 || t.self || t.name != "h1" {
		return "", -1
	}
	raw, end := h1Text(b, t.next)
	if end < 0 {
		return "", -1
	}
	return normalize(html.UnescapeString(decode(raw, ""))), end
}

// inlineLimit bounds the Markdown read for one heading. A title needs a fraction
// of it, and matching brackets costs time in proportion to its square.
const inlineLimit = 2 << 10

// inline reduces a heading's Markdown to the text a reader sees: link text
// without its address, code without its backticks, words without the asterisks
// around them, character references decoded. Images go entirely: beside a
// README's title, they are build badges.
func inline(s string) string {
	if len(s) > inlineLimit {
		i := inlineLimit
		for i > 0 && !utf8.RuneStart(s[i]) {
			i--
		}
		s = s[:i]
	}
	var out strings.Builder
	var open []link // links whose text is being read, innermost last
	for i := 0; i < len(s); {
		if n := len(open); n > 0 && i >= open[n-1].bracket {
			if i == open[n-1].bracket {
				i = open[n-1].end // past "](address)"
			}
			open = open[:n-1]
			continue
		}
		switch c := s[i]; c {
		case '\\':
			if i+1 < len(s) && asciiPunct(s[i+1]) {
				out.WriteByte(s[i+1])
				i += 2
				continue
			}
		case '`':
			n := run(s, i, '`')
			if end := codeEnd(s, i+n, n); end >= 0 {
				out.WriteString(s[i+n : end])
				i = end + n
			} else {
				out.WriteString(s[i : i+n])
				i += n
			}
			continue
		case '!':
			if l, ok := linkAt(s, i+1); ok {
				i = l.end // an image, and its description with it
				continue
			}
		case '[':
			if l, ok := linkAt(s, i); ok {
				open = append(open, l)
				i++
				continue
			}
		case '<':
			if end := inlineTag(s, i); end >= 0 {
				i = end
				continue
			}
		case '*', '_':
			n := run(s, i, c)
			if !emphasis(s, i, n) {
				out.WriteString(s[i : i+n])
			}
			i += n
			continue
		case '&':
			if text, end := entity(s, i); end >= 0 {
				out.WriteString(text)
				i = end
				continue
			}
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// link is a Markdown link or image: bracket is the index of the ']' that ends
// its text, and end the index just past its destination.
type link struct{ bracket, end int }

// linkAt reads the link whose text opens with the '[' at s[i]: [text](address)
// or [text][reference].
func linkAt(s string, i int) (link, bool) {
	if i >= len(s) || s[i] != '[' {
		return link{}, false
	}
	bracket := matchBracket(s, i, '[', ']')
	if bracket < 0 || bracket+1 >= len(s) {
		return link{}, false
	}
	end := -1
	switch s[bracket+1] {
	case '(':
		end = matchBracket(s, bracket+1, '(', ')')
	case '[':
		end = matchBracket(s, bracket+1, '[', ']')
	}
	if end < 0 {
		return link{}, false
	}
	return link{bracket, end + 1}, true
}

// matchBracket returns the index of the bracket that closes the one at s[i],
// counting nested pairs and passing over escaped ones, or -1.
func matchBracket(s string, i int, open, shut byte) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case open:
			depth++
		case shut:
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// codeEnd returns the index of the next run of exactly n backticks at or after
// s[i], the run that closes a code span, or -1.
func codeEnd(s string, i, n int) int {
	for i < len(s) {
		k := strings.IndexByte(s[i:], '`')
		if k < 0 {
			return -1
		}
		i += k
		m := run(s, i, '`')
		if m == n {
			return i
		}
		i += m
	}
	return -1
}

// inlineTag returns the index just past the HTML tag or autolink at s[i], or -1.
func inlineTag(s string, i int) int {
	if i+1 >= len(s) || !asciiLetter(s[i+1]) && s[i+1] != '/' && s[i+1] != '!' && s[i+1] != '?' {
		return -1
	}
	k := strings.IndexByte(s[i:], '>')
	if k < 0 {
		return -1
	}
	return i + k + 1
}

// entity decodes the character reference at s[i], such as &amp; or &#8212;, and
// returns the index after it, or -1 when s[i] starts none.
func entity(s string, i int) (string, int) {
	k := strings.IndexByte(s[i:min(len(s), i+32)], ';')
	if k < 0 {
		return "", -1
	}
	ref := s[i : i+k+1]
	if text := html.UnescapeString(ref); text != ref {
		return text, i + k + 1
	}
	return "", -1
}

// emphasis reports whether the run of n asterisks or underscores at s[i] marks
// emphasis, by CommonMark's flanking rules, rather than standing for itself. An
// underscore inside a word, as in snake_case, stands for itself.
func emphasis(s string, i, n int) bool {
	before, after := ' ', ' '
	if i > 0 {
		before, _ = utf8.DecodeLastRuneInString(s[:i])
	}
	if i+n < len(s) {
		after, _ = utf8.DecodeRuneInString(s[i+n:])
	}
	left := !unicode.IsSpace(after) && (!punct(after) || unicode.IsSpace(before) || punct(before))
	right := !unicode.IsSpace(before) && (!punct(before) || unicode.IsSpace(after) || punct(after))
	if s[i] == '*' {
		return left || right
	}
	return left && (!right || punct(before)) || right && (!left || punct(after))
}

func punct(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }

func asciiPunct(c byte) bool { return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0 }

// run counts the copies of c that start at s[i].
func run[T ~string | ~[]byte](s T, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}
