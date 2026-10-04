package textual

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// checkText verifies what Text promises of any input once tidy has run.
func checkText(t *testing.T, s string, limit int) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("invalid UTF-8: %q", s)
	}
	if n := utf8.RuneCountInString(s); n > limit {
		t.Fatalf("%d runes, limit %d", n, limit)
	}
	for _, r := range s {
		if r == '\r' || r < 0x20 && r != '\t' && r != '\n' || r >= 0x7F && r < 0xA0 || r == '\uFEFF' {
			t.Fatalf("control character %U in %q", r, s)
		}
	}
	if strings.TrimRight(s, " \t\n") != s {
		t.Fatalf("trailing whitespace in %q", s)
	}
}

// fuzzLimits are a roomy limit and a tight one, which cuts output mid-word.
func fuzzLimits(data []byte) []int { return []int{4096, 1 + len(data)%23} }

func FuzzHTML(f *testing.F) {
	for _, seed := range []string{
		"<p>Hello, <b>world</b>!</p>",
		"<title>T</title><h1>A</h1><h2>B<br>C</h2><p>x&nbsp;y &amp; z</p>",
		"<ul><li>a<ul><li>b<ol><li>c</ol></ul><li><p>d</p></ul>",
		"<table><caption>c</caption><tr><th>k<th>v<tr><td><td>1<tr><td><table><tr><td>in</table></table>",
		"<pre>\n\n  ```x\n  y\xc2\xa0</pre><pre></pre><td><pre>a\nb</pre>",
		"<dl><dt>t<dd>d</dl><hr><br><br><br>tail",
		"<nav>n</nav><script>s</script><style>s</style><svg><title>t</title></svg><math>m</math>",
		"<template><p>t</template><noscript>n</noscript><iframe>i</iframe><object>o</object>",
		"\xEF\xBB\xBF<p>bom</p>",
		"\xFF\xFE<\x00p\x00>\x00a\x00",
		"<p>caf\xe9</p>",
		strings.Repeat("<div>", 600) + "deep" + strings.Repeat("</div>", 600),
		strings.Repeat("<font>", 600) + "<script>x</script>" + strings.Repeat("<p>", 600),
		"<h3><li>x</li></h3><td>stray cell</td><li>stray item",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, limit := range fuzzLimits(data) {
			raw, _ := htmlFile(bytes.NewReader(data), int64(len(data)), limit)
			s := tidy(raw, limit)
			checkText(t, s, limit)
			// Outside fenced code the builder writes only words, single spaces
			// between them, and at most one blank line between blocks.
			if !strings.Contains(s, "```") {
				if strings.Contains(s, "\n\n\n") || strings.Contains(s, " \n") || strings.ContainsRune(s, '\t') {
					t.Fatalf("stray whitespace in %q", s)
				}
			}
		}
	})
}

func FuzzRTF(f *testing.F) {
	for _, seed := range []string{
		`{\rtf1\ansi{\fonttbl{\f0 Arial;}}\pard Caf\'e9\par Next\line line\tab tab}`,
		`{\rtf1 a荤?b{\uc2舒\'97\'97}c\uc0 3 d}`,
		`{\rtf1 \u-10179?\u-8704? \u-10179?x 嚃2?}`,
		`{\rtf1{\*\unknown x}{\pict 0102}{\field{\*\fldinst X}{\fldrslt shown}}}`,
		`{\rtf1\bin3 {}}after\bin-5 x\bin}`,
		`{\rtf1 \{\}\\\~\-\_\'zz\'4`,
		`{\rtf1\trowd a\cell b\cell\row c\cell\row}`,
		`{\rtf1 ` + strings.Repeat("{", 2000) + "deep" + strings.Repeat("}", 2000) + `}`,
		`{\rtf1\abcdefghijklmnopqrstuvwxyzabcdefghij123456789012345 x}`,
		"  \xEF\xBB\xBF{\\rtf1 x}",
		`not rtf`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, limit := range fuzzLimits(data) {
			raw, err := rtf(bytes.NewReader(data), int64(len(data)), limit)
			if err != nil && (err != errNotRTF || raw != "") {
				t.Fatalf("err %v with %q", err, raw)
			}
			s := tidy(raw, limit)
			checkText(t, s, limit)
			if strings.Contains(s, "\n\n\n") {
				t.Fatalf("more than one blank line in %q", s)
			}
		}
	})
}
