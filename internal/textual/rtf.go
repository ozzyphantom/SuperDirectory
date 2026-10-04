package textual

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"unicode/utf16"
)

var errNotRTF = errors.New("rtf: no {\\rtf header")

// rtfSkipped are the destinations whose text is not part of the document: the
// font, colour, style and list tables, metadata, pictures, embedded objects and
// field instructions. Destinations marked \* are skipped as well; a reader that
// does not know one is meant to.
var rtfSkipped = map[string]bool{
	"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true,
	"pict": true, "object": true, "themedata": true, "datastore": true,
	"listtable": true, "listoverridetable": true, "rsidtbl": true,
	"generator": true, "xmlnstbl": true, "latentstyles": true, "filetbl": true,
	"revtbl": true, "fldinst": true, "nonshppict": true, "sp": true,
	"footnote": true, "annotation": true, "xe": true, "tc": true,
}

// rtfSymbols are the control words that stand for one character.
var rtfSymbols = map[string]string{
	"emdash": "\u2014", "endash": "\u2013", "bullet": "\u2022",
	"lquote": "\u2018", "rquote": "\u2019", "ldblquote": "\u201C", "rdblquote": "\u201D",
	"emspace": " ", "enspace": " ", "qmspace": " ",
}

// rtfGroup is the state a group inherits and restores.
type rtfGroup struct {
	skip bool // inside a destination that holds no document text
	uc   int  // fallback characters that follow a \u character
}

// maxRTFDepth bounds the group stack. Real documents nest a few dozen deep; a
// deeper group shares its parent's state.
const maxRTFDepth = 1024

// rtf reads Rich Text Format as a stream: a document padded with megabytes of
// picture data is scanned once, never held in memory.
func rtf(r io.ReaderAt, size int64, limit int) (string, error) {
	p := &rtfParser{
		in:  bufio.NewReaderSize(io.NewSectionReader(r, 0, size), 64<<10),
		o:   newBuilder(limit),
		cur: rtfGroup{uc: 1},
	}
	if !p.header() {
		return "", errNotRTF
	}
	err := p.parse()
	p.flushRun()
	return p.o.String(), err
}

type rtfParser struct {
	in    *bufio.Reader
	o     *builder
	run   strings.Builder // text not yet handed to o
	cur   rtfGroup
	stack []rtfGroup
	deep  int  // groups opened past maxRTFDepth
	fresh bool // at the start of a group, where a destination word may appear
	fall  int  // fallback characters still to skip after a \u character
	high  rune // a high surrogate waiting for its pair
}

// header skips leading whitespace and a byte-order mark, and reports whether
// the text starts as RTF must.
func (p *rtfParser) header() bool {
	for {
		b, err := p.in.Peek(1)
		if err != nil {
			return false
		}
		if b[0] != ' ' && b[0] != '\t' && b[0] != '\r' && b[0] != '\n' {
			break
		}
		p.in.Discard(1)
	}
	if b, _ := p.in.Peek(3); string(b) == "\xEF\xBB\xBF" {
		p.in.Discard(3)
	}
	b, _ := p.in.Peek(5)
	return string(b) == `{\rtf`
}

func (p *rtfParser) parse() error {
	for !p.o.full() {
		c, err := p.in.ReadByte()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch c {
		case '{':
			p.open()
		case '}':
			if !p.close() {
				return nil // the document's own group has closed
			}
		case '\\':
			if err := p.control(); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
		case '\r', '\n':
			// Line breaks in the source only wrap it; \par makes paragraphs.
		case '\t':
			p.char(func() { p.flushRun(); p.o.raw("\t") })
		default:
			p.fresh = false
			if p.fall > 0 {
				p.fall--
				continue
			}
			if !p.cur.skip {
				p.emit(cp1252(c))
			}
		}
	}
	return nil
}

func (p *rtfParser) open() {
	p.fresh, p.fall = true, 0
	if len(p.stack) >= maxRTFDepth {
		p.deep++
		return
	}
	p.stack = append(p.stack, p.cur)
}

// close ends a group and reports whether any remain open.
func (p *rtfParser) close() bool {
	p.fresh, p.fall = false, 0
	if p.deep > 0 {
		p.deep--
		return true
	}
	if len(p.stack) == 0 {
		return false
	}
	p.cur = p.stack[len(p.stack)-1]
	p.stack = p.stack[:len(p.stack)-1]
	return len(p.stack) > 0
}

