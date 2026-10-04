package pdf

import (
	"errors"
	"io"
	"strconv"
)

// The object model. A value is nil (null), bool, int64, float64, string (the raw
// bytes of a PDF string), name, keyword, array, dict, ref or *stream.
type (
	name    string // a /Name, without the slash
	keyword string // a bare word: an operator, obj, R, or a delimiter such as << or [
	array   []any
	dict    map[name]any
)

// ref points at an indirect object.
type ref struct {
	num uint32
	gen uint16
}

// stream is a stream object: its dictionary, and where its data starts in the file.
type stream struct {
	hdr dict
	off int64 // file offset of the first data byte
	ref ref   // the object it is, which keys its decryption
}

var errDepth = errors.New("pdf: objects nested too deeply")

// class sorts bytes: 0 regular, 1 white space, 2 delimiter.
var class = func() (c [256]uint8) {
	for _, b := range []byte{0, '\t', '\n', '\f', '\r', ' '} {
		c[b] = 1
	}
	for _, b := range []byte("()<>[]{}/%") {
		c[b] = 2
	}
	return c
}()

func isSpace(b byte) bool   { return class[b] == 1 }
func isRegular(b byte) bool { return class[b] == 0 }

// lexer splits PDF syntax into tokens and values. It reads a byte slice, or a file
// it pulls in from a starting offset, in reads that start small and double.
type lexer struct {
	buf     []byte
	pos     int
	base    int64       // file offset of buf[0]
	src     io.ReaderAt // nil when buf holds all the input
	end     int64       // reads from src stop here
	pulled  int64       // bytes read from src, capped at maxObjectBytes
	left    *int64      // the Doc's allowance of bytes read from the file
	back    []any       // tokens pushed back; the last is read first
	scratch []byte
	refs    bool // read "num gen R" as a reference; content streams have none
}

// maxObjectBytes caps what one lexer reads from a file. Stream data is read
// separately; this bounds dictionaries, arrays and cross-reference tables.
const maxObjectBytes = 64 << 20

func newLexer(b []byte) *lexer { return &lexer{buf: b} }

// fileLexer reads the file from off. What it reads counts against the Doc's
// allowance: a damaged file can nest each object inside the last one's string,
// and reading every object to the end of the file would then never finish.
func (d *Doc) fileLexer(off int64) *lexer {
	return &lexer{src: d.r, base: off, end: d.size, refs: true, left: &d.lexLeft}
}

// offset is the file offset of the next byte.
func (l *lexer) offset() int64 { return l.base + int64(l.pos) }

// fill pulls more of the file into buf, keeping the byte before pos so unread
// still works. It reports whether any bytes arrived.
func (l *lexer) fill() bool {
	if l.src == nil {
		return false
	}
	at := l.base + int64(len(l.buf))
	if at < 0 || at >= l.end || l.pulled >= maxObjectBytes || l.left != nil && *l.left <= 0 {
		return false
	}
	if keep := l.pos - 1; keep > 0 {
		n := copy(l.buf, l.buf[keep:])
		l.buf = l.buf[:n]
		l.base += int64(keep)
		l.pos -= keep
	}
	// Most objects are small: read 512 bytes first, then double.
	want := min(max(l.pulled, 512), 256<<10, l.end-at)
	if l.left != nil {
		want = min(want, *l.left)
	}
	start := len(l.buf)
	if cap(l.buf)-start < int(want) {
		nb := make([]byte, start, start+int(want))
		copy(nb, l.buf)
		l.buf = nb
	}
	n, _ := l.src.ReadAt(l.buf[start:start+int(want)], at)
	l.buf = l.buf[:start+n]
	l.pulled += int64(n)
	if l.left != nil {
		*l.left -= int64(n)
	}
	return n > 0
}

// next returns the next byte, and false at the end of the input.
func (l *lexer) next() (byte, bool) {
	if l.pos >= len(l.buf) && !l.fill() {
		return 0, false
	}
	b := l.buf[l.pos]
	l.pos++
	return b, true
}

// unread steps back over the byte next just returned.
func (l *lexer) unread() { l.pos-- }

// skipSpace skips white space and comments and returns the first byte after them.
func (l *lexer) skipSpace() (byte, bool) {
	for {
		b, ok := l.next()
		if !ok {
			return 0, false
		}
		if b == '%' {
			for {
				b, ok = l.next()
				if !ok {
					return 0, false
				}
				if b == '\n' || b == '\r' {
					break
				}
			}
			continue
		}
		if !isSpace(b) {
			return b, true
		}
	}
}

