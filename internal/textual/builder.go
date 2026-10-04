package textual

import (
	"strings"
	"unicode/utf8"
)

// builder assembles extracted text the way a reader expects it: words joined by
// single spaces, blocks on lines of their own, Markdown marks at the start of
// headings and list items, and nothing written once the limit is reached.
//
// Breaks are asked for, not written: a run of blocks that hold no text leaves one
// break behind, not a stack of blank lines.
type builder struct {
	b     strings.Builder
	n     int // runes written
	limit int

	brk      int    // break due before the next text: 0 none, 1 new line, 2 blank line
	space    bool   // a space is due before the next word
	lineText bool   // the current line holds text
	mark     string // starts the next line that gets text: "## ", "- "
	sep      string // goes between this line's text and the next: " | " between cells
	inline   int    // > 0 in a table cell or a heading, where a break becomes a space
}

func newBuilder(limit int) *builder { return &builder{limit: limit} }

func (o *builder) String() string { return o.b.String() }

// full reports whether the limit is reached; readers stop once it is.
func (o *builder) full() bool { return o.n >= o.limit }

func (o *builder) write(s string) {
	o.b.WriteString(s)
	o.n += utf8.RuneCountInString(s)
}

// block asks for a break before the next text: 1 starts a new line, 2 leaves a
// blank line. A break before any text is dropped, so output never starts blank.
func (o *builder) block(n int) {
	if o.inline > 0 {
		o.space = true
		return
	}
	if o.b.Len() > 0 {
		o.brk = max(o.brk, n)
	}
}

// newline is a forced line break, such as <br> or \line. Unlike block, two in a
// row leave a blank line, as they do on the page.
func (o *builder) newline() {
	if o.inline > 0 {
		o.space = true
		return
	}
	if o.b.Len() > 0 {
		o.brk = min(o.brk+1, 2)
	}
}

// title writes a document's title as a heading on a line of its own.
func (o *builder) title(t string) {
	o.block(2)
	o.mark = "# "
	o.text(t)
	o.mark = ""
	o.block(2)
}

// cell starts a table cell: the next text on this line follows a " | ". Empty
// cells leave no separator behind.
func (o *builder) cell() {
	if o.brk == 0 && o.lineText {
		o.sep = " | "
	}
}

// text writes s with its whitespace collapsed, as HTML and word processors show it.
func (o *builder) text(s string) {
	for s != "" && !o.full() {
		i := strings.IndexFunc(s, notSpace)
		if i < 0 {
			o.space = true
			return
		}
		if i > 0 {
			o.space = true
		}
		s = s[i:]
		j := strings.IndexFunc(s, isSpace)
		if j < 0 {
			j = len(s)
		}
		// Control characters go now, not in tidy: a word made only of them would
		// leave behind the space or break written for it.
		if w := visible(s[:j]); w != "" {
			o.lead()
			o.write(w)
		}
		s = s[j:]
	}
}

// raw writes s as it stands: a tab, or spaces the document spelled out.
func (o *builder) raw(s string) {
	if s == "" || o.full() {
		return
	}
	o.lead()
	o.write(s)
}

// code writes preformatted text as a fenced block, its whitespace intact. The
// fence outruns any run of backticks inside, so the block cannot close early.
func (o *builder) code(s string) {
	if s = visible(s); strings.TrimSpace(s) == "" {
		return
	}
	if o.inline > 0 {
		o.text(s)
		return
	}
	fence := strings.Repeat("`", max(3, longestRun(s, '`')+1))
	o.block(2)
	o.mark = "" // a fence cannot share its line with a bullet
	o.flush()
	o.write(fence + "\n" + s + "\n" + fence)
	o.lineText = true
	o.block(2)
}

// lead writes what is due before the next text: a pending break, then the line's
// mark, or a separator or space when the line already has text.
func (o *builder) lead() {
	o.flush()
	switch {
	case !o.lineText:
		o.write(o.mark)
	case o.sep != "":
		o.write(o.sep)
	case o.space:
		o.write(" ")
	}
	o.mark, o.sep, o.space = "", "", false
	o.lineText = true
}

// flush writes a pending break.
func (o *builder) flush() {
	if o.brk == 0 {
		return
	}
	o.write(strings.Repeat("\n", o.brk))
	o.brk, o.lineText, o.space, o.sep = 0, false, false, ""
}

// isSpace is HTML's collapsible whitespace plus the no-break space, which reads as
// a plain space once there is no layout left to protect, and the Unicode line and
// paragraph separators, which would otherwise turn into stray line breaks.
func isSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\f', '\v', '\u00A0', '\u2028', '\u2029':
		return true
	}
	return false
}

func notSpace(r rune) bool { return !isSpace(r) }

// visible returns s without the control characters tidy would drop.
func visible(s string) string {
	if strings.IndexFunc(s, control) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if control(r) {
			return -1
		}
		return r
	}, s)
}

// longestRun returns the length of the longest run of c in s.
func longestRun(s string, c byte) int {
	best, n := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			n++
			best = max(best, n)
		} else {
			n = 0
		}
	}
	return best
}
