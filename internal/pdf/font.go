package pdf

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"unicode/utf16"
)

// font turns the bytes of a shown string into character codes, and codes into
// text and advance widths.
type font struct {
	composite bool         // Type0: codes of one to four bytes
	vertical  bool         // writing mode 1: glyphs advance down the page
	enc       [256]string  // simple fonts: the text of each code
	toUni     *cmap        // ToUnicode, which beats every other mapping
	space     []codeRange  // composite fonts: how bytes split into codes
	twoByte   bool         // composite fonts with no ranges: two bytes a code
	utf16     bool         // the codes are UTF-16 (the Uni…-UCS2 and -UTF16 CMaps)
	cids      *cmap        // an embedded encoding CMap's codes-to-CIDs mapping
	widths    []float64    // simple fonts: widths from FirstChar on, in ems
	first     int          // simple fonts: the code widths[0] belongs to
	missing   float64      // the width of codes outside widths
	std       *[95]uint16  // standard-font metrics, when the file lists no widths
	cidW      []widthRange // composite fonts: widths by CID
	dw        float64      // composite fonts: the default width
	vAdv      float64      // vertical fonts: the advance down the page, in ems
	scale     float64      // glyph space to text space: 1/1000, or a Type3 font's matrix
}

type widthRange struct {
	lo, hi uint32
	w      []float64 // one per CID, or a single width for the whole range
}

// defaultFont stands in when a content stream shows text before choosing a font,
// or names a font its resources lack.
var defaultFont = func() *font {
	f := &font{scale: 0.001, std: &helveticaWidths, missing: 0.5}
	f.setEncoding(winAnsiEncoding)
	return f
}()

func (f *font) setEncoding(e *encoding) {
	for i, r := range e {
		if r != 0 {
			f.enc[i] = string(r)
		} else {
			f.enc[i] = ""
		}
	}
}

// maxFonts caps the fonts one Doc loads; past it, text shows in the default font.
const maxFonts = 4096

// fontFor returns the font named n in the resources, loading it once per Doc.
func (d *Doc) fontFor(res dict, n name) *font {
	fonts := d.dictOf(res["Font"])
	v, ok := fonts[n]
	if !ok {
		return defaultFont
	}
	if r, ok := v.(ref); ok {
		if f, ok := d.fonts[r.num]; ok {
			return f
		}
		if len(d.fonts)+len(d.inlineFn) >= maxFonts {
			return defaultFont
		}
		f := d.loadFont(d.dictOf(r))
		d.fonts[r.num] = f
		return f
	}
	fd := d.dictOf(v)
	if fd == nil {
		return defaultFont
	}
	// A direct dictionary is cached by identity. The entry holds the dictionary,
	// so its address cannot be freed and handed to another while cached.
	key := reflect.ValueOf(fd).Pointer()
	if e, ok := d.inlineFn[key]; ok {
		return e.f
	}
	if len(d.fonts)+len(d.inlineFn) >= maxFonts {
		return defaultFont
	}
	f := d.loadFont(fd)
	d.inlineFn[key] = inlineFont{fd, f}
	return f
}

type inlineFont struct {
	fd dict
	f  *font
}

func (d *Doc) loadFont(fd dict) *font {
	if fd == nil {
		return defaultFont
	}
	f := &font{scale: 0.001}
	subtype, _ := d.resolve(fd["Subtype"]).(name)
	if subtype == "Type0" {
		d.loadComposite(f, fd)
	} else {
		d.loadSimple(f, fd, subtype)
	}
	if s, ok := d.resolve(fd["ToUnicode"]).(*stream); ok {
		f.toUni = d.cmapOf(s)
	}
	if f.composite && f.space == nil && !f.twoByte {
		if f.toUni != nil && len(f.toUni.space) > 0 {
			f.space = f.toUni.space
		} else {
			f.twoByte = true
		}
	}
	return f
}

