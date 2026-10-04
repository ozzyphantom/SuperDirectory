package textual

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRTF(t *testing.T) {
	doc := `{\rtf1\ansi\ansicpg1252\deff0{\fonttbl{\f0\fswiss Helvetica;}{\f1 Times;}}` +
		`{\colortbl;\red255\green0\blue0;}{\stylesheet{\s0 Normal;}}` +
		`{\info{\title Secret Title}{\author Someone}}{\*\generator Riched20 10.0;}` + "\r\n" +
		`\pard\f0\fs24 Caf\'e9 cr\'E8me \'93quoted\'94\par` + "\r\n" +
		`Tab\tab here\line next\par` + "\n" +
		`{\*\bkmkstart b1}Uni\u8364?code {\uc2\u8212\'97\'97} dash\par` + "\n" +
		`{\uc0\u20013\u25991}\par` + "\n" +
		`Emoji \u-10179?\u-8704?\par` + "\n" +
		`Braces \{ \} \\ back\~space\par` + "\n" +
		`{\pict\pngblip 89504e470d0a1a0a}{\field{\*\fldinst HYPERLINK "http://x"}{\fldrslt link}}\par` + "\n" +
		`\bin4 {}{}after bin\par` + "\n" +
		`}trailing junk`

	want := "Café crème “quoted”\n" +
		"Tab\there\nnext\n" +
		"Uni€code — dash\n" +
		"中文\n" +
		"Emoji 😀\n" +
		"Braces { } \\ back space\n" +
		"link\n" +
		"after bin"
	if got := extract(t, "a.rtf", []byte(doc), 0); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestRTFParagraphs(t *testing.T) {
	doc := `{\rtf1 One\par\par Two\par\pard\par\par\par Three\sect Four\page Five}`
	if got := extract(t, "a.rtf", []byte(doc), 0); got != "One\n\nTwo\n\nThree\nFour\nFive" {
		t.Errorf("got %q", got)
	}
}

func TestRTFControlCharacters(t *testing.T) {
	// Found by FuzzRTF: a line holding only a control character, dropped late,
	// left its line break behind, three newlines in a row.
	for _, doc := range []string{`{\rtf1 a\par\'15\par\par b}`, "{\\rtf1 a\\par \x01\\par\\par b}"} {
		if got := extract(t, "a.rtf", []byte(doc), 0); got != "a\n\nb" {
			t.Errorf("%q: got %q", doc, got)
		}
	}
	doc := "{\\rtf\\&0\\\n\x15\\\n\\&\\\nyy\\*\\\n\n\n\n\n\n\n0\\\n\n0\\\n\x15"
	if got := extract(t, "a.rtf", []byte(doc), 0); strings.Contains(got, "\n\n\n") {
		t.Errorf("three newlines: %q", got)
	}
}

func TestRTFTable(t *testing.T) {
	doc := `{\rtf1\trowd\cellx1000\cellx2000 \intbl Key\cell Value\cell\row\trowd \intbl a\cell 1\cell\row}`
	if got := extract(t, "a.rtf", []byte(doc), 0); got != "Key | Value\na | 1" {
		t.Errorf("got %q", got)
	}
}

func TestRTFFallbackSkipping(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		// The fallback count is the group's: \uc2 skips two, then reverts.
		{`{\rtf1{\uc2\u233 xxy}-\u233 xz}`, "éy-éz"},
		// A control word counts as one fallback character.
		{`{\rtf1\u233\'e9 z}`, "é z"},
		// A brace ends the fallback early.
		{`{\rtf1{\uc3\u233 a}b}`, "éb"},
		// A lone surrogate is replaced, not dropped.
		{`{\rtf1 x\u-10179?y}`, "x\uFFFDy"},
		// Negative \u values wrap into the upper half of the 16-bit range.
		{`{\rtf1\u-4064?}`, "\uF020"},
	} {
		if got := extract(t, "a.rtf", []byte(tc.doc), 0); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.doc, got, tc.want)
		}
	}
}

func TestRTFSkippedGroups(t *testing.T) {
	doc := `{\rtf1{\*\unknowndest secret}{\fonttbl{\f0 Font;}}{\colortbl;}` +
		`{\stylesheet{\s1 Heading;}}{\info{\author A}}{\pict{\*\blipuid 00}ffd8}` +
		`{\object{\objdata 0102}}{\*\themedata 504b}{\*\datastore 0105}` +
		`{\footnote note}{\*\fldinst PAGE}{\fldinst  PAGE }shown}`
	if got := extract(t, "a.rtf", []byte(doc), 0); got != "shown" {
		t.Errorf("got %q", got)
	}
}

func TestRTFLeadingWhitespaceAndBOM(t *testing.T) {
	doc := "\xEF\xBB\xBF" + `{\rtf1 text}`
	if got := extract(t, "a.rtf", []byte(doc), 0); got != "text" {
		t.Errorf("BOM: got %q", got)
	}
	if got := extract(t, "a.rtf", []byte("\r\n  "+`{\rtf1 text}`), 0); got != "text" {
		t.Errorf("leading whitespace: got %q", got)
	}
}

func TestRTFNotRTF(t *testing.T) {
	_, err := Text("a.rtf", strings.NewReader("plain text, not RTF"), 19, 0)
	if !errors.Is(err, errNotRTF) {
		t.Fatalf("err = %v, want errNotRTF", err)
	}
}

func TestRTFLimit(t *testing.T) {
	doc := `{\rtf1 ` + strings.Repeat(`word\par `, 10000) + `}`
	if got := extract(t, "a.rtf", []byte(doc), 25); utf8.RuneCountInString(got) > 25 || !strings.HasPrefix(got, "word\nword") {
		t.Errorf("got %q", got)
	}
	// A paragraph that never ends still stops at the limit.
	doc = `{\rtf1 ` + strings.Repeat("abc ", 100000) + `}`
	if got := extract(t, "a.rtf", []byte(doc), 10); got != "abc abc ab" {
		t.Errorf("got %q", got)
	}
}

func TestRTFUnterminated(t *testing.T) {
	for _, doc := range []string{`{\rtf1 cut off\par more`, `{\rtf1 x\`, `{\rtf1 x\'4`, `{\rtf1{{{{ deep`} {
		if _, err := Text("a.rtf", strings.NewReader(doc), int64(len(doc)), 0); err != nil {
			t.Errorf("%q: %v", doc, err)
		}
	}
}
