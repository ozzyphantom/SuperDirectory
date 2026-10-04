// Package merge writes many small text documents into a few large ones.
//
// NotebookLM takes 50 sources per notebook on the free plan, and a documentation
// scrape can hold two thousand pages. Packed into numbered Markdown files, each
// page under a heading that names where it came from, the scrape fits in a few
// sources, and an answer that quotes a page still says which page it was.
package merge

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits caps each output file. A zero field means no limit on that measure.
type Limits struct {
	Words int
	Bytes int64
}

// NotebookLM leaves headroom under its 500,000 words and 200 MB per source.
var NotebookLM = Limits{Words: 400_000, Bytes: 150 << 20}

// allows reports whether a file of this many words and bytes is within l.
func (l Limits) allows(words int, size int64) bool {
	return (l.Words == 0 || words <= l.Words) && (l.Bytes == 0 || size <= l.Bytes)
}

// continued marks the heading of every part of a split document but the first.
const continued = " (continued)"

var errClosed = errors.New("merge: writer is closed")

// Writer appends documents to numbered Markdown files in dir: "<base> 001.md",
// "<base> 002.md", ...
//
// Files are created only when a document goes into them, so none is empty, and
// never over an existing file: a name that is taken stops the Writer with an
// error rather than destroy what is there.
type Writer struct {
	dir, base string
	lim       Limits

	f     *os.File
	buf   *bufio.Writer
	words int   // words in the open file
	size  int64 // bytes in the open file

	files  []string
	err    error // the first failure; every later call returns it
	closed bool
}