func (d *Doc) loadSimple(f *font, fd dict, subtype name) {
	desc := d.dictOf(fd["FontDescriptor"])
	base := standardEncoding
	if subtype == "TrueType" {
		base = winAnsiEncoding // what viewers assume for a TrueType font without one
	}
	named := false
	var diffs array
	switch e := d.resolve(fd["Encoding"]).(type) {
	case name:
		if b := namedEncoding(e); b != nil {
			base, named = b, true
		}
	case dict:
		if n, ok := d.resolve(e["BaseEncoding"]).(name); ok {
			if b := namedEncoding(n); b != nil {
				base, named = b, true
			}
		}
		diffs, _ = d.resolve(e["Differences"]).(array)
	}
	// An embedded Type 1 font brings its own encoding: the base for /Differences
	// unless the font dictionary names another.
	var builtin map[byte]string
	if !named && (subtype == "Type1" || subtype == "MMType1") {
		builtin = d.builtinEncoding(desc)
	}
	if builtin != nil {
		for c, n := range builtin {
			f.enc[c] = glyphText(n)
		}
	} else {
		f.setEncoding(base)
	}
	// 256 codes and their names; a longer array is padding or an attack.
	code := -1
	for _, v := range diffs[:min(len(diffs), 1024)] {
		switch t := d.resolve(v).(type) {
		case int64:
			code = int(t)
		case name:
			if code >= 0 && code < 256 {
				g := glyphText(string(t))
				if g == "" && subtype == "Type3" {
					g = numericGlyph(string(t))
				}
				f.enc[code] = g
			}
			code++
		}
	}

	if subtype == "Type3" {
		if m, ok := d.matrix(fd["FontMatrix"]); ok && m[0] > 0 {
			f.scale = m[0]
		}
	}
	if mw, ok := d.number(desc["MissingWidth"]); ok {
		f.missing = mw * f.scale
	}
	if w, ok := d.resolve(fd["Widths"]).(array); ok && len(w) > 0 {
		first, _ := d.intOf(fd["FirstChar"])
		if first < 0 || first > 255 {
			first = 0
		}
		f.first = int(first)
		for _, x := range w[:min(len(w), 256)] {
			v, _ := d.number(x)
			f.widths = append(f.widths, v*f.scale)
		}
		return
	}
	// No widths: a standard font, measured from its known metrics.
	bf, _ := d.resolve(fd["BaseFont"]).(name)
	f.std = standardWidths(string(bf))
	if f.missing == 0 {
		f.missing = 0.5
	}
}

// builtinEncoding reads the encoding an embedded Type 1 font program declares,
// from its clear-text part: "dup 12 /fi put" and the like. TeX's Computer Modern
// fonts depend on it, since in them code 12 is the fi ligature. It returns nil
// when there is no such program, or it uses StandardEncoding.
func (d *Doc) builtinEncoding(desc dict) map[byte]string {
	s, ok := d.resolve(desc["FontFile"]).(*stream)
	if !ok {
		return nil
	}
	data, err := d.decode(s)
	if err != nil {
		return nil
	}
	if n, ok := d.intOf(s.hdr["Length1"]); ok && n > 0 && n < int64(len(data)) {
		data = data[:n] // the clear text; the rest is encrypted
	}
	i := bytes.Index(data, []byte("/Encoding"))
	if i < 0 {
		return nil
	}
	l := newLexer(data[i+len("/Encoding"):])
	enc := map[byte]string{}
	var last [3]any // the three tokens before the current one
	for range 4 * 256 * 4 {
		tok, err := l.token()
		if err != nil {
			break
		}
		if k, ok := tok.(keyword); ok {
			switch k {
			case "StandardEncoding":
				if len(enc) == 0 {
					return nil
				}
			case "put":
				dup, _ := last[0].(keyword)
				code, ok1 := last[1].(int64)
				n, ok2 := last[2].(name)
				if dup == "dup" && ok1 && ok2 && code >= 0 && code < 256 {
					enc[byte(code)] = string(n)
				}
			case "def", "eexec":
				if len(enc) > 0 || k == "eexec" {
					return enc
				}
			}
		}
		last = [3]any{last[1], last[2], tok}
	}
	return enc
}

func namedEncoding(n name) *encoding {
	switch n {
	case "WinAnsiEncoding":
		return winAnsiEncoding
	case "MacRomanEncoding":
		return macRomanEncoding
	case "StandardEncoding":
		return standardEncoding
	}
	return nil
}

