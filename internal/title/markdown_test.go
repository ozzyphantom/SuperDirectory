package title

import (
	"strings"
	"testing"
	"time"
)

var markdownCases = []struct{ name, in, want string }{
	// YAML front matter.
	{"yaml plain", "---\ntitle: Plain Title\n---\n# Heading\n", "Plain Title"},
	{"yaml double quoted", "---\ntitle: \"Say \\\"Hi\\\" caf\\u00e9\\x21\"\n---\n", `Say "Hi" café!`},
	{"yaml escapes", "---\ntitle: \"Tab\\tNew\\nLine\\_NBSP \\U0001F600 \\uD83D\\uDE00 \\uZZZZ end\\\n  joined\"\n---\n",
		"Tab New Line NBSP 😀 😀 uZZZZ endjoined"},
	{"yaml single quoted", "---\ntitle: 'It''s Here'\n---\n", "It's Here"},
	{"yaml comment", "---\ntitle: Plain # a comment\n---\n", "Plain"},
	{"yaml hash in a word", "---\ntitle: C# Basics\n---\n", "C# Basics"},
	{"yaml folded block", "---\ntitle: >-\n  Folded\n  Title\nauthor: me\n---\n", "Folded Title"},
	{"yaml literal block", "---\ntitle: |\n  Literal\n  Block\n---\n", "Literal Block"},
	{"yaml plain over lines", "---\ntitle: A Long\n  Wrapped Title\ndate: 2020-01-02\n---\n", "A Long Wrapped Title"},
	{"yaml value on the next line", "---\ntitle:\n  Next Line Title\n---\n", "Next Line Title"},
	{"yaml quoted over lines", "---\ntitle: \"Quoted\n  Over Lines\"\n---\n", "Quoted Over Lines"},
	{"yaml key case", "---\nTitle: Capital Key\n---\n", "Capital Key"},
	{"yaml nested title", "---\nauthor:\n  title: Dr\n---\n# Real Heading\n", "Real Heading"},
	{"yaml null", "---\ntitle: null\n---\n# From Heading\n", "From Heading"},
	{"yaml list", "---\ntitle:\n  - a\n  - b\n---\n# List Heading\n", "List Heading"},
	{"yaml placeholder", "---\ntitle: Untitled\n---\n# Heading Instead\n", "Heading Instead"},
	{"yaml closed by dots", "---\ntitle: Dots End\n...\nbody\n", "Dots End"},
	{"yaml never closed", "---\ntitle: Not Front Matter\n# Body Heading\n", "Body Heading"},
	{"byte order mark and CRLF", "\xef\xbb\xbf---\r\ntitle: Windows File\r\n---\r\n", "Windows File"},

	// TOML front matter, as Hugo writes it.
	{"toml", "+++\ntitle = \"Hugo Page\"\ndate = 2021-01-01\n+++\n", "Hugo Page"},
	{"toml literal string", "+++\ntitle = 'C:\\Path'\n+++\n", `C:\Path`},
	{"toml multi-line string", "+++\ntitle = \"\"\"\nMulti\nLine\"\"\"\n+++\n", "Multi Line"},
	{"toml escaped quote", "+++\ntitle = \"\"\"A \\\" quote\"\"\"\n+++\n", `A " quote`},
	{"toml multi-line literal", "+++\ntitle = '''Literal\n\\no escape'''\n+++\n", `Literal \no escape`},
	{"toml unclosed", "+++\ntitle = \"\"\"Never closed\n+++\n# Fallback\n", "Fallback"},
	{"toml table", "+++\n[params]\ntitle = \"Nested\"\n+++\n# TOML Heading\n", "TOML Heading"},
	{"toml number", "+++\ntitle = 2024\n+++\n# Numbered\n", "Numbered"},

	// ATX headings.
	{"atx", "Intro text\n\n# ATX Title #\n", "ATX Title"},
	{"atx keeps a hash in a word", "# C#\n", "C#"},
	{"atx indented three", "   # Indented Three\n", "Indented Three"},
	{"atx indented four is code", "    # Code Comment\n\n# Real\n", "Real"},
	{"atx tab", "#\tTabbed\n", "Tabbed"},
	{"hashtag", "#hashtag\n# Spaced\n", "Spaced"},
	{"level two", "## Section\n# Document Title\n", "Document Title"},
	{"empty atx", "#\n# Second\n", "Second"},
	{"first heading decides", "# Index\n# Real One\n", ""},
	{"lone CR line ends", "# Old Mac\rbody\r", "Old Mac"},

	// Setext headings.
	{"setext", "Setext Title\n============\n", "Setext Title"},
	{"setext paragraph", "First line\nsecond line\n===\n", "First line second line"},
	{"setext level two", "Sub\n---\nMain\n====\n", "Main"},
	{"setext after a list item", "- item\n===\n# Later\n", "Later"},

	// What is not a heading.
	{"fenced code", "```sh\n# install deps\nmake\n```\n# After Fence\n", "After Fence"},
	{"tilde fence", "~~~\n# not heading\n~~~\nReal Setext\n===\n", "Real Setext"},
	{"unclosed fence", "```\n# hidden\n", ""},
	{"html comment", "<!--\n# Commented Out\n-->\n# Live Heading\n", "Live Heading"},
	{"block quote", "> # Quoted\n# Unquoted\n", "Unquoted"},

	// An HTML heading, as a README centers it under a logo.
	{"html h1", "<h1 align=\"center\">\n  <img src=\"logo.png\" width=\"80\"><br>\n  Fancy Project\n</h1>\n\n# Install\n", "Fancy Project"},
	{"empty html h1", "<h1><img src=\"logo.png\"></h1>\n\n# Markdown Title\n", "Markdown Title"},

	// A heading's Markdown, reduced to what a reader sees.
	{"inline markup", "# The `foo` **command** and _more_ [docs](http://x.y/z)\n", "The foo command and more docs"},
	{"badges", "# MyLib [![Build](https://ci.example/b.svg)](https://ci.example) ![Cov](c.svg)\n", "MyLib"},
	{"snake case", "# my_module_name\n", "my_module_name"},
	{"escapes", "# 100\\% \\*real\\*\n", "100% *real*"},
	{"entities", "# Q&amp;A &mdash; Notes\n", "Q&A — Notes"},
	{"windows-1252", "# Caf\xe9\n", "Café"},
}

