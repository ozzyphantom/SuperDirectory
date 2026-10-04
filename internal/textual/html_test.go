package textual

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestHTMLStructure(t *testing.T) {
	page := `<!DOCTYPE html>
<html><head><title> The  Guide </title>
<style>p { color: red }</style>
<script>var x = "<p>not text</p>";</script></head>
<body>
<nav><a href="/">Home</a> | <a href="/docs">Docs</a></nav>
<h1>Install</h1>
<p>Run   the <b>installer</b>&nbsp;now &amp; wait.</p>
<ul><li>One</li><li>Two<ul><li>Two A</li></ul></li></ul>
<ol><li>First</li></ol>
<table><tr><th>Key</th><th>Value</th></tr><tr><td>a</td><td></td><td>1</td></tr></table>
<pre>  indented
    more

end
</pre>
<p>Line<br>break</p>
<noscript>Enable JavaScript</noscript><template><p>template text</p></template>
<svg><title>icon</title><text>svg text</text></svg>
<math><mi>x</mi></math>
<iframe src="x">frame text</iframe>
<object data="x">object text</object>
<dl><dt>Term</dt><dd>Definition</dd></dl>
<h2>Next <br> part</h2>
<div>tail</div>
</body></html>`

	want := "# The Guide\n\n" +
		"# Install\n\n" +
		"Run the installer now & wait.\n\n" +
		"- One\n- Two\n  - Two A\n\n" +
		"- First\n\n" +
		"Key | Value\na | 1\n\n" +
		"```\n  indented\n    more\n\nend\n```\n\n" +
		"Line\nbreak\n\n" +
		"Term\n: Definition\n\n" +
		"## Next part\n\n" +
		"tail"
	if got := extract(t, "page.html", []byte(page), 0); got != want {
		t.Errorf("got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestHTMLHeadings(t *testing.T) {
	page := "<h1>A</h1><h2>B</h2><h3>C</h3><h4>D</h4><h5>E</h5><h6>F</h6><h3></h3><p>after</p>"
	want := "# A\n\n## B\n\n### C\n\n#### D\n\n##### E\n\n###### F\n\nafter"
	if got := extract(t, "a.htm", []byte(page), 0); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHTMLPre(t *testing.T) {
	// A fence must outrun the backticks inside, or the block would end early.
	page := "<p>Example:</p><pre>\n\n  ```go\n  x := 1\n  ```\xc2\xa0\n\n</pre><p>done</p>"
	want := "Example:\n\n````\n  ```go\n  x := 1\n  ```\n````\n\ndone"
	if got := extract(t, "a.html", []byte(page), 0); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// In a table cell, a block cannot be fenced; its text runs inline.
	page = "<table><tr><td><pre>a\n  b</pre></td><td>c</td></tr></table>"
	if got := extract(t, "a.html", []byte(page), 0); got != "a b | c" {
		t.Errorf("pre in a cell: got %q", got)
	}
}

func TestHTMLBreaks(t *testing.T) {
	for _, tc := range []struct{ page, want string }{
		{"a<br>b", "a\nb"},
		{"a<br><br>b", "a\n\nb"},
		{"a<br><br><br><br>b", "a\n\nb"},
		{"<p>a</p><p></p><p> </p><div></div><p>b</p>", "a\n\nb"},
		{"<div>a</div><div>b</div>", "a\nb"},
		{"<br><br><p>first</p>", "first"},
		{"<p>a <span> b </span> c</p>", "a b c"},
		{"<p>a<span>b</span>c</p>", "abc"},
		{"<p>tab\tand\nnewline</p>", "tab and newline"},
		{"<p>&lt;tag&gt; &copy; &#8364; &eacute;</p>", "<tag> © € é"},
	} {
		if got := extract(t, "a.html", []byte(tc.page), 0); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.page, got, tc.want)
		}
	}
}

func TestHTMLControlCharacters(t *testing.T) {
	// Found by FuzzHTML: a word made only of control characters, dropped late,
	// left the space written before it at the end of a line.
	page := "<p>one \x04\x00</p><h1>two</h1><p>a\x01b</p>"
	if got := extract(t, "a.html", []byte(page), 0); got != "one\n\n# two\n\nab" {
		t.Errorf("got %q", got)
	}
	page = "000000<h00<h0>000 \x04\x00<h1>0<\xd9\xd9\xd9\xd9\xd9\xd9\xd9\xd9Br>"
	if got := extract(t, "a.html", []byte(page), 0); strings.Contains(got, " \n") {
		t.Errorf("stray space: %q", got)
	}
}

func TestHTMLTables(t *testing.T) {
	page := `<table><caption>Ports</caption>
<thead><tr><th>Port</th><th>Use</th></tr></thead>
<tbody><tr><td>22</td><td><p>SSH</p><p>secure</p></td></tr>
<tr><td></td><td>empty first</td></tr>
<tr><td><table><tr><td>in</td><td>ner</td></tr></table></td><td>outer</td></tr></tbody></table>`
	want := "Ports\nPort | Use\n22 | SSH secure\nempty first\nin | ner | outer"
	if got := extract(t, "a.html", []byte(page), 0); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHTMLLists(t *testing.T) {
	page := `<p>Intro</p><ul><li><p>Para item</p></li><li>Outer<ol><li>Inner<ul><li>Deep</li></ul></li></ol></li><li>Last</li></ul><p>After</p>`
	want := "Intro\n\n- Para item\n- Outer\n  - Inner\n    - Deep\n- Last\n\nAfter"
	if got := extract(t, "a.html", []byte(page), 0); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHTMLTitle(t *testing.T) {
	for _, tc := range []struct{ page, want string }{
		{"<p>No title</p>", "No title"},
		{"<title></title><p>Empty title</p>", "Empty title"},
		{"<title>A &amp; B</title><h1>Body</h1>", "# A & B\n\n# Body"},
		{"<p>x</p><svg><title>not the page</title></svg>", "x"},
	} {
		if got := extract(t, "a.html", []byte(tc.page), 0); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.page, got, tc.want)
		}
	}
}

func TestHTMLEncodings(t *testing.T) {
	if got := extract(t, "a.html", []byte("<p>caf\xe9 \x93ol\xe9\x94</p>"), 0); got != "café “olé”" {
		t.Errorf("windows-1252: got %q", got)
	}
	page := utf16Bytes("<p>naïve 😀</p>", binary.LittleEndian, true)
	if got := extract(t, "a.html", page, 0); got != "naïve 😀" {
		t.Errorf("utf-16: got %q", got)
	}
}

func TestHTMLDeepNesting(t *testing.T) {
	// The parser refuses pages nested past 512 elements. The text must survive,
	// with the structure the shallow part of the page has.
	const depth = 5000
	for _, tc := range []struct{ page, want string }{
		{strings.Repeat("<div>", depth) + "deep" + strings.Repeat("</div>", depth), "deep"},
		{"<h1>Top</h1><ul><li>item</li></ul>" + strings.Repeat("<div>", depth) +
			"<p>one</p><script>var hidden = 1;</script><p>two</p>" + strings.Repeat("</div>", depth),
			"# Top\n\n- item\n\none\n\ntwo"},
		{strings.Repeat("<div/>", depth) + "self-closed", "self-closed"},
		{strings.Repeat(`<font size="2">`, depth) + "unclosed fonts", "unclosed fonts"},
	} {
		if got := extract(t, "a.html", []byte(tc.page), 0); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestUnnest(t *testing.T) {
	page := `<div><div><p>a</p><script>if (x < y) {}</script><br><img src=x></div></div><i>b</i>`
	for depth, want := range map[int]string{
		2: `<div><div><br>a<br><script>if (x < y) {}</script><br><img src=x></div></div><i>b</i>`,
		0: `<br><br><br>a<br><script>if (x < y) {}</script><br><img src=x><br><br>b`,
	} {
		if got := unnest(page, depth); got != want {
			t.Errorf("depth %d:\n got %s\nwant %s", depth, got, want)
		}
	}
}