func (d *Doc) loadComposite(f *font, fd dict) {
	f.composite, f.dw, f.vAdv = true, 1, -1
	switch e := d.resolve(fd["Encoding"]).(type) {
	case name:
		s := string(e)
		f.vertical = strings.HasSuffix(s, "-V")
		switch {
		case s == "Identity-H" || s == "Identity-V":
			f.twoByte = true
		case strings.HasPrefix(s, "Uni") && (strings.Contains(s, "UCS2") || strings.Contains(s, "UTF16")):
			f.twoByte, f.utf16 = true, true
		}
	case *stream:
		if c := d.cmapOf(e); c != nil {
			f.space, f.cids, f.vertical = c.space, c, c.wmode == 1
		}
		if wm, ok := d.intOf(e.hdr["WMode"]); ok {
			f.vertical = wm == 1
		}
	}
	descs, _ := d.resolve(fd["DescendantFonts"]).(array)
	if len(descs) == 0 {
		return
	}
	desc := d.dictOf(descs[0])
	if dw, ok := d.number(desc["DW"]); ok {
		f.dw = dw / 1000
	}
	if dw2, ok := d.resolve(desc["DW2"]).(array); ok && len(dw2) == 2 {
		if v, ok := d.number(dw2[1]); ok {
			f.vAdv = v / 1000
		}
	}
	// Widths count against the Doc's table allowance, so many fonts sharing one
	// huge /W array cannot each copy it.
	w, _ := d.resolve(desc["W"]).(array)
	limit := min(maxCMap, d.tableLeft)
	entries := 0
	for i := 0; i+1 < len(w) && entries < limit; {
		lo, ok := d.intOf(w[i])
		if !ok || lo < 0 || lo >= 1<<32 {
			break
		}
		if list, ok := d.resolve(w[i+1]).(array); ok { // c [w1 w2 ...]
			list = list[:min(len(list), limit-entries)]
			if len(list) > 0 && lo+int64(len(list)) <= 1<<32 {
				r := widthRange{lo: uint32(lo), hi: uint32(lo + int64(len(list)) - 1)}
				for _, x := range list {
					v, _ := d.number(x)
					r.w = append(r.w, v/1000)
				}
				f.cidW = append(f.cidW, r)
			}
			entries += max(len(list), 1)
			i += 2
			continue
		}
		hi, ok1 := d.intOf(w[i+1]) // c1 c2 w
		if i+2 >= len(w) || !ok1 || hi < lo || hi >= 1<<32 {
			break
		}
		v, _ := d.number(w[i+2])
		f.cidW = append(f.cidW, widthRange{lo: uint32(lo), hi: uint32(hi), w: []float64{v / 1000}})
		entries++
		i += 3
	}
	d.tableLeft -= entries
	sort.SliceStable(f.cidW, func(i, j int) bool { return f.cidW[i].lo < f.cidW[j].lo })
}

// cmapOf parses a CMap stream once per Doc.
func (d *Doc) cmapOf(s *stream) *cmap {
	if c, ok := d.cmaps[s.ref.num]; ok && s.ref.num != 0 {
		return c
	}
	var c *cmap
	if data, err := d.decode(s); err == nil {
		var n int
		c, n = parseCMap(data, min(maxCMap, d.tableLeft))
		d.tableLeft -= n
	}
	if s.ref.num != 0 {
		d.cmaps[s.ref.num] = c
	}
	return c
}

// each splits s into codes and calls fn with each code's byte length, its
// text, and its advance width in ems.
func (f *font) each(s string, fn func(code uint32, n int, text string, w float64)) {
	if !f.composite {
		for i := 0; i < len(s); i++ {
			c := s[i]
			fn(uint32(c), 1, f.text(uint32(c), 1), f.width1(c))
		}
		return
	}
	for i := 0; i < len(s); {
		code, n := f.split(s[i:])
		i += n
		fn(code, n, f.text(code, n), f.widthCID(f.cid(code, n)))
	}
}

// split takes the next code from s, by the font's codespace ranges.
func (f *font) split(s string) (uint32, int) {
	if f.twoByte || len(f.space) == 0 {
		if len(s) < 2 {
			return uint32(s[0]), 1
		}
		return uint32(s[0])<<8 | uint32(s[1]), 2
	}
	for n := 1; n <= 4 && n <= len(s); n++ {
		var code uint32
		for i := range n {
			code = code<<8 | uint32(s[i])
		}
		for _, r := range f.space {
			if r.n == n && r.contains(code) {
				return code, n
			}
		}
	}
	// No range matches: take as many bytes as the shortest range, so the codes
	// that follow stay aligned.
	n := 4
	for _, r := range f.space {
		n = min(n, r.n)
	}
	n = min(n, len(s))
	var code uint32
	for i := range n {
		code = code<<8 | uint32(s[i])
	}
	return code, n
}

// text returns the Unicode text of a code.
func (f *font) text(code uint32, n int) string {
	if f.toUni != nil {
		if t, ok := f.toUni.lookup(code, n); ok {
			return t
		}
		if n == 1 { // some writers key a simple font's map with two-byte codes
			if t, ok := f.toUni.lookup(code, 2); ok {
				return t
			}
		}
	}
	if !f.composite {
		return f.enc[code&0xFF]
	}
	if f.utf16 && n == 2 && (code < 0xD800 || code > 0xDFFF) {
		return string(rune(code))
	}
	return ""
}

