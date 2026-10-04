package pdf

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// encoding maps single-byte codes to Unicode. Zero marks a code with no character.
type encoding [256]rune

// latin1 builds an encoding that is ASCII below 128, high above it, then patched.
func latin1(high *[128]rune, patch map[byte]rune) *encoding {
	var e encoding
	for i := 32; i < 127; i++ {
		e[i] = rune(i)
	}
	if high != nil {
		copy(e[128:], high[:])
	}
	for c, r := range patch {
		e[c] = r
	}
	return &e
}

// identityHigh is ISO Latin-1 above 128, with the C1 controls left out.
var identityHigh = func() (h [128]rune) {
	for i := 32; i < 128; i++ {
		h[i] = rune(128 + i)
	}
	return h
}()

var winAnsiEncoding = latin1(&identityHigh, map[byte]rune{
	0x80: 0x20AC, 0x82: 0x201A, 0x83: 0x0192, 0x84: 0x201E, 0x85: 0x2026, 0x86: 0x2020,
	0x87: 0x2021, 0x88: 0x02C6, 0x89: 0x2030, 0x8A: 0x0160, 0x8B: 0x2039, 0x8C: 0x0152,
	0x8E: 0x017D, 0x91: 0x2018, 0x92: 0x2019, 0x93: 0x201C, 0x94: 0x201D, 0x95: 0x2022,
	0x96: 0x2013, 0x97: 0x2014, 0x98: 0x02DC, 0x99: 0x2122, 0x9A: 0x0161, 0x9B: 0x203A,
	0x9C: 0x0153, 0x9E: 0x017E, 0x9F: 0x0178,
})

var standardEncoding = latin1(nil, map[byte]rune{
	0x27: 0x2019, 0x60: 0x2018,
	0xA1: 0x00A1, 0xA2: 0x00A2, 0xA3: 0x00A3, 0xA4: 0x2044, 0xA5: 0x00A5, 0xA6: 0x0192,
	0xA7: 0x00A7, 0xA8: 0x00A4, 0xA9: 0x0027, 0xAA: 0x201C, 0xAB: 0x00AB, 0xAC: 0x2039,
	0xAD: 0x203A, 0xAE: 0xFB01, 0xAF: 0xFB02, 0xB1: 0x2013, 0xB2: 0x2020, 0xB3: 0x2021,
	0xB4: 0x00B7, 0xB6: 0x00B6, 0xB7: 0x2022, 0xB8: 0x201A, 0xB9: 0x201E, 0xBA: 0x201D,
	0xBB: 0x00BB, 0xBC: 0x2026, 0xBD: 0x2030, 0xBF: 0x00BF, 0xC1: 0x0060, 0xC2: 0x00B4,
	0xC3: 0x02C6, 0xC4: 0x02DC, 0xC5: 0x00AF, 0xC6: 0x02D8, 0xC7: 0x02D9, 0xC8: 0x00A8,
	0xCA: 0x02DA, 0xCB: 0x00B8, 0xCD: 0x02DD, 0xCE: 0x02DB, 0xCF: 0x02C7, 0xD0: 0x2014,
	0xE1: 0x00C6, 0xE3: 0x00AA, 0xE8: 0x0141, 0xE9: 0x00D8, 0xEA: 0x0152, 0xEB: 0x00BA,
	0xF1: 0x00E6, 0xF5: 0x0131, 0xF8: 0x0142, 0xF9: 0x00F8, 0xFA: 0x0153, 0xFB: 0x00DF,
})

