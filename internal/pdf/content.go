package pdf

import (
	"errors"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// matrix is a transformation matrix [a b c d e f].
type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

// mul returns m × n: m applied first, then n.
func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

// translate returns m moved by (tx, ty) in its own space.
func (m matrix) translate(tx, ty float64) matrix {
	m[4] += tx*m[0] + ty*m[2]
	m[5] += tx*m[1] + ty*m[3]
	return m
}

// textOut collects text. Runs of white space fold into one separator, which is
// written only when more text follows, so lines carry no trailing spaces.
//
// Glyphs are drawn left to right, so Hebrew or Arabic arrives in visual order,
// each word back to front. textOut holds a run of right-to-left glyphs until the
// word ends and writes it reversed, glyph by glyph, so a ligature that stands for
// two letters keeps them in order. Words keep their order on the line.
type textOut struct {
	b       []byte
	runes   int
	limit   int
	full    bool
	pending byte     // a separator owed before the next text: ' ' or '\n'
	rtl     []string // glyphs held for reversal; combining marks wait here too
	hasRTL  bool     // rtl holds a right-to-left letter, not only marks
}

func (t *textOut) sep(c byte) {
	t.flush()
	if c == '\n' || t.pending == 0 {
		t.pending = c
	}
}

// write adds the text of one glyph.
func (t *textOut) write(s string) {
	switch {
	case s == "":
	case isRTL(s):
		t.rtl = append(t.rtl, s)
		t.hasRTL = true
		if len(t.rtl) >= 1024 {
			t.flush()
		}
	case isMark(s):
		// In visual order a mark comes before its base, which may be the next glyph.
		t.rtl = append(t.rtl, s)
	default:
		t.flush()
		t.emit(s)
	}
}

// flush writes the held glyphs: reversed when they are a right-to-left word.
func (t *textOut) flush() {
	if len(t.rtl) == 0 {
		return
	}
	if t.hasRTL {
		slices.Reverse(t.rtl)
	}
	for _, g := range t.rtl {
		t.emit(g)
	}
	t.rtl, t.hasRTL = t.rtl[:0], false
}

func isRTL(s string) bool {
	if s[0] < 0x80 {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	return !unicode.IsDigit(r) && unicode.In(r, unicode.Hebrew, unicode.Arabic, unicode.Syriac, unicode.Thaana, unicode.Nko)
}

func isMark(s string) bool {
	if s[0] < 0x80 {
		return false
	}
	for _, r := range s {
		if !unicode.Is(unicode.Mn, r) {
			return false
		}
	}
	return true
}

func (t *textOut) emit(s string) {
	for _, r := range s {
		switch {
		case t.full:
			return
		case unicode.IsSpace(r):
			if t.pending == 0 {
				t.pending = ' '
			}
		case r < 0x20, r == 0x7F, r >= 0x80 && r < 0xA0, r == 0x200B, r == 0xFEFF, r == utf8.RuneError:
			// Controls and invisible marks carry no text.
		case r == 0xAD: // a soft hyphen in a PDF is a hyphen someone sees
			t.put('-')
		case r >= 0xFB00 && r <= 0xFB06: // ligatures read better spelled out
			for _, c := range ligatures[r-0xFB00] {
				t.put(c)
			}
		default:
			t.put(r)
		}
	}
}

var ligatures = [...]string{"ff", "fi", "fl", "ffi", "ffl", "st", "st"}

func (t *textOut) put(r rune) {
	if t.full {
		return
	}
	if t.pending != 0 && len(t.b) > 0 {
		t.b = append(t.b, t.pending)
		if t.runes++; t.runes >= t.limit {
			t.full = true
			return
		}
	}
	t.pending = 0
	t.b = utf8.AppendRune(t.b, r)
	if t.runes++; t.runes >= t.limit {
		t.full = true
	}
}

func (t *textOut) String() string {
	t.flush()
	return strings.TrimRight(string(t.b), " \n")
}

// gstate is the part of the graphics state that places text.
type gstate struct {
	ctm                  matrix
	font                 *font
	size                 float64 // Tf's size
	tc, tw, th, tl, rise float64 // character and word spacing, horizontal scale, leading, rise
}

// interp runs content streams for their text. It tracks where each run of glyphs
// starts and ends on the page, in device space, and judges from the gap between
// runs whether a space or a new line separates them.
type interp struct {
	d      *Doc
	out    *textOut
	gs     gstate
	stack  []gstate
	floor  int // the stack depth a running form XObject started at
	extra  int // q's past the stack cap, waiting for their Q's
	tm     matrix
	tlm    matrix
	penX   float64 // where the last run of glyphs ended
	penY   float64
	penEm  float64 // that run's font size on the page
	hasPen bool
	forms  []uint32 // form XObjects being run, to refuse one that draws itself
	stop   bool
}

// maxArgs caps the operands kept for one operator; none takes more than six.
const maxArgs = 64

func (x *interp) page(p page) {
	x.gs = gstate{ctm: identity, th: 1}
	x.stack, x.floor, x.extra = x.stack[:0], 0, 0
	x.tm, x.tlm = identity, identity
	x.hasPen = false
	x.forms = x.forms[:0]
	x.run(x.d.contents(p.d), p.res, 0)
}

// contents returns a page's content: one stream, or several joined.
func (d *Doc) contents(pg dict) []byte {
	switch c := d.resolve(pg["Contents"]).(type) {
	case *stream:
		b, _ := d.decode(c)
		return b
	case array:
		var all []byte
		for _, v := range c {
			s, ok := d.resolve(v).(*stream)
			if !ok {
				continue
			}
			b, err := d.decode(s)
			if errors.Is(err, errBudget) || len(all)+len(b) >= maxStream {
				break // a page's content is capped like one stream
			}
			if err != nil {
				continue
			}
			all = append(append(all, b...), '\n')
		}
		return all
	}
	return nil
}

func (x *interp) run(data []byte, res dict, depth int) {
	l := newLexer(data)
	args := make([]any, 0, 8)
	for !x.stop {
		tok, err := l.token()
		if err != nil {
			return
		}
		if k, ok := tok.(keyword); ok {
			switch k {
			case "[":
				if tok, err = l.array(1); err != nil {
					return
				}
			case "<<":
				if tok, err = l.dict(1); err != nil {
					return
				}
			case "null":
				tok = nil
			case "]", ">>", "{", "}", ")", ">":
				continue
			case "BI":
				skipInlineImage(l)
				args = args[:0]
				continue
			default:
				if x.d.ops--; x.d.ops < 0 {
					x.stop = true
					return
				}
				x.op(k, args, res, depth)
				args = args[:0]
				continue
			}
		}
		if len(args) == maxArgs {
			args = append(args[:0], args[1:]...)
		}
		args = append(args, tok)
	}
}

func (x *interp) op(k keyword, args []any, res dict, depth int) {
	gs := &x.gs
	switch k {
	case "q":
		if len(x.stack) < 256 {
			x.stack = append(x.stack, *gs)
		} else {
			x.extra++
		}
	case "Q":
		if x.extra > 0 {
			x.extra--
		} else if n := len(x.stack); n > x.floor {
			*gs = x.stack[n-1]
			x.stack = x.stack[:n-1]
		}
	case "cm":
		if m, ok := matrixArgs(args); ok {
			gs.ctm = m.mul(gs.ctm)
		}
	case "BT":
		x.tm, x.tlm = identity, identity
	case "Tc":
		setLast(&gs.tc, args)
	case "Tw":
		setLast(&gs.tw, args)
	case "TL":
		setLast(&gs.tl, args)
	case "Ts":
		setLast(&gs.rise, args)
	case "Tz":
		if v, ok := lastNum(args); ok {
			gs.th = v / 100
		}
	case "Tf":
		if len(args) >= 2 {
			if n, ok := args[len(args)-2].(name); ok {
				gs.font = x.d.fontFor(res, n)
			}
			setLast(&gs.size, args)
		}
	case "Td", "TD":
		if len(args) >= 2 {
			tx, ok1 := num(args[len(args)-2])
			ty, ok2 := num(args[len(args)-1])
			if ok1 && ok2 {
				if k == "TD" {
					gs.tl = -ty
				}
				x.newLine(tx, ty)
			}
		}
	case "Tm":
		if m, ok := matrixArgs(args); ok {
			x.tm, x.tlm = m, m
		}
	case "T*":
		x.newLine(0, -gs.tl)
	case "Tj", "'", `"`:
		if k == `"` && len(args) >= 3 {
			if v, ok := num(args[len(args)-3]); ok {
				gs.tw = v
			}
			if v, ok := num(args[len(args)-2]); ok {
				gs.tc = v
			}
		}
		if k != "Tj" {
			x.newLine(0, -gs.tl)
		}
		if len(args) > 0 {
			if s, ok := args[len(args)-1].(string); ok {
				x.show(s)
			}
		}
	case "TJ":
		if len(args) > 0 {
			if a, ok := args[len(args)-1].(array); ok {
				x.showArray(a)
			}
		}
	case "Do":
		if len(args) > 0 {
			if n, ok := args[len(args)-1].(name); ok {
				x.form(res, n, depth)
			}
		}
	}
}

func (x *interp) newLine(tx, ty float64) {
	x.tlm = x.tlm.translate(tx, ty)
	x.tm = x.tlm
}

// trm maps text space, where one unit is one em of the current font, to the page.
func (x *interp) trm() matrix {
	gs := &x.gs
	return matrix{gs.size * gs.th, 0, 0, gs.size, 0, gs.rise}.mul(x.tm).mul(gs.ctm)
}

// show writes a string's text and moves the text position past its glyphs.
func (x *interp) show(s string) {
	gs := &x.gs
	f := gs.font
	if f == nil {
		f = defaultFont
	}
	start := x.trm()
	x.separate(start, f.vertical)
	var adv float64
	f.each(s, func(code uint32, n int, text string, w float64) {
		if !x.out.full {
			x.out.write(text)
		}
		step := gs.tc
		if n == 1 && code == ' ' {
			step += gs.tw // word spacing applies to the single-byte space
		}
		if f.vertical {
			adv += f.vAdv*gs.size + step
		} else {
			adv += (w*gs.size + step) * gs.th
		}
	})
	if f.vertical {
		x.tm = x.tm.translate(0, adv)
	} else {
		x.tm = x.tm.translate(adv, 0)
	}
	end := x.trm()
	x.penX, x.penY, x.penEm, x.hasPen = end[4], end[5], math.Hypot(start[2], start[3]), true
	if x.out.full {
		x.stop = true
	}
}

// separate decides what parts this run from the last: a new line when the text
// moved more than half an em across the line, and a space when it skipped ahead
// more than 0.15 em along it, or jumped back more than an em.
//
// The 0.15 is measured, not guessed. In justified text set by TeX and by
// Ghostscript, word gaps shrink to 0.14 em; the noise left inside words by
// kerning and by estimated widths stays under 0.1.
func (x *interp) separate(trm matrix, vertical bool) {
	if !x.hasPen {
		return
	}
	dx, dy := trm[4]-x.penX, trm[5]-x.penY
	la, lc := math.Hypot(trm[0], trm[1]), math.Hypot(trm[2], trm[3])
	if !(la > 0 && lc > 0) || math.IsInf(la, 0) || math.IsInf(lc, 0) {
		return
	}
	along := (dx*trm[0] + dy*trm[1]) / la
	across := (dx*trm[2] + dy*trm[3]) / lc
	if vertical {
		along, across = -across, along
	}
	em := lc
	switch {
	case math.Abs(across) > 0.5*max(em, x.penEm):
		x.out.sep('\n')
	case along > 0.15*em || along < -em:
		x.out.sep(' ')
	}
}

// showArray runs a TJ array: strings to show, and numbers that move the next
// glyph back, in thousandths of an em. A large forward move reads as a space.
func (x *interp) showArray(a array) {
	gs := &x.gs
	f := gs.font
	if f == nil {
		f = defaultFont
	}
	for _, v := range a {
		switch t := v.(type) {
		case string:
			x.show(t)
		case int64, float64:
			n, _ := num(t)
			adj := -n / 1000 * gs.size
			if f.vertical {
				x.tm = x.tm.translate(0, adj)
			} else {
				x.tm = x.tm.translate(adj*gs.th, 0)
			}
		}
		if x.stop {
			return
		}
	}
}

// form runs a form XObject's content in place. Image XObjects are not touched.
func (x *interp) form(res dict, n name, depth int) {
	if depth+1 > maxFormDepth {
		return
	}
	v := x.d.dictOf(res["XObject"])[n]
	r, isRef := v.(ref)
	if isRef && slices.Contains(x.forms, r.num) {
		return // a form that draws itself
	}
	s, ok := x.d.resolve(v).(*stream)
	if !ok {
		return
	}
	if st, _ := x.d.resolve(s.hdr["Subtype"]).(name); st != "Form" {
		return
	}
	// Each run costs at least a kilobyte of the budget, so a form drawn a
	// million times over cannot run without end.
	if x.d.charge(1024) != nil {
		x.stop = true
		return
	}
	data, err := x.d.decode(s)
	if err != nil {
		return
	}
	saved, tm, tlm, floor, extra := x.gs, x.tm, x.tlm, x.floor, x.extra
	if m, ok := x.d.matrix(s.hdr["Matrix"]); ok {
		x.gs.ctm = m.mul(x.gs.ctm)
	}
	fres := x.d.dictOf(s.hdr["Resources"])
	if fres == nil {
		fres = res
	}
	if isRef {
		x.forms = append(x.forms, r.num)
	}
	x.floor, x.extra = len(x.stack), 0
	x.run(data, fres, depth+1)
	x.stack = x.stack[:x.floor]
	if isRef {
		x.forms = x.forms[:len(x.forms)-1]
	}
	x.gs, x.tm, x.tlm, x.floor, x.extra = saved, tm, tlm, floor, extra
}

// skipInlineImage moves past an inline image: its dictionary up to ID, then its
// data up to an EI keyword standing on its own.
func skipInlineImage(l *lexer) {
	length := -1
	for range 4096 {
		tok, err := l.token()
		if err != nil {
			return
		}
		if k, ok := tok.(keyword); ok && k == "ID" {
			break
		}
		if n, ok := tok.(name); ok && (n == "L" || n == "Length") {
			if v, err := l.token(); err == nil {
				if i, ok := v.(int64); ok && i >= 0 && i < int64(len(l.buf)) {
					length = int(i)
				}
			}
		}
	}
	if b, ok := l.next(); ok && !isSpace(b) {
		l.unread() // one white-space byte separates ID from the data
	}
	b := l.buf
	from := l.pos
	if length >= 0 && from+length <= len(b) {
		from += length
	}
	for i := from; i+1 < len(b); i++ {
		if b[i] == 'E' && b[i+1] == 'I' && (i == l.pos || isSpace(b[i-1])) && (i+2 == len(b) || !isRegular(b[i+2])) {
			l.pos = i + 2
			return
		}
	}
	l.pos = len(b)
}

func num(v any) (float64, bool) {
	switch t := v.(type) {
	case int64:
		return float64(t), true
	case float64:
		return t, true
	}
	return 0, false
}

func (d *Doc) number(v any) (float64, bool) { return num(d.resolve(v)) }

func lastNum(args []any) (float64, bool) {
	if len(args) == 0 {
		return 0, false
	}
	return num(args[len(args)-1])
}

func setLast(dst *float64, args []any) {
	if v, ok := lastNum(args); ok {
		*dst = v
	}
}

func matrixArgs(args []any) (matrix, bool) {
	if len(args) < 6 {
		return matrix{}, false
	}
	var m matrix
	for i, a := range args[len(args)-6:] {
		v, ok := num(a)
		if !ok {
			return matrix{}, false
		}
		m[i] = v
	}
	return m, true
}

func (d *Doc) matrix(v any) (matrix, bool) {
	a, ok := d.resolve(v).(array)
	if !ok || len(a) != 6 {
		return matrix{}, false
	}
	var m matrix
	for i := range m {
		f, ok := d.number(a[i])
		if !ok {
			return matrix{}, false
		}
		m[i] = f
	}
	return m, true
}