// token returns the next token: a keyword (delimiters included), name, string,
// int64, float64 or bool. At the end of the input it returns io.EOF.
func (l *lexer) token() (any, error) {
	if n := len(l.back); n > 0 {
		t := l.back[n-1]
		l.back = l.back[:n-1]
		return t, nil
	}
	b, ok := l.skipSpace()
	if !ok {
		return nil, io.EOF
	}
	switch b {
	case '/':
		return l.name(), nil
	case '(':
		return l.literal(), nil
	case '<':
		if c, ok := l.next(); ok {
			if c == '<' {
				return keyword("<<"), nil
			}
			l.unread()
		}
		return l.hex(), nil
	case '>':
		if c, ok := l.next(); ok {
			if c == '>' {
				return keyword(">>"), nil
			}
			l.unread()
		}
		return keyword(">"), nil
	case '[':
		return keyword("["), nil
	case ']':
		return keyword("]"), nil
	case '{':
		return keyword("{"), nil
	case '}':
		return keyword("}"), nil
	case ')':
		return keyword(")"), nil
	}
	l.unread()
	w := l.word()
	if v, ok := number(w); ok {
		return v, nil
	}
	switch string(w) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return op(w), nil
}

// word reads a run of regular characters into scratch.
func (l *lexer) word() []byte {
	l.scratch = l.scratch[:0]
	for {
		b, ok := l.next()
		if !ok {
			break
		}
		if !isRegular(b) {
			l.unread()
			break
		}
		l.scratch = append(l.scratch, b)
	}
	return l.scratch
}

// op interns the common keywords, which content streams repeat by the million.
func op(w []byte) keyword {
	switch string(w) {
	case "Tj":
		return "Tj"
	case "TJ":
		return "TJ"
	case "Td":
		return "Td"
	case "TD":
		return "TD"
	case "Tf":
		return "Tf"
	case "Tm":
		return "Tm"
	case "BT":
		return "BT"
	case "ET":
		return "ET"
	case "q":
		return "q"
	case "Q":
		return "Q"
	case "cm":
		return "cm"
	case "re":
		return "re"
	case "m":
		return "m"
	case "l":
		return "l"
	case "c":
		return "c"
	case "f":
		return "f"
	case "S":
		return "S"
	case "n":
		return "n"
	case "R":
		return "R"
	case "obj":
		return "obj"
	case "endobj":
		return "endobj"
	case "null":
		return "null"
	}
	return keyword(w)
}

// number parses w as a PDF number: an optional sign, digits, at most one point.
// Anything else, "Inf" and "NaN" included, is not a number.
func number(w []byte) (any, bool) {
	if len(w) == 0 {
		return nil, false
	}
	i, neg := 0, false
	if w[0] == '+' || w[0] == '-' {
		neg, i = w[0] == '-', 1
	}
	digits, point := 0, -1
	for j := i; j < len(w); j++ {
		switch b := w[j]; {
		case b >= '0' && b <= '9':
			digits++
		case b == '.' && point < 0:
			point = j
		default:
			return nil, false
		}
	}
	if digits == 0 {
		return nil, false
	}
	if point < 0 && digits <= 18 {
		var n int64
		for _, b := range w[i:] {
			n = n*10 + int64(b-'0')
		}
		if neg {
			n = -n
		}
		return n, true
	}
	f, err := strconv.ParseFloat(string(w), 64)
	if err != nil {
		return nil, false
	}
	return f, true
}

// name reads a name after its slash, decoding #xx escapes.
func (l *lexer) name() name {
	l.scratch = l.scratch[:0]
	for {
		b, ok := l.next()
		if !ok {
			break
		}
		if !isRegular(b) {
			l.unread()
			break
		}
		if b == '#' {
			h, ok1 := l.next()
			lo, ok2 := l.next()
			if ok1 && ok2 && unhex(h) >= 0 && unhex(lo) >= 0 {
				b = byte(unhex(h)<<4 | unhex(lo))
			} else {
				if ok2 {
					l.unread()
				}
				if ok1 {
					l.unread() // not an escape after all: keep the # as it is
				}
			}
		}
		l.scratch = append(l.scratch, b)
	}
	return name(l.scratch)
}

func unhex(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}