var macRomanEncoding = latin1(&[128]rune{
	0x00C4, 0x00C5, 0x00C7, 0x00C9, 0x00D1, 0x00D6, 0x00DC, 0x00E1, 0x00E0, 0x00E2, 0x00E4, 0x00E3, 0x00E5, 0x00E7, 0x00E9, 0x00E8,
	0x00EA, 0x00EB, 0x00ED, 0x00EC, 0x00EE, 0x00EF, 0x00F1, 0x00F3, 0x00F2, 0x00F4, 0x00F6, 0x00F5, 0x00FA, 0x00F9, 0x00FB, 0x00FC,
	0x2020, 0x00B0, 0x00A2, 0x00A3, 0x00A7, 0x2022, 0x00B6, 0x00DF, 0x00AE, 0x00A9, 0x2122, 0x00B4, 0x00A8, 0x2260, 0x00C6, 0x00D8,
	0x221E, 0x00B1, 0x2264, 0x2265, 0x00A5, 0x00B5, 0x2202, 0x2211, 0x220F, 0x03C0, 0x222B, 0x00AA, 0x00BA, 0x03A9, 0x00E6, 0x00F8,
	0x00BF, 0x00A1, 0x00AC, 0x221A, 0x0192, 0x2248, 0x2206, 0x00AB, 0x00BB, 0x2026, 0x00A0, 0x00C0, 0x00C3, 0x00D5, 0x0152, 0x0153,
	0x2013, 0x2014, 0x201C, 0x201D, 0x2018, 0x2019, 0x00F7, 0x25CA, 0x00FF, 0x0178, 0x2044, 0x00A4, 0x2039, 0x203A, 0xFB01, 0xFB02,
	0x2021, 0x00B7, 0x201A, 0x201E, 0x2030, 0x00C2, 0x00CA, 0x00C1, 0x00CB, 0x00C8, 0x00CD, 0x00CE, 0x00CF, 0x00CC, 0x00D3, 0x00D4,
	0xF8FF, 0x00D2, 0x00DA, 0x00DB, 0x00D9, 0x0131, 0x02C6, 0x02DC, 0x00AF, 0x02D8, 0x02D9, 0x02DA, 0x00B8, 0x02DD, 0x02DB, 0x02C7,
}, nil)

// pdfDocEncoding is the encoding of text strings without a byte order mark.
var pdfDocEncoding = latin1(&identityHigh, map[byte]rune{
	'\t': '\t', '\n': '\n', '\r': '\r',
	0x18: 0x02D8, 0x19: 0x02C7, 0x1A: 0x02C6, 0x1B: 0x02D9, 0x1C: 0x02DD, 0x1D: 0x02DB, 0x1E: 0x02DA, 0x1F: 0x02DC,
	0x80: 0x2022, 0x81: 0x2020, 0x82: 0x2021, 0x83: 0x2026, 0x84: 0x2014, 0x85: 0x2013, 0x86: 0x0192, 0x87: 0x2044,
	0x88: 0x2039, 0x89: 0x203A, 0x8A: 0x2212, 0x8B: 0x2030, 0x8C: 0x201E, 0x8D: 0x201C, 0x8E: 0x201D, 0x8F: 0x2018,
	0x90: 0x2019, 0x91: 0x201A, 0x92: 0x2122, 0x93: 0xFB01, 0x94: 0xFB02, 0x95: 0x0141, 0x96: 0x0152, 0x97: 0x0160,
	0x98: 0x0178, 0x99: 0x017D, 0x9A: 0x0131, 0x9B: 0x0142, 0x9C: 0x0153, 0x9D: 0x0161, 0x9E: 0x017E, 0xA0: 0x20AC,
	0xAD: 0,
})