func (f *font) width1(c byte) float64 {
	if i := int(c) - f.first; f.widths != nil {
		if i >= 0 && i < len(f.widths) {
			return f.widths[i]
		}
		return f.missing
	}
	if f.std != nil {
		if t := f.enc[c]; len(t) == 1 && t[0] >= 32 && t[0] < 127 {
			return float64(f.std[t[0]-32]) / 1000
		}
	}
	return f.missing
}

// cid maps a code to a CID: through an embedded encoding CMap, or as itself.
func (f *font) cid(code uint32, n int) uint32 {
	if f.cids != nil {
		if c, ok := f.cids.cid(code, n); ok {
			return c
		}
	}
	return code
}

func (f *font) widthCID(cid uint32) float64 {
	i := sort.Search(len(f.cidW), func(i int) bool { return f.cidW[i].lo > cid }) - 1
	if i >= 0 {
		r := f.cidW[i]
		if cid <= r.hi {
			if len(r.w) == 1 {
				return r.w[0]
			}
			if k := int(cid - r.lo); k < len(r.w) {
				return r.w[k]
			}
		}
	}
	return f.dw
}

// cmap is a parsed CMap: a ToUnicode map, or an encoding CMap embedded in a file.
type cmap struct {
	space  []codeRange
	chars  map[uint64]string // bfchar, keyed by byte length << 32 | code
	ranges []bfRange         // bfrange, sorted by length then low code
	cidMap []cidRange        // cidchar and cidrange
	wmode  int
}

// codeRange is a codespace range: each byte of a code lies between the
// corresponding bytes of lo and hi.
type codeRange struct {
	lo, hi uint32
	n      int
}

func (r codeRange) contains(code uint32) bool {
	for i := range r.n {
		sh := uint(8 * i)
		b, lo, hi := code>>sh&0xFF, r.lo>>sh&0xFF, r.hi>>sh&0xFF
		if b < lo || b > hi {
			return false
		}
	}
	return true
}

type bfRange struct {
	lo, hi uint32
	n      int
	dst    []rune   // the first code's text; its last rune counts up through the range
	arr    []string // or the text of each code
}

type cidRange struct {
	lo, hi uint32
	n      int
	cid    uint32
}

// parseCMap reads the parts of a CMap that matter here, up to limit entries, and
// returns how many it kept. A range counts once, however many codes it covers.
func parseCMap(data []byte, limit int) (*cmap, int) {
	c := &cmap{chars: map[uint64]string{}}
	l := newLexer(data)
	entries := 0
	var prev any
	for entries < limit {
		tok, err := l.token()
		if err != nil {
			break
		}
		if k, ok := tok.(keyword); ok {
			switch k {
			case "begincodespacerange":
				entries += c.readSpace(l, limit-entries)
			case "beginbfchar":
				entries += c.readBFChar(l, limit-entries)
			case "beginbfrange":
				entries += c.readBFRange(l, limit-entries)
			case "begincidchar", "begincidrange":
				entries += c.readCID(l, k == "begincidrange", limit-entries)
			}
		}
		if n, ok := prev.(name); ok && n == "WMode" {
			if v, ok := tok.(int64); ok {
				c.wmode = int(v)
			}
		}
		prev = tok
	}
	sort.SliceStable(c.ranges, func(i, j int) bool {
		a, b := c.ranges[i], c.ranges[j]
		return a.n < b.n || a.n == b.n && a.lo < b.lo
	})
	sort.SliceStable(c.cidMap, func(i, j int) bool {
		a, b := c.cidMap[i], c.cidMap[j]
		return a.n < b.n || a.n == b.n && a.lo < b.lo
	})
	return c, entries
}

// maxCodespace caps codespace ranges, which every code is checked against. Real
// CMaps declare a handful.
const maxCodespace = 64

// codeOf reads a source code: a string of one to four bytes.
func codeOf(v any) (uint32, int, bool) {
	s, ok := v.(string)
	if !ok || len(s) == 0 || len(s) > 4 {
		return 0, 0, false
	}
	var code uint32
	for i := 0; i < len(s); i++ {
		code = code<<8 | uint32(s[i])
	}
	return code, len(s), true
}

// operands reads the tokens of a section, up to the keyword that ends it or
// limit tokens, whichever comes first.
func operands(l *lexer, limit int) []any {
	var out []any
	for len(out) < limit {
		tok, err := l.token()
		if err != nil {
			break
		}
		if k, ok := tok.(keyword); ok && strings.HasPrefix(string(k), "end") {
			break
		}
		if k, ok := tok.(keyword); ok && k == "[" {
			v, err := l.array(1)
			if err != nil {
				break
			}
			tok = v
		}
		out = append(out, tok)
	}
	return out
}