// control reads what follows a backslash: a control word with an optional
// numeric parameter, or a control symbol.
func (p *rtfParser) control() error {
	c, err := p.in.ReadByte()
	if err != nil {
		return err
	}
	if !isASCIILetter(c) {
		return p.symbol(c)
	}
	var word [32]byte
	n := 0
	for isASCIILetter(c) {
		if n < len(word) {
			word[n] = c
			n++
		}
		if c, err = p.in.ReadByte(); err != nil {
			break
		}
	}
	param, hasParam := 0, false
	if err == nil && (c == '-' || isDigit(c)) {
		neg := c == '-'
		if neg {
			c, err = p.in.ReadByte()
		}
		for digits := 0; err == nil && isDigit(c); digits++ {
			if digits < 10 {
				param = param*10 + int(c-'0')
			}
			hasParam = true
			c, err = p.in.ReadByte()
		}
		if neg {
			param = -param
		}
	}
	if err == nil && c != ' ' {
		p.in.UnreadByte() // the delimiter belongs to what follows
	}
	p.word(string(word[:n]), param, hasParam)
	if err == io.EOF {
		return nil
	}
	return err
}

func (p *rtfParser) word(w string, param int, hasParam bool) {
	if w == "bin" { // binary data follows, and may hold anything, braces included
		if param > 0 {
			p.in.Discard(param)
		}
		return
	}
	if p.fresh && rtfSkipped[w] {
		p.cur.skip = true
	}
	p.fresh = false
	if p.fall > 0 {
		p.fall-- // a control word counts as one fallback character
		return
	}
	if p.cur.skip {
		return
	}
	switch w {
	case "par", "line", "sect", "page":
		p.flushRun()
		p.o.newline()
	case "row":
		p.flushRun()
		p.o.block(1)
	case "cell", "nestcell":
		p.flushRun()
		p.o.cell()
	case "tab":
		p.flushRun()
		p.o.raw("\t")
	case "uc":
		if hasParam {
			p.cur.uc = min(max(param, 0), 16)
		}
	case "u":
		if hasParam {
			if param < 0 {
				param += 65536 // \u takes a signed 16-bit value
			}
			p.emit(rune(param))
			p.fall = p.cur.uc
		}
	default:
		if s, ok := rtfSymbols[w]; ok {
			p.emitString(s)
		}
	}
}

// symbol handles a control symbol: a backslash and one non-letter.
func (p *rtfParser) symbol(c byte) error {
	switch c {
	case '*':
		if p.fresh {
			p.cur.skip = true
		}
		return nil
	case '\'':
		var hex [2]byte
		for i := range hex {
			b, err := p.in.ReadByte()
			if err != nil {
				return err
			}
			hex[i] = b
		}
		hi, ok1 := unhex(hex[0])
		lo, ok2 := unhex(hex[1])
		p.char(func() {
			if ok1 && ok2 {
				p.emit(cp1252(hi<<4 | lo))
			}
		})
	case '\\', '{', '}':
		p.char(func() { p.emit(rune(c)) })
	case '~':
		p.char(func() { p.emit(' ') })
	case '_':
		p.char(func() { p.emit('-') })
	case '\r', '\n': // a backslash before a line break is a \par
		p.char(func() { p.flushRun(); p.o.newline() })
	default:
		p.char(func() {}) // \- optional hyphens and the rest show nothing
	}
	return nil
}

// char runs f for one character of text, unless it is a \u character's fallback
// or sits in a skipped destination.
func (p *rtfParser) char(f func()) {
	p.fresh = false
	if p.fall > 0 {
		p.fall--
		return
	}
	if !p.cur.skip {
		f()
	}
}

// emit adds one character to the pending run, pairing UTF-16 surrogates that
// arrive as two \u words. A long run is handed on before it ends, so a paragraph
// that never breaks still meets the limit.
func (p *rtfParser) emit(r rune) {
	if p.high != 0 {
		hi := p.high
		p.high = 0
		if utf16.IsSurrogate(r) && r >= 0xDC00 {
			p.run.WriteRune(utf16.DecodeRune(hi, r))
			return
		}
		p.run.WriteRune('\uFFFD')
	}
	switch {
	case r >= 0xD800 && r < 0xDC00:
		p.high = r
	case utf16.IsSurrogate(r) || r > 0x10FFFF:
		p.run.WriteRune('\uFFFD')
	default:
		p.run.WriteRune(r)
	}
	if p.run.Len() >= 4<<10 {
		p.o.text(p.run.String())
		p.run.Reset()
	}
}

func (p *rtfParser) emitString(s string) {
	for _, r := range s {
		p.emit(r)
	}
}

// flushRun hands pending text to the builder, which collapses its whitespace.
func (p *rtfParser) flushRun() {
	if p.high != 0 {
		p.high = 0
		p.run.WriteRune('\uFFFD')
	}
	if p.run.Len() > 0 {
		p.o.text(p.run.String())
		p.run.Reset()
	}
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
