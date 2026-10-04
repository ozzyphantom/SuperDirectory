package sniff

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// text names b as one of the text types (txt, json, html, xml, svg), or returns
// "" when b is not text. Text is valid UTF-8, or UTF-16 or UTF-32 behind a byte
// order mark, with no NUL and few control characters. A legacy encoding such as
// Latin-1 cannot be told from binary data, so it is not called text.
func text(b []byte, whole bool) string {
	s, ok := decode(b, whole)
	if !ok {
		return ""
	}
	return markup(s, whole)
}

// decode returns b's text as UTF-8 without its byte order mark, and whether b is
// text at all. A head that is not the whole file may end partway through a
// character; that character is dropped.
func decode(b []byte, whole bool) ([]byte, bool) {
	next, recode := utf8Next, true
	switch {
	case hasPrefix(b, "\x00\x00\xfe\xff"):
		b, next = b[4:], utf32BE
	case hasPrefix(b, "\xff\xfe\x00\x00"):
		b, next = b[4:], utf32LE
	case hasPrefix(b, "\xfe\xff"):
		b, next = b[2:], utf16BE
	case hasPrefix(b, "\xff\xfe"):
		b, next = b[2:], utf16LE
	default:
		b, recode = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), false
	}
	var out []byte // UTF-16 and UTF-32 re-encoded as UTF-8; UTF-8 is used as it is
	runes, controls, i := 0, 0, 0
	for i < len(b) {
		r, n, ok := next(b[i:])
		if n == 0 { // the input ends partway through a character
			if whole {
				return nil, false
			}
			break
		}
		if !ok || r == 0 {
			return nil, false
		}
		runes++
		if !printable(r) {
			controls++
		}
		if recode {
			out = utf8.AppendRune(out, r)
		}
		i += n
	}
	// Real text can carry a stray control character, so one in 32 is forgiven.
	if runes == 0 || controls*32 > runes {
		return nil, false
	}
	if !recode {
		out = b[:i]
	}
	return out, true
}

// printable reports whether r belongs in text: anything but a control character,
// except the controls real text files carry. Those are tab, the line and page
// breaks, BEL, backspace (man pages overstrike with it), the DOS end-of-file mark,
// and the escape that starts a terminal color code.
func printable(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', '\a', '\b', 0x1a, 0x1b:
		return true
	}
	return !unicode.IsControl(r)
}

// utf8Next decodes one UTF-8 character. A length of 0 means b ends partway
// through one; ok is false for an invalid sequence.
func utf8Next(b []byte) (r rune, n int, ok bool) {
	if !utf8.FullRune(b) {
		return 0, 0, true
	}
	r, n = utf8.DecodeRune(b)
	return r, n, r != utf8.RuneError || n > 1
}

func utf16LE(b []byte) (rune, int, bool) { return utf16Next(b, binary.LittleEndian) }
func utf16BE(b []byte) (rune, int, bool) { return utf16Next(b, binary.BigEndian) }
func utf32LE(b []byte) (rune, int, bool) { return utf32Next(b, binary.LittleEndian) }
func utf32BE(b []byte) (rune, int, bool) { return utf32Next(b, binary.BigEndian) }

// utf16Next decodes one UTF-16 character, joining a surrogate pair. A surrogate
// without its partner is invalid.
func utf16Next(b []byte, bo binary.ByteOrder) (rune, int, bool) {
	if len(b) < 2 {
		return 0, 0, true
	}
	hi := rune(bo.Uint16(b))
	switch {
	case hi < 0xd800 || hi > 0xdfff:
		return hi, 2, true
	case hi > 0xdbff:
		return 0, 2, false // a low surrogate first
	case len(b) < 4:
		return 0, 0, true
	}
	lo := rune(bo.Uint16(b[2:]))
	if lo < 0xdc00 || lo > 0xdfff {
		return 0, 2, false
	}
	return 0x10000 + (hi-0xd800)<<10 + (lo - 0xdc00), 4, true
}

// utf32Next decodes one UTF-32 character, refusing values beyond Unicode and
// surrogates.
func utf32Next(b []byte, bo binary.ByteOrder) (rune, int, bool) {
	if len(b) < 4 {
		return 0, 0, true
	}
	u := bo.Uint32(b)
	if u > unicode.MaxRune || (u >= 0xd800 && u <= 0xdfff) {
		return 0, 4, false
	}
	return rune(u), 4, true
}