// literal reads a (string) after its opening parenthesis. An unterminated string
// ends at the end of the input.
func (l *lexer) literal() string {
	l.scratch = l.scratch[:0]
	depth := 1
	for {
		b, ok := l.next()
		if !ok {
			return string(l.scratch)
		}
		switch b {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return string(l.scratch)
			}
		case '\r': // an end of line inside a string reads as \n
			if c, ok := l.next(); ok && c != '\n' {
				l.unread()
			}
			b = '\n'
		case '\\':
			if b, ok = l.next(); !ok {
				return string(l.scratch)
			}
			switch b {
			case 'n':
				b = '\n'
			case 'r':
				b = '\r'
			case 't':
				b = '\t'
			case 'b':
				b = '\b'
			case 'f':
				b = '\f'
			case '\r': // a line continuation
				if c, ok := l.next(); ok && c != '\n' {
					l.unread()
				}
				continue
			case '\n':
				continue
			default:
				if b >= '0' && b <= '7' {
					v := int(b - '0')
					for range 2 {
						c, ok := l.next()
						if !ok {
							break
						}
						if c < '0' || c > '7' {
							l.unread()
							break
						}
						v = v<<3 | int(c-'0')
					}
					b = byte(v)
				}
				// Any other escaped byte stands for itself.
			}
		}
		l.scratch = append(l.scratch, b)
	}
}

// hex reads a <hex string> after its opening bracket. Bytes that are not hex
// digits are skipped; an odd final digit is padded with zero.
func (l *lexer) hex() string {
	l.scratch = l.scratch[:0]
	hi := -1
	for {
		b, ok := l.next()
		if !ok || b == '>' {
			break
		}
		v := unhex(b)
		if v < 0 {
			continue
		}
		if hi < 0 {
			hi = v
		} else {
			l.scratch = append(l.scratch, byte(hi<<4|v))
			hi = -1
		}
	}
	if hi >= 0 {
		l.scratch = append(l.scratch, byte(hi<<4))
	}
	return string(l.scratch)
}

// object reads one value.
func (l *lexer) object(depth int) (any, error) {
	tok, err := l.token()
	if err != nil {
		return nil, err
	}
	return l.objectFrom(tok, depth)
}

// objectFrom finishes the value that starts with tok.
func (l *lexer) objectFrom(tok any, depth int) (any, error) {
	switch t := tok.(type) {
	case keyword:
		switch t {
		case "[":
			return l.array(depth + 1)
		case "<<":
			return l.dict(depth + 1)
		case "null":
			return nil, nil
		}
	case int64:
		if l.refs {
			return l.maybeRef(t), nil
		}
	}
	return tok, nil
}

// maybeRef reads "gen R" after num if they are there, and returns the reference;
// otherwise it puts the tokens back and returns num.
func (l *lexer) maybeRef(num int64) any {
	t2, err := l.token()
	if err != nil {
		return num
	}
	gen, ok := t2.(int64)
	if !ok {
		l.back = append(l.back, t2)
		return num
	}
	t3, err := l.token()
	if err != nil {
		l.back = append(l.back, t2)
		return num
	}
	if k, ok := t3.(keyword); ok && k == "R" {
		if num <= 0 || num >= maxObjects || gen < 0 || gen > 0xFFFF {
			return ref{} // object 0 is always free: this reads as null
		}
		return ref{num: uint32(num), gen: uint16(gen)}
	}
	l.back = append(l.back, t3, t2)
	return num
}

// ends reports whether k ends the object being read, so a missing ] or >> does
// not swallow what follows.
func ends(k keyword) bool {
	switch k {
	case "endobj", "stream", "endstream", "obj", "xref", "trailer", "startxref":
		return true
	}
	return false
}

func (l *lexer) array(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errDepth
	}
	var a array
	for {
		tok, err := l.token()
		if err != nil {
			return a, nil // unterminated at the end of the input
		}
		if k, ok := tok.(keyword); ok {
			if k == "]" {
				return a, nil
			}
			if k == ">>" || ends(k) {
				l.back = append(l.back, tok)
				return a, nil
			}
		}
		v, err := l.objectFrom(tok, depth)
		if err != nil {
			return nil, err
		}
		a = append(a, v)
	}
}

func (l *lexer) dict(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errDepth
	}
	d := dict{}
	for {
		tok, err := l.token()
		if err != nil {
			return d, nil
		}
		key, ok := tok.(name)
		if !ok {
			if k, ok := tok.(keyword); ok {
				if k == ">>" {
					return d, nil
				}
				if ends(k) {
					l.back = append(l.back, tok)
					return d, nil
				}
			}
			// A value where a key belongs: read past it whole.
			if _, err := l.objectFrom(tok, depth); err != nil {
				return nil, err
			}
			continue
		}
		v, err := l.object(depth)
		if err == io.EOF {
			return d, nil
		}
		if err != nil {
			return nil, err
		}
		if k, ok := v.(keyword); ok && (k == ">>" || ends(k)) {
			if k != ">>" {
				l.back = append(l.back, k)
			}
			return d, nil // a key without a value
		}
		d[key] = v
	}
}