// NewWriter returns a Writer that fills dir, creating dir if needed. base names
// the files; it cannot hold a path separator.
func NewWriter(dir, base string, lim Limits) (*Writer, error) {
	if base == "" || base != filepath.Base(base) || strings.ContainsAny(base, `/\`) {
		return nil, fmt.Errorf("merge: base %q is not a plain file name", base)
	}
	if lim.Words < 0 || lim.Bytes < 0 {
		return nil, errors.New("merge: negative limit")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Writer{dir: dir, base: base, lim: lim}, nil
}

// Add appends one document under a heading naming where it came from, and returns
// the file it went into. A document that would push the current file past a limit
// starts a new file; one bigger than the limits on its own is split at paragraph
// boundaries (or line/word boundaries when a paragraph alone is too big) across
// as many files as it needs, and Add returns the first.
//
// A single word too big for a file on its own goes into a file of its own, over
// the limit: cutting it would change the text.
func (w *Writer) Add(origin, text string) (file string, err error) {
	if w.closed {
		return "", errClosed
	}
	if w.err != nil {
		return "", w.err
	}
	head := "## " + cleanOrigin(origin)
	text = cleanText(text)
	words := Words(head) + Words(text)
	size := sectionSize(head, text)

	if w.fits(words, size) {
		return w.write(head, text, words)
	}
	if w.size > 0 {
		w.next()
		if w.fits(words, size) {
			return w.write(head, text, words)
		}
	}
	for i, p := range w.split(head, text) {
		h := head
		if i > 0 {
			h += continued
			w.next()
		}
		f, err := w.write(h, p.text, Words(h)+p.words)
		if err != nil {
			return "", err
		}
		if i == 0 {
			file = f
		}
	}
	return file, nil
}

// Close flushes and closes the open file. Calling it again does nothing.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	w.next()
	return w.err
}

// Files returns every file written, in order.
func (w *Writer) Files() []string {
	return append([]string(nil), w.files...)
}

// fits reports whether a section goes into the open file without passing a limit.
// After the first section, each one costs a blank line more.
func (w *Writer) fits(words int, size int64) bool {
	if w.size > 0 {
		size++
	}
	return w.lim.allows(w.words+words, w.size+size)
}

// write appends a section of the given words to the open file, opening the next
// file if none is. After a failure it writes nothing more.
func (w *Writer) write(head, text string, words int) (string, error) {
	if w.err != nil {
		return "", w.err
	}
	if w.f == nil {
		name := filepath.Join(w.dir, fmt.Sprintf("%s %03d.md", w.base, len(w.files)+1))
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			w.err = fmt.Errorf("merge: %w", err)
			return "", w.err
		}
		w.f, w.buf = f, bufio.NewWriterSize(f, 64<<10)
		w.files = append(w.files, name)
		w.words, w.size = 0, 0
	}
	if w.size > 0 {
		w.buf.WriteByte('\n')
		w.size++
	}
	w.buf.WriteString(head)
	if text != "" {
		w.buf.WriteString("\n\n")
		w.buf.WriteString(text)
	}
	if err := w.buf.WriteByte('\n'); err != nil {
		w.err = fmt.Errorf("merge: %w", err)
		return "", w.err
	}
	w.words += words
	w.size += sectionSize(head, text)
	return w.files[len(w.files)-1], nil
}

// next closes the open file, so the next write starts a new one.
func (w *Writer) next() {
	if w.f == nil {
		return
	}
	err := w.buf.Flush()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	if err != nil && w.err == nil {
		w.err = fmt.Errorf("merge: %w", err)
	}
	w.f, w.buf = nil, nil
	w.words, w.size = 0, 0
}

// sectionSize is the bytes a section takes: its heading line, then a blank line
// and the text when there is text.
func sectionSize(head, text string) int64 {
	n := int64(len(head)) + 1
	if text != "" {
		n += int64(len(text)) + 2
	}
	return n
}

// split cuts a document too big for one file into parts that each fit an empty
// file under the continued heading. Paragraphs stay whole when they fit; one that
// does not is cut at line breaks, and a line that does not at spaces. Parts are
// packed greedily, so a cut paragraph's lines share a part with its neighbours.
func (w *Writer) split(head, text string) []part {
	if text == "" {
		return []part{{}} // the heading alone is over the limit
	}
	h := head + continued
	hw := Words(h)
	// room reports whether text of so many words and bytes fits an empty file
	// under the continued heading: heading line, blank line, text, newline.
	room := func(words, size int) bool {
		return w.lim.allows(hw+words, int64(len(h)+3+size))
	}

	var units []span
	for _, p := range spans(text, 0, len(text), 2) {
		if room(p.words, p.end-p.start) {
			units = append(units, p)
			continue
		}
		for _, l := range spans(text, p.start, p.end, 1) {
			if room(l.words, l.end-l.start) {
				units = append(units, l)
				continue
			}
			units = append(units, spans(text, l.start, l.end, 0)...)
		}
	}

	var parts []part
	for i := 0; i < len(units); {
		start, end, words := units[i].start, units[i].end, units[i].words
		j := i + 1
		for ; j < len(units) && room(words+units[j].words, units[j].end-start); j++ {
			end, words = units[j].end, words+units[j].words
		}
		parts = append(parts, part{text[start:end], words})
		i = j
	}
	return parts
}

// part is one piece of a split document, with its word count.
type part struct {
	text  string
	words int
}

// span is a stretch of a document between whitespace, with its word count.
type span struct{ start, end, words int }

// spans returns the stretches of text[start:end] between runs of whitespace that
// hold at least breaks newlines: 2 cuts between paragraphs, 1 between lines, 0
// between words. Words never straddle whitespace, so the stretches' words add up
// to the whole's.
func spans(text string, start, end, breaks int) []span {
	var out []span
	from := start
	for i := start; i < end; {
		r, n := utf8.DecodeRuneInString(text[i:end])
		if !unicode.IsSpace(r) {
			i += n
			continue
		}
		j, newlines := i, 0
		for j < end {
			r, n := utf8.DecodeRuneInString(text[j:end])
			if !unicode.IsSpace(r) {
				break
			}
			if r == '\n' {
				newlines++
			}
			j += n
		}
		if newlines >= breaks {
			if i > from {
				out = append(out, span{from, i, Words(text[from:i])})
			}
			from = j
		}
		i = j
	}
	if end > from {
		out = append(out, span{from, end, Words(text[from:end])})
	}
	return out
}

// cleanOrigin makes origin fit on one heading line.
func cleanOrigin(origin string) string {
	origin = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(origin, "\uFFFD"))
	return strings.TrimSpace(origin)
}

// cleanText gives text \n line endings and trims blank lines and trailing
// whitespace from its ends, so sections sit exactly one blank line apart. The
// first line keeps its indentation.
func cleanText(text string) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimRightFunc(text, unicode.IsSpace)
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 || strings.TrimSpace(text[:i]) != "" {
			return text
		}
		text = text[i+1:]
	}
}

// Words counts words the way the limit does: runs of letters, digits and joiners.
// A joiner keeps a word whole without making one: the apostrophe in "don't", the
// underscore in "snake_case", an accent typed as a combining mark. Chinese and
// Japanese characters count one word each. Those scripts put no spaces between
// words, and counting high only adds files, while counting low could push a
// file past the real limit.
func Words(s string) int {
	n, in := 0, false
	for _, r := range s {
		if r < utf8.RuneSelf {
			switch {
			case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
				in = true
			case r == '\'' || r == '_':
			default:
				if in {
					n++
				}
				in = false
			}
			continue
		}
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana):
			if in {
				n++
			}
			n++
			in = false
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			in = true
		case r == '\u2019' || r == '\u200C' || r == '\u200D' || unicode.In(r, unicode.Pc, unicode.M):
		default:
			if in {
				n++
			}
			in = false
		}
	}
	if in {
		n++
	}
	return n
}
