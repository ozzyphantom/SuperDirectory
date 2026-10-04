package title

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// decode turns a title's bytes into text. UTF-8 is taken as it is. Anything
// else is read as Windows-1252, the encoding browsers assume for an old Western
// page, unless the document declared a charset this package cannot decode: then
// the text is unreadable, and "" says so. Latin-1 text almost never forms valid
// UTF-8, so trying UTF-8 first rarely misreads a legacy page.
func decode(b []byte, charset string) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if l := label(charset); l != "" && !utf8Labels[l] && !latinLabels[l] {
		return ""
	}
	return windows1252(b)
}

// label tidies a charset name as a document writes it: case, spaces, quotes.
func label(charset string) string {
	return strings.ToLower(strings.Trim(charset, " \t\r\n\f\"'"))
}

// utf8Labels are the names a document may give UTF-8.
var utf8Labels = map[string]bool{
	"utf-8": true, "utf8": true, "unicode-1-1-utf-8": true,
	"unicode11utf8": true, "unicode20utf8": true, "x-unicode20utf8": true,
}

// latinLabels are the names that browsers read as Windows-1252, ASCII and
// ISO-8859-1 among them.
var latinLabels = map[string]bool{
	"ansi_x3.4-1968": true, "ascii": true, "cp1252": true, "cp819": true,
	"csisolatin1": true, "ibm819": true, "iso-8859-1": true, "iso-ir-100": true,
	"iso8859-1": true, "iso88591": true, "iso_8859-1": true, "iso_8859-1:1987": true,
	"l1": true, "latin1": true, "us-ascii": true, "windows-1252": true, "x-cp1252": true,
}

// windows1252 decodes b, which is Windows-1252, into UTF-8.
func windows1252(b []byte) string {
	var s strings.Builder
	s.Grow(len(b) + len(b)/2)
	for _, c := range b {
		if c >= 0x80 && c < 0xA0 {
			s.WriteRune(cp1252[c-0x80])
		} else {
			s.WriteRune(rune(c))
		}
	}
	return s.String()
}

// cp1252 holds what Windows-1252 puts at 0x80 through 0x9F, where Latin-1 has
// control codes: curly quotes, dashes, the euro sign. The five bytes it leaves
// unassigned keep their control codes, as they do in a browser.
var cp1252 = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

// xmlCharset lets an XML decoder read the encodings older tools declare besides
// UTF-8: Latin-1, Windows-1252, ASCII, and UTF-8 spelled "UTF8". The input is
// one archive entry, already capped at entryLimit, so it is read whole.
func xmlCharset(charset string, in io.Reader) (io.Reader, error) {
	switch l := label(charset); {
	case utf8Labels[l]:
		return in, nil
	case latinLabels[l]:
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		return strings.NewReader(windows1252(b)), nil
	}
	return nil, fmt.Errorf("unsupported charset %q", charset)
}