// markup names decoded text by how it starts: SVG, HTML or XML by its prolog,
// JSON by parsing the whole file, and plain text otherwise.
func markup(s []byte, whole bool) string {
	t := bytes.TrimLeft(s, " \t\r\n\f")
	if len(t) > 0 && t[0] == '<' {
		if m := prolog(t); m != "" {
			return m
		}
	}
	// Only an object or an array counts. A file holding "42" or "true" is valid
	// JSON, but naming it .json would be no help to anyone.
	if whole && len(t) > 0 && (t[0] == '{' || t[0] == '[') && json.Valid(t) {
		return "json"
	}
	return "txt"
}

// prolog reads markup's prolog (an XML declaration, comments, processing
// instructions, a doctype) up to the first element, and names the document by
// what it found. It returns "" for markup that is none of SVG, HTML and XML.
func prolog(t []byte) string {
	declared := false
	doctype, root := "", ""
	for range 64 { // a prolog has a few parts; this bounds a hostile one
		t = bytes.TrimLeft(t, " \t\r\n\f")
		ok := false
		switch {
		case len(t) == 0:
			// the head ended inside the prolog
		case hasPrefixFold(t, "<?xml") && len(t) > 5 && isSpace(t[5]):
			declared = true
			t, ok = past(t, "?>")
		case hasPrefix(t, "<?"):
			t, ok = past(t, "?>")
		case hasPrefix(t, "<!--"):
			t, ok = past(t[4:], "-->")
		case hasPrefixFold(t, "<!doctype"):
			doctype = markupName(bytes.TrimLeft(t[9:], " \t\r\n\f"))
			t, ok = pastDoctype(t)
		case t[0] == '<':
			root = markupName(t[1:])
		}
		if !ok {
			break
		}
	}
	return verdict(declared, doctype, root)
}

// verdict names a document by its prolog. An XML declaration makes it XML, or SVG
// with an svg root. Without one, an HTML doctype or an html, head, body or title
// element makes it HTML, and an svg root SVG. A doctype stands in for a root the
// head did not reach.
func verdict(declared bool, doctype, root string) string {
	svg := strings.EqualFold(root, "svg") || strings.HasSuffix(strings.ToLower(root), ":svg") ||
		(root == "" && strings.EqualFold(doctype, "svg"))
	switch {
	case declared && svg:
		return "svg"
	case declared:
		return "xml"
	case strings.EqualFold(doctype, "html"):
		return "html"
	case svg:
		return "svg"
	}
	switch strings.ToLower(root) {
	case "html", "head", "body", "title":
		return "html"
	}
	return ""
}

// markupName reads a markup name: everything up to whitespace, '>', '/' or '['.
func markupName(b []byte) string {
	i := bytes.IndexFunc(b, func(r rune) bool {
		return r == '>' || r == '/' || r == '[' || unicode.IsSpace(r)
	})
	if i < 0 {
		i = len(b)
	}
	return string(b[:min(i, 64)])
}

// past returns b after the first sep, and false when b holds none.
func past(b []byte, sep string) ([]byte, bool) {
	i := bytes.Index(b, []byte(sep))
	if i < 0 {
		return nil, false
	}
	return b[i+len(sep):], true
}

// pastDoctype returns b after its doctype declaration. An internal subset in
// brackets can hold quoted strings and comments, and either can contain '>'.
func pastDoctype(b []byte) ([]byte, bool) {
	var quote byte
	depth := 0
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[':
			depth++
		case c == ']' && depth > 0:
			depth--
		case c == '<' && depth > 0 && hasPrefix(b[i:], "<!--"):
			j := bytes.Index(b[i+4:], []byte("-->"))
			if j < 0 {
				return nil, false
			}
			i += 4 + j + 2 // to the comment's closing '>'
		case c == '>' && depth == 0:
			return b[i+1:], true
		}
	}
	return nil, false
}

func hasPrefixFold(b []byte, s string) bool {
	return len(b) >= len(s) && strings.EqualFold(string(b[:len(s)]), s)
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f'
}