// glyphs maps glyph names, as a /Differences array or a font program gives them,
// to Unicode: the Latin letters with their accents, digits, punctuation, quotes,
// dashes, bullets and ligatures, and the Greek letters and math symbols of TeX's
// fonts. Names outside it are tried as uniXXXX or uXXXX[XX].
var glyphs = func() map[string]rune {
	m := map[string]rune{
		"quoteleft": 0x2018, "quoteright": 0x2019, "quotesinglbase": 0x201A, "quotereversed": 0x201B,
		"quotedblleft": 0x201C, "quotedblright": 0x201D, "quotedblbase": 0x201E,
		"dagger": 0x2020, "daggerdbl": 0x2021, "bullet": 0x2022, "onedotenleader": 0x2024,
		"twodotenleader": 0x2025, "ellipsis": 0x2026, "perthousand": 0x2030, "minute": 0x2032,
		"second": 0x2033, "guilsinglleft": 0x2039, "guilsinglright": 0x203A, "fraction": 0x2044,
		"figuredash": 0x2012, "endash": 0x2013, "emdash": 0x2014, "underscoredbl": 0x2017,
		"minus": 0x2212, "Euro": 0x20AC, "euro": 0x20AC, "trademark": 0x2122, "florin": 0x0192,
		"circumflex": 0x02C6, "caron": 0x02C7, "breve": 0x02D8, "dotaccent": 0x02D9, "ring": 0x02DA,
		"ogonek": 0x02DB, "tilde": 0x02DC, "hungarumlaut": 0x02DD,
		"ff": 0xFB00, "fi": 0xFB01, "fl": 0xFB02, "ffi": 0xFB03, "ffl": 0xFB04,
		"middot": 0x00B7, "nonbreakingspace": 0x00A0, "dotlessj": 0x0237, "lozenge": 0x25CA,
		"notequal": 0x2260, "infinity": 0x221E, "lessequal": 0x2264, "greaterequal": 0x2265,
		"partialdiff": 0x2202, "summation": 0x2211, "product": 0x220F, "integral": 0x222B,
		"radical": 0x221A, "approxequal": 0x2248, "Ohm": 0x2126, "increment": 0x2206,
		"apple": 0xF8FF, "estimated": 0x212E,
		// The symbols of TeX's math fonts that turn up in running text.
		"similar": 0x223C, "element": 0x2208, "arrowdblright": 0x21D2, "arrowdblleft": 0x21D0,
		"arrowboth": 0x2194, "arrowdblboth": 0x21D4, "asteriskmath": 0x2217, "proportional": 0x221D,
		"equivalence": 0x2261, "propersubset": 0x2282, "propersuperset": 0x2283,
		"reflexsubset": 0x2286, "reflexsuperset": 0x2287, "union": 0x222A, "intersection": 0x2229,
		"logicaland": 0x2227, "logicalor": 0x2228, "universal": 0x2200, "existential": 0x2203,
		"emptyset": 0x2205, "nabla": 0x2207, "prime": 0x2032, "angleleft": 0x2329, "angleright": 0x232A,
		"circlemultiply": 0x2297, "circleplus": 0x2295, "lessmuch": 0x226A, "greatermuch": 0x226B,
		"theta1": 0x03D1, "phi1": 0x03D5, "omega1": 0x03D6, "sigma1": 0x03C2,
		"arrowleft": 0x2190, "arrowup": 0x2191, "arrowright": 0x2192, "arrowdown": 0x2193,
		"Dslash": 0x0110, "dmacron": 0x0111, "Gcedilla": 0x0122, "gcedilla": 0x0123,
		"Kcedilla": 0x0136, "kcedilla": 0x0137, "Lcedilla": 0x013B, "lcedilla": 0x013C,
		"Ncedilla": 0x0145, "ncedilla": 0x0146, "Rcedilla": 0x0156, "rcedilla": 0x0157,
		"Tcedilla": 0x0162, "tcedilla": 0x0163, "Idot": 0x0130, "Edot": 0x0116, "edot": 0x0117,
		"Gdot": 0x0120, "gdot": 0x0121, "Cdot": 0x010A, "cdot": 0x010B, "Zdot": 0x017B,
		"zdot": 0x017C, "Odblacute": 0x0150, "odblacute": 0x0151, "Udblacute": 0x0170,
		"udblacute": 0x0171, "kra": 0x0138, "quoterightn": 0x0149, "Ldotaccent": 0x013F,
		"ldotaccent": 0x0140, "Scommaaccent": 0x0218, "scommaaccent": 0x0219,
	}
	add := func(first rune, names string) {
		for i, n := range strings.Fields(names) {
			m[n] = first + rune(i)
		}
	}
	add(0x20, "space exclam quotedbl numbersign dollar percent ampersand quotesingle "+
		"parenleft parenright asterisk plus comma hyphen period slash "+
		"zero one two three four five six seven eight nine colon semicolon less equal greater question at")
	add(0x5B, "bracketleft backslash bracketright asciicircum underscore grave")
	add(0x7B, "braceleft bar braceright asciitilde")
	for c := 'A'; c <= 'Z'; c++ {
		m[string(c)] = c
		m[string(c+32)] = c + 32
	}
	add(0x0391, "Alpha Beta Gamma Delta Epsilon Zeta Eta Theta Iota Kappa Lambda Mu Nu Xi Omicron Pi Rho")
	add(0x03A3, "Sigma Tau Upsilon Phi Chi Psi Omega")
	add(0x03B1, "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mugreek nu xi "+
		"omicron pi rho sigmafinal sigma tau upsilon phi chi psi omega")
	add(0xA0, "nbspace exclamdown cent sterling currency yen brokenbar section dieresis copyright "+
		"ordfeminine guillemotleft logicalnot sfthyphen registered macron degree plusminus "+
		"twosuperior threesuperior acute mu paragraph periodcentered cedilla onesuperior "+
		"ordmasculine guillemotright onequarter onehalf threequarters questiondown "+
		"Agrave Aacute Acircumflex Atilde Adieresis Aring AE Ccedilla Egrave Eacute Ecircumflex "+
		"Edieresis Igrave Iacute Icircumflex Idieresis Eth Ntilde Ograve Oacute Ocircumflex "+
		"Otilde Odieresis multiply Oslash Ugrave Uacute Ucircumflex Udieresis Yacute Thorn "+
		"germandbls agrave aacute acircumflex atilde adieresis aring ae ccedilla egrave eacute "+
		"ecircumflex edieresis igrave iacute icircumflex idieresis eth ntilde ograve oacute "+
		"ocircumflex otilde odieresis divide oslash ugrave uacute ucircumflex udieresis yacute "+
		"thorn ydieresis")
	add(0x100, "Amacron amacron Abreve abreve Aogonek aogonek Cacute cacute Ccircumflex "+
		"ccircumflex Cdotaccent cdotaccent Ccaron ccaron Dcaron dcaron Dcroat dcroat Emacron "+
		"emacron Ebreve ebreve Edotaccent edotaccent Eogonek eogonek Ecaron ecaron Gcircumflex "+
		"gcircumflex Gbreve gbreve Gdotaccent gdotaccent Gcommaaccent gcommaaccent Hcircumflex "+
		"hcircumflex Hbar hbar Itilde itilde Imacron imacron Ibreve ibreve Iogonek iogonek "+
		"Idotaccent dotlessi IJ ij Jcircumflex jcircumflex Kcommaaccent kcommaaccent "+
		"kgreenlandic Lacute lacute Lcommaaccent lcommaaccent Lcaron lcaron Ldot ldot Lslash "+
		"lslash Nacute nacute Ncommaaccent ncommaaccent Ncaron ncaron napostrophe Eng eng "+
		"Omacron omacron Obreve obreve Ohungarumlaut ohungarumlaut OE oe Racute racute "+
		"Rcommaaccent rcommaaccent Rcaron rcaron Sacute sacute Scircumflex scircumflex "+
		"Scedilla scedilla Scaron scaron Tcommaaccent tcommaaccent Tcaron tcaron Tbar tbar "+
		"Utilde utilde Umacron umacron Ubreve ubreve Uring uring Uhungarumlaut uhungarumlaut "+
		"Uogonek uogonek Wcircumflex wcircumflex Ycircumflex ycircumflex Ydieresis Zacute "+
		"zacute Zdotaccent zdotaccent Zcaron zcaron longs")
	return m
}()

