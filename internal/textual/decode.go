package textual

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// decode turns bytes in an unknown encoding into UTF-8. A byte-order mark settles
// it. Without one, valid UTF-8 is read as UTF-8 and anything else as Windows-1252,
// the encoding that old documents not in UTF-8 nearly always turn out to use. Its
// printable range is a superset of Latin-1's, so Latin-1 files read right too.
//
// cut says b is the start of a longer file, so a sequence broken off at its end
// is not held against UTF-8.
func decode(b []byte, cut bool) string {
	switch {
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		b = b[3:]
		if cut {
			b = dropPartial(b)
		}
		return strings.ToValidUTF8(string(b), "\uFFFD")
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return fromUTF16(b[2:], false)
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return fromUTF16(b[2:], true)
	}
	if cut {
		b = dropPartial(b)
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return fromWindows1252(b)
}

// dropPartial trims a UTF-8 sequence cut off at the end of b.
func dropPartial(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax+1; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// fromUTF16 decodes UTF-16 in either byte order. An unpaired surrogate becomes
// U+FFFD; an odd byte at the end is dropped.
func fromUTF16(b []byte, bigEndian bool) string {
	unit := func(i int) rune {
		if bigEndian {
			return rune(b[i])<<8 | rune(b[i+1])
		}
		return rune(b[i+1])<<8 | rune(b[i])
	}
	var s strings.Builder
	s.Grow(len(b) / 2)
	for i := 0; i+1 < len(b); i += 2 {
		u := unit(i)
		if utf16.IsSurrogate(u) {
			if i+3 < len(b) {
				if r := utf16.DecodeRune(u, unit(i+2)); r != utf8.RuneError {
					s.WriteRune(r)
					i += 2
					continue
				}
			}
			u = utf8.RuneError
		}
		s.WriteRune(u)
	}
	return s.String()
}

// fromWindows1252 decodes Windows-1252. Bytes 0x80 to 0x9F hold its punctuation
// and a few letters; the five it leaves undefined decode to C1 controls, which
// tidy drops.
func fromWindows1252(b []byte) string {
	var s strings.Builder
	s.Grow(len(b) + len(b)/4)
	for _, c := range b {
		s.WriteRune(cp1252(c))
	}
	return s.String()
}

// cp1252 decodes one Windows-1252 byte.
func cp1252(c byte) rune {
	if c >= 0x80 && c < 0xA0 {
		return cp1252High[c-0x80]
	}
	return rune(c)
}

var cp1252High = [32]rune{
	'\u20AC', '\u0081', '\u201A', '\u0192', '\u201E', '\u2026', '\u2020', '\u2021',
	'\u02C6', '\u2030', '\u0160', '\u2039', '\u0152', '\u008D', '\u017D', '\u008F',
	'\u0090', '\u2018', '\u2019', '\u201C', '\u201D', '\u2022', '\u2013', '\u2014',
	'\u02DC', '\u2122', '\u0161', '\u203A', '\u0153', '\u009D', '\u017E', '\u0178',
}
