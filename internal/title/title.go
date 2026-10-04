// Package title finds a document's own title.
//
// A scrape names its files page_0042.html and doc(3).docx, but the document
// inside usually says what it is. This package reads that title from where each
// format keeps it: a web page's <title>, a Markdown file's front matter or first
// heading, the metadata an Office, OpenDocument or EPUB file carries. A PDF's
// title comes through the PDF hook, because the PDF reader lives in another
// package.
//
// Every file is untrusted. A reader looks at a bounded head of the file, or at
// bounded entries of an archive, and a file it cannot make sense of has no
// title. Nothing here returns an error: a document without a title keeps its
// name.
package title

import (
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	headLimit  = 256 << 10 // bytes of an HTML or Markdown file searched for a title
	entryLimit = 1 << 20   // bytes of one archive entry read, after decompression
	maxRunes   = 200       // the longest title Of returns
)

// PDF, when set, reads a PDF's title. The PDF reader lives in another package; a
// nil PDF means PDFs are not supported.
var PDF func(r io.ReaderAt, size int64) string

// readers finds a title in each supported format, by extension.
var readers = map[string]func(r io.ReaderAt, size int64) string{
	"html": htmlFile, "htm": htmlFile, "xhtml": htmlFile, "shtml": htmlFile,
	"md": markdownFile, "markdown": markdownFile, "mdown": markdownFile,
	"docx": ooxmlFile, "docm": ooxmlFile,
	"xlsx": ooxmlFile, "xlsm": ooxmlFile,
	"pptx": ooxmlFile, "pptm": ooxmlFile,
	"odt": odfFile, "ods": odfFile, "odp": odfFile, "odg": odfFile,
	"epub": epubFile,
}

// Of returns the title the document gives itself, or "" when it has none or the
// format is not supported. The format is chosen by name's extension.
//
// A panic inside a reader costs one title, not the whole run. The readers here
// are fuzzed, but the PDF hook is another package's code, and a scrape can hold
// anything.
func Of(name string, r io.ReaderAt, size int64) (title string) {
	read := reader(name)
	if read == nil || r == nil || size <= 0 {
		return ""
	}
	defer func() {
		if recover() != nil {
			title = ""
		}
	}()
	return clean(read(r, size))
}

// Supported reports whether Of can read name's format.
func Supported(name string) bool { return reader(name) != nil }

func reader(name string) func(io.ReaderAt, int64) string {
	ext := extension(name)
	if ext == "pdf" {
		return PDF
	}
	return readers[ext]
}

// extension returns name's extension in lower case, without the dot. A leading
// dot starts a name, not an extension, as in package organize: ".md" is a hidden
// file with no extension.
func extension(name string) string {
	base := strings.TrimPrefix(filepath.Base(name), ".")
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		return strings.ToLower(base[i+1:])
	}
	return ""
}

// head returns the first headLimit bytes of a file, or all of it when it is
// shorter. A failed read keeps what came before it, since a title sits near the
// top.
func head(r io.ReaderAt, size int64) []byte {
	n := min(size, headLimit)
	b := make([]byte, n)
	got, _ := io.ReadFull(io.NewSectionReader(r, 0, n), b)
	b = b[:got]
	if size > headLimit {
		b = dropPartialRune(b)
	}
	return b
}

// clip cuts b to at most n bytes without leaving half a character at the end.
func clip(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return dropPartialRune(b[:n])
}

// dropPartialRune trims a character the head cut in half, so that a cut does not
// make a UTF-8 file look like a legacy encoding.
func dropPartialRune(b []byte) []byte {
	for k := 1; k <= utf8.UTFMax && k <= len(b); k++ {
		c := b[len(b)-k]
		if c < utf8.RuneSelf {
			return b
		}
		if utf8.RuneStart(c) {
			if !utf8.FullRune(b[len(b)-k:]) {
				return b[:len(b)-k]
			}
			return b
		}
	}
	return b
}

// clean is the last step for every title: normalize it, and drop it when it
// names nothing.
func clean(s string) string {
	s = normalize(s)
	if generic(s) {
		return ""
	}
	return s
}

// normalize trims a title, turns each run of whitespace and control characters
// into one space, drops characters that show nothing, and cuts the result to
// maxRunes, between words when it can.
func normalize(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == utf8.RuneError || invisible(r):
			continue // a byte that is not UTF-8, or a mark that shows nothing
		case unicode.IsSpace(r) || unicode.IsControl(r):
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if utf8.RuneCountInString(out) <= maxRunes {
		return out
	}
	n := 0
	for range maxRunes {
		_, size := utf8.DecodeRuneInString(out[n:])
		n += size
	}
	return shorten(out, n)
}

// invisible reports whether r is a formatting character that shows nothing: a
// soft hyphen, a zero-width space, a byte order mark, a bidirectional control.
// In a file name they are noise at best. At worst a right-to-left override makes
// a name read backwards.
func invisible(r rune) bool {
	switch r {
	case 0x00AD, 0x061C, 0x180E, 0x200B, 0x200E, 0x200F, 0x2060, 0xFEFF:
		return true
	}
	return r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069
}

// placeholders are titles that tools write when nobody named the document. They
// are compared in lower case, after any trailing number: "Document1", "New Page
// 1" and "Untitled-3" are placeholders too.
var placeholders = map[string]bool{
	"untitled": true, "untitled document": true, "document": true, "index": true,
	"home": true, "home page": true, "homepage": true, "page": true,
	"new page": true, "new document": true, "default": true,
	"powerpoint presentation": true, // PowerPoint's own default for every new deck
}

// generic reports whether a normalized title names nothing: a placeholder, or
// text without a single letter ("404", "---", "2024").
func generic(s string) bool {
	if !strings.ContainsFunc(s, unicode.IsLetter) {
		return true
	}
	s = strings.ToLower(s)
	s = strings.TrimRight(strings.TrimRightFunc(s, unicode.IsDigit), " -_.")
	if placeholders[s] {
		return true
	}
	// "Untitled presentation", "Untitled spreadsheet": Google's defaults, and
	// their kin from other tools.
	f := strings.Fields(s)
	return len(f) == 2 && f[0] == "untitled"
}

// shorten cuts s to at most n bytes. It never splits a character, nor parts a
// letter from the accents and joiners after it, and it cuts at the last space
// when that still fills half of n, so a long title loses whole words.
func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := max(n, 0)
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	for i > 0 {
		r, _ := utf8.DecodeRuneInString(s[i:])
		p, size := utf8.DecodeLastRuneInString(s[:i])
		if !extends(r) && p != zwj {
			break
		}
		i -= size
	}
	if s[i] != ' ' { // a cut just before a space already falls between words
		if sp := strings.LastIndexByte(s[:i], ' '); sp > 0 && sp >= n/2 {
			i = sp
		}
	}
	return strings.TrimRight(s[:i], " ")
}

const zwj = 0x200D // zero-width joiner: glues emoji into one picture

// extends reports whether r belongs to the character before it: a combining
// accent, a zero-width joiner, a variation selector, an emoji skin tone, or a tag
// character of a flag.
func extends(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc) || r == zwj ||
		r >= 0xFE00 && r <= 0xFE0F || r >= 0xE0100 && r <= 0xE01EF ||
		r >= 0x1F3FB && r <= 0x1F3FF || r >= 0xE0020 && r <= 0xE007F
}