// glyphText returns the text a glyph name stands for, or "". It follows the Adobe
// glyph list's rules: a suffix after a period is dropped (a.sc is a), underscores
// join ligature parts (f_f_i is ffi), and uniXXXX or uXXXX[XX] give the code
// points in hex.
func glyphText(n string) string {
	if r, ok := glyphs[n]; ok {
		return string(r)
	}
	if i := strings.IndexByte(n, '.'); i > 0 {
		n = n[:i]
		if r, ok := glyphs[n]; ok {
			return string(r)
		}
	}
	if strings.Contains(n, "_") {
		var b strings.Builder
		for _, part := range strings.Split(n, "_") {
			if part == "" {
				return ""
			}
			b.WriteString(glyphText(part))
		}
		return b.String()
	}
	if hex, ok := strings.CutPrefix(n, "uni"); ok && len(hex) >= 4 && len(hex)%4 == 0 && len(hex) <= 64 {
		var b strings.Builder
		for i := 0; i < len(hex); i += 4 {
			v, err := strconv.ParseUint(hex[i:i+4], 16, 32)
			if err != nil || v >= 0xD800 && v <= 0xDFFF {
				return ""
			}
			b.WriteRune(rune(v))
		}
		return b.String()
	}
	if hex, ok := strings.CutPrefix(n, "u"); ok && len(hex) >= 4 && len(hex) <= 6 {
		v, err := strconv.ParseUint(hex, 16, 32)
		if err == nil && v <= utf8.MaxRune && (v < 0xD800 || v > 0xDFFF) {
			return string(rune(v))
		}
	}
	return ""
}

