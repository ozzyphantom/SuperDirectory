// Package textual turns documents into plain text.
//
// It is the reading half of merging: a scrape's pages, manuals and notes become
// text that package merge packs into a few large Markdown files. Each reader
// keeps what a person reading the text needs: headings, list items, table rows
// and line breaks. It drops what only a renderer needs: markup, styles,
// scripts, fonts and pictures.
//
// Every input is untrusted. A damaged file yields the text read before the
// damage, and reads are capped, so a hostile file costs bounded time and memory.
package textual

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ErrUnsupported means Text has no reader for the file's format.
var ErrUnsupported = errors.New("textual: unsupported format")

// PDF, when set, extracts a PDF's text. The PDF reader lives in another package;
// a nil PDF means PDFs are unsupported.
var PDF func(r io.ReaderAt, size int64, limit int) (string, error)

// defaultLimit is the rune cap when the caller sets none: about ten novels, more
// than one NotebookLM source holds, and little enough that one document cannot
// exhaust memory.
const defaultLimit = 5_000_000

// maxEntry caps the bytes read from one HTML file or one entry of a DOCX, ODT or
// EPUB. A real page or chapter is a small fraction of it; a zip bomb stops at it.
const maxEntry = 64 << 20

// reader extracts the text of one format. It returns what it read before any
// error, so a damaged file still gives up its readable part.
type reader func(r io.ReaderAt, size int64, limit int) (string, error)

// readers maps a lowercased extension to its reader. PDF is absent: it goes
// through the hook.
var readers = map[string]reader{
	"md": plain, "markdown": plain, "mdown": plain,

	"html": htmlFile, "htm": htmlFile, "xhtml": htmlFile, "shtml": htmlFile,

	"rtf":  rtf,
	"docx": docx, "docm": docx,
	"odt":  odt,
	"epub": epub,
}

// plainExts are the formats read as they stand. Beyond prose they take the text
// formats a manual or a scrape carries that read well without rendering: markup
// sources, subtitles, configuration and code samples. JSON, XML, JavaScript and
// CSS are left out on purpose. A scrape carries them by the hundred as site
// assets and search indexes, and merged they would bury the prose.
var plainExts = []string{
	"txt", "text", "log", "csv", "tsv", "nfo",
	"rst", "adoc", "asciidoc", "org", "tex",
	"srt", "vtt",
	"ini", "cfg", "conf", "yaml", "yml", "toml",
	"c", "h", "cc", "cpp", "hpp", "cs", "go", "java", "kt", "py", "rb", "rs",
	"swift", "sh", "bash", "zsh", "ps1", "bat", "sql", "pl", "lua",
}

func init() {
	for _, e := range plainExts {
		readers[e] = plain
	}
}

// Text returns the document's text, read by name's extension. It stops after
// limit runes; limit <= 0 means 5,000,000.
//
// The text uses \n line endings and holds no control characters but tab and
// newline. Damaged input yields the text read before the damage when there is
// some, and an error when there is none. ErrUnsupported means no reader handles
// the format.
func Text(name string, r io.ReaderAt, size int64, limit int) (text string, err error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	read := readerFor(name)
	if read == nil {
		return "", ErrUnsupported
	}
	// The readers are fuzzed, but they run on files nobody vetted. A panic in one
	// should cost that document, not the whole copy.
	defer func() {
		if p := recover(); p != nil {
			text, err = "", fmt.Errorf("textual: reading %s: %v", filepath.Base(name), p)
		}
	}()
	s, err := read(r, size, limit)
	if s = tidy(s, limit); s != "" {
		return s, nil
	}
	return "", err
}

// Supported reports whether Text can read name's format.
func Supported(name string) bool {
	return readerFor(name) != nil
}

func readerFor(name string) reader {
	ext := extension(name)
	if ext == "pdf" {
		if PDF == nil {
			return nil
		}
		return PDF
	}
	return readers[ext]
}

// extension returns name's lowercased extension without its dot. A leading dot
// starts a name, not an extension: ".txt" has none.
func extension(name string) string {
	base := strings.TrimPrefix(filepath.Base(name), ".")
	i := strings.LastIndexByte(base, '.')
	if i < 0 {
		return ""
	}
	return strings.ToLower(base[i+1:])
}

// plain reads text as it stands; only its encoding and line endings change. A
// rune never takes more than four bytes, even as UTF-16 or as \r\n, so a long
// file is read only as far as limit runes can reach.
func plain(r io.ReaderAt, size int64, limit int) (string, error) {
	n := size
	if int64(limit) < size/4 {
		n = int64(limit)*4 + 4
	}
	b, err := readAt(r, n)
	return decode(b, n < size), err
}

// readAt reads the first n bytes of r. A file shorter than its stated size
// yields what it has.
func readAt(r io.ReaderAt, n int64) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	b := make([]byte, n)
	m, err := r.ReadAt(b, 0)
	if err == io.EOF {
		err = nil
	}
	return b[:m], err
}

// tidy makes extracted text safe to hand on: valid UTF-8, \n line endings, no
// control characters but tab and newline, no trailing whitespace, and at most
// limit runes. Form feeds and Unicode line separators become newlines: they
// break lines in the source, and some tools choke on them.
func tidy(s string, limit int) string {
	var b strings.Builder
	b.Grow(min(len(s), limit))
	n := 0
	for i := 0; i < len(s) && n < limit; {
		r, w := utf8.DecodeRuneInString(s[i:])
		i += w
		switch {
		case r == '\r':
			if i < len(s) && s[i] == '\n' {
				continue // the \n that follows is the line break
			}
			r = '\n'
		case r == '\f' || r == '\v' || r == '\u2028' || r == '\u2029':
			r = '\n'
		case control(r):
			continue
		}
		b.WriteRune(r) // an invalid byte decoded as RuneError, written as U+FFFD
		n++
	}
	return strings.TrimRight(b.String(), " \t\n")
}

// control reports whether tidy drops r: a C0 or C1 control character other than
// a tab or a line break, or a byte-order mark inside the text.
func control(r rune) bool {
	switch r {
	case '\t', '\n', '\r', '\f', '\v':
		return false
	}
	return r < 0x20 || r >= 0x7F && r < 0xA0 || r == '\uFEFF'
}