func TestMarkdownTitle(t *testing.T) {
	for _, c := range markdownCases {
		t.Run(c.name, func(t *testing.T) {
			r := strings.NewReader(c.in)
			if got := Of("notes.md", r, r.Size()); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestInline(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"**bold** and *em*", "bold and em"},
		{"2 * 3 = 6", "2 * 3 = 6"},
		{"__init__.py", "init.py"},
		{"snake_case_name", "snake_case_name"},
		{"`code`", "code"},
		{"``a`b``", "a`b"},
		{"`unclosed", "`unclosed"},
		{"[text](url)", "text"},
		{"[a [b](u1) c](u2)", "a b c"},
		{"[Guide][1]", "Guide"},
		{"![alt](img.png) Title", " Title"},
		{"[![badge](b.svg)](link) Title", " Title"},
		{"[not a link] here", "[not a link] here"},
		{"[open", "[open"},
		{`\[escaped\]`, "[escaped]"},
		{"a <br> b", "a  b"},
		{"x < y", "x < y"},
		{"&amp; &bogus; &#65;", "& &bogus; A"},
	}
	for _, c := range cases {
		if got := inline(c.in); got != c.want {
			t.Errorf("inline(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestMarkdownPathologicalFilesStayFast: every file here would cost time in
// proportion to its square if each line sent a scan to the end of the file.
func TestMarkdownPathologicalFilesStayFast(t *testing.T) {
	files := map[string]string{
		"unclosed html h1s": strings.Repeat("<h1 x\n", headLimit/6),
		"empty html h1s":    strings.Repeat("<h1></h1>\n", headLimit/10),
		"brackets":          "# " + strings.Repeat("[", headLimit),
		"backticks":         "# " + strings.Repeat("`a", headLimit/2),
		"one long para":     strings.Repeat("words ", headLimit/6) + "\n===\n",
		"empty headings":    strings.Repeat("# <x>\n", headLimit/6),
		"open quote":        "---\ntitle: \"" + strings.Repeat("x\n", headLimit/2) + "---\n",
		"comment lines":     strings.Repeat("<!--\n", headLimit/5),
	}
	for name, file := range files {
		start := time.Now()
		checkTitle(t, markdownTitle([]byte(file)))
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s: %v for %d bytes", name, d, len(file))
		}
	}
}

func FuzzMarkdownTitle(f *testing.F) {
	for _, c := range markdownCases {
		f.Add([]byte(c.in))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		checkTitle(t, markdownTitle(b))
	})
}