// numericGlyph reads glyph names such as a65 or c101, the character code in
// decimal after a letter or two, which TeX's bitmap Type 3 fonts use. Elsewhere
// such a name is more often a glyph index, so only Type 3 fonts consult this.
func numericGlyph(n string) string {
	i := 0
	for i < len(n) && i < 2 && (n[i] >= 'a' && n[i] <= 'z' || n[i] >= 'A' && n[i] <= 'Z') {
		i++
	}
	digits := n[i:]
	if len(digits) < 2 || len(digits) > 4 {
		return ""
	}
	v, err := strconv.Atoi(digits)
	if err != nil || v < 32 || v == 127 || v > 255 || v >= 128 && v < 160 {
		return ""
	}
	return string(rune(v))
}

// textString decodes a PDF text string: UTF-16BE or UTF-8 when it starts with a
// byte order mark, PDFDocEncoding otherwise.
func textString(s string) string {
	switch {
	case strings.HasPrefix(s, "\xfe\xff"):
		return utf16BE(s[2:])
	case strings.HasPrefix(s, "\xef\xbb\xbf"):
		return strings.ToValidUTF8(s[3:], "")
	case strings.HasPrefix(s, "\xff\xfe"): // little-endian: not allowed, but written
		b := []byte(s[2:])
		for i := 0; i+1 < len(b); i += 2 {
			b[i], b[i+1] = b[i+1], b[i]
		}
		return utf16BE(string(b))
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if r := pdfDocEncoding[s[i]]; r != 0 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// utf16BE decodes big-endian UTF-16, leaving out language escapes: a language
// code between two U+001B marks.
func utf16BE(s string) string {
	u := make([]uint16, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		c := uint16(s[i])<<8 | uint16(s[i+1])
		if c == 0x1B {
			// Skip to the closing escape; its bytes are a language tag, not text.
			j := i + 2
			for j+1 < len(s) && !(s[j] == 0 && s[j+1] == 0x1B) {
				j += 2
			}
			i = j
			continue
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

// Advance widths of the printable ASCII characters, in thousandths of an em, for
// the standard fonts a file may use without listing widths. They place each run
// of glyphs well enough to tell a word gap from kerning. Courier is 600
// throughout; Helvetica stands in for sans-serif faces, Times for serif ones.
var (
	helveticaWidths = [95]uint16{
		278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
		556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
		1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
		667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
		333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
		556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
	}
	helveticaBoldWidths = [95]uint16{
		278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
		556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
		975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
		667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
		333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
		611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584,
	}
	timesWidths = [95]uint16{
		250, 333, 408, 500, 500, 833, 778, 180, 333, 333, 500, 564, 250, 333, 250, 278,
		500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 278, 278, 564, 564, 564, 444,
		921, 722, 667, 667, 722, 611, 556, 722, 722, 333, 389, 722, 611, 889, 722, 722,
		556, 722, 667, 556, 611, 722, 722, 944, 722, 722, 611, 333, 278, 333, 469, 500,
		333, 444, 500, 444, 500, 444, 333, 500, 500, 278, 278, 500, 278, 778, 500, 500,
		500, 500, 333, 389, 278, 500, 500, 722, 500, 500, 444, 480, 200, 480, 541,
	}
	timesBoldWidths = [95]uint16{
		250, 333, 555, 500, 500, 1000, 833, 278, 333, 333, 500, 570, 250, 333, 250, 278,
		500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 333, 333, 570, 570, 570, 500,
		930, 722, 667, 722, 722, 667, 611, 778, 778, 389, 500, 778, 667, 944, 722, 778,
		611, 778, 722, 556, 667, 722, 722, 1000, 722, 722, 667, 333, 278, 333, 581, 500,
		333, 500, 556, 444, 556, 444, 333, 500, 556, 278, 333, 556, 278, 833, 556, 500,
		556, 556, 444, 389, 333, 556, 500, 722, 500, 500, 444, 394, 220, 394, 520,
	}
	timesItalicWidths = [95]uint16{
		250, 333, 420, 500, 500, 833, 778, 214, 333, 333, 500, 675, 250, 333, 250, 278,
		500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 333, 333, 675, 675, 675, 500,
		920, 611, 611, 667, 722, 611, 611, 722, 722, 333, 444, 667, 556, 833, 667, 722,
		611, 722, 611, 500, 556, 722, 611, 833, 611, 556, 556, 389, 278, 389, 422, 500,
		333, 500, 500, 444, 500, 444, 278, 500, 500, 278, 278, 444, 278, 722, 500, 500,
		500, 500, 389, 389, 278, 500, 444, 667, 444, 444, 389, 400, 275, 400, 541,
	}
	timesBoldItalicWidths = [95]uint16{
		250, 389, 555, 500, 500, 833, 778, 278, 333, 333, 500, 570, 250, 333, 250, 278,
		500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 333, 333, 570, 570, 570, 500,
		832, 667, 667, 667, 722, 667, 667, 722, 778, 389, 500, 667, 611, 889, 722, 722,
		611, 722, 667, 556, 611, 722, 667, 889, 667, 611, 611, 333, 278, 333, 570, 500,
		333, 500, 500, 444, 500, 444, 333, 500, 556, 278, 278, 500, 278, 778, 556, 500,
		500, 500, 389, 389, 278, 556, 444, 667, 500, 444, 389, 348, 220, 348, 570,
	}
	courierWidths = func() (w [95]uint16) {
		for i := range w {
			w[i] = 600
		}
		return w
	}()
)

// standardWidths picks the metrics for a font named base: one of the standard
// fonts, or a common face that shares their widths, such as Arial.
func standardWidths(base string) *[95]uint16 {
	b := strings.ToLower(base)
	if i := strings.IndexByte(b, '+'); i >= 0 {
		b = b[i+1:] // a subset prefix such as ABCDEF+
	}
	bold := strings.Contains(b, "bold") || strings.Contains(b, "black") || strings.Contains(b, "heavy")
	italic := strings.Contains(b, "italic") || strings.Contains(b, "oblique")
	switch {
	case strings.Contains(b, "courier") || strings.Contains(b, "mono"):
		return &courierWidths
	case strings.Contains(b, "times") || strings.Contains(b, "serif") && !strings.Contains(b, "sans"):
		switch {
		case bold && italic:
			return &timesBoldItalicWidths
		case bold:
			return &timesBoldWidths
		case italic:
			return &timesItalicWidths
		}
		return &timesWidths
	case bold:
		return &helveticaBoldWidths
	}
	return &helveticaWidths
}