func (c *cmap) readSpace(l *lexer, room int) int {
	ops := operands(l, 2*room)
	for i := 0; i+1 < len(ops) && len(c.space) < maxCodespace; i += 2 {
		lo, n1, ok1 := codeOf(ops[i])
		hi, n2, ok2 := codeOf(ops[i+1])
		if ok1 && ok2 && n1 == n2 {
			c.space = append(c.space, codeRange{lo, hi, n1})
		}
	}
	return len(ops) / 2
}

func (c *cmap) readBFChar(l *lexer, room int) int {
	ops := operands(l, 2*room)
	for i := 0; i+1 < len(ops); i += 2 {
		code, n, ok := codeOf(ops[i])
		if !ok {
			continue
		}
		switch t := ops[i+1].(type) {
		case string:
			c.chars[uint64(n)<<32|uint64(code)] = utf16Text(t)
		case name: // a glyph name instead of text
			c.chars[uint64(n)<<32|uint64(code)] = glyphText(string(t))
		}
	}
	return len(ops) / 2
}

func (c *cmap) readBFRange(l *lexer, room int) int {
	ops := operands(l, 3*room)
	entries := 0
	for i := 0; i+2 < len(ops) && entries < room; i += 3 {
		lo, n1, ok1 := codeOf(ops[i])
		hi, n2, ok2 := codeOf(ops[i+1])
		entries++
		if !ok1 || !ok2 || n1 != n2 || hi < lo {
			continue
		}
		r := bfRange{lo: lo, hi: hi, n: n1}
		switch t := ops[i+2].(type) {
		case string:
			r.dst = []rune(utf16Text(t))
			if len(r.dst) == 0 {
				continue
			}
		case array:
			for _, x := range t[:min(len(t), int(hi-lo)+1, max(room-entries, 0))] {
				s, _ := x.(string)
				r.arr = append(r.arr, utf16Text(s))
			}
			entries += len(r.arr)
			if len(r.arr) == 0 {
				continue
			}
		default:
			continue
		}
		c.ranges = append(c.ranges, r)
	}
	return entries
}

func (c *cmap) readCID(l *lexer, isRange bool, room int) int {
	per := 2
	if isRange {
		per = 3
	}
	ops := operands(l, per*room)
	for i := 0; i+per-1 < len(ops); i += per {
		lo, n1, ok1 := codeOf(ops[i])
		hi, n2 := lo, n1
		ok2 := true
		if isRange {
			hi, n2, ok2 = codeOf(ops[i+1])
		}
		cid, ok3 := ops[i+per-1].(int64)
		if ok1 && ok2 && ok3 && n1 == n2 && hi >= lo && cid >= 0 && cid < 1<<32 {
			c.cidMap = append(c.cidMap, cidRange{lo, hi, n1, uint32(cid)})
		}
	}
	return len(ops) / per
}

// utf16Text decodes a ToUnicode destination: UTF-16BE, or a lone byte. It keeps
// 32 code units at most, more than any ligature needs: a range's destination is
// copied for every code looked up in it.
func utf16Text(s string) string {
	if len(s) == 1 {
		return string(rune(s[0]))
	}
	s = s[:min(len(s), 64)]
	u := make([]uint16, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		u = append(u, uint16(s[i])<<8|uint16(s[i+1]))
	}
	return string(utf16.Decode(u))
}

// lookup returns the text of a code. Overlapping ranges are not merged: the one
// with the highest start at or below the code is the one consulted.
func (c *cmap) lookup(code uint32, n int) (string, bool) {
	if c == nil {
		return "", false
	}
	if t, ok := c.chars[uint64(n)<<32|uint64(code)]; ok {
		return t, true
	}
	i := sort.Search(len(c.ranges), func(i int) bool {
		r := c.ranges[i]
		return r.n > n || r.n == n && r.lo > code
	}) - 1
	if i < 0 {
		return "", false
	}
	r := c.ranges[i]
	if r.n != n || code > r.hi {
		return "", false
	}
	k := code - r.lo
	if len(r.dst) > 0 {
		out := append([]rune(nil), r.dst...)
		out[len(out)-1] += rune(k)
		return string(out), true
	}
	if int(k) < len(r.arr) {
		return r.arr[k], true
	}
	return "", false
}

func (c *cmap) cid(code uint32, n int) (uint32, bool) {
	i := sort.Search(len(c.cidMap), func(i int) bool {
		r := c.cidMap[i]
		return r.n > n || r.n == n && r.lo > code
	}) - 1
	if i < 0 {
		return 0, false
	}
	if r := c.cidMap[i]; r.n == n && code <= r.hi {
		return r.cid + (code - r.lo), true
	}
	return 0, false
}
