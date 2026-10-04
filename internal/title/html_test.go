package title

import (
	"strings"
	"testing"
	"time"
)

var htmlCases = []struct{ name, in, want string }{
	{"plain", `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Network Setup Guide</title></head><body><h1>Other</h1></body></html>`, "Network Setup Guide"},
	{"entities", `<title>Tips &amp; Tricks &#8212; caf&eacute; &#x263A; &lt;b&gt;</title>`, "Tips & Tricks — café ☺ <b>"},
	{"whitespace", "<title>\n\t  Multi\n  Line\t\tTitle  \n</title>", "Multi Line Title"},
	{"uppercase tags", `<HTML><HEAD><TITLE>Upper Case</TITLE></HEAD></HTML>`, "Upper Case"},
	{"attributes", `<Title id="t" data-x='a>b' >Attr Title</TITLE >`, "Attr Title"},
	{"invisible characters", "<title>Data&nbsp;Sheet&shy;s \xe2\x80\x8bv2</title>", "Data Sheets v2"},
	{"right-to-left override", "<title>invoice&#x202E;fdp.exe</title>", "invoicefdp.exe"},
	{"controls", "<title>A\x01B\x7fC</title>", "A B C"},
	{"long title", "<title>" + strings.Repeat("word ", 60) + "</title>", strings.TrimSpace(strings.Repeat("word ", 40))},

	// A title that names nothing gives way to the first h1.
	{"placeholder title", `<title>Untitled Document</title><body><h1>Real Heading</h1>`, "Real Heading"},
	{"index title", `<title>index</title><h1>Getting Started</h1>`, "Getting Started"},
	{"numbered placeholder", `<title>New Page 1</title><h1>Release Notes</h1>`, "Release Notes"},
	{"digits only", `<title>404</title><h1>Page Not Found</h1>`, "Page Not Found"},
	{"punctuation only", `<title> -- | -- </title><h1>Wiring</h1>`, "Wiring"},
	{"empty title", `<title>   </title><h1>From Heading</h1>`, "From Heading"},
	{"self-closed title", `<title/><h1>XHTML Heading</h1>`, "XHTML Heading"},
	{"unclosed title", `<title>Never closed <h1>Rescue</h1>`, "Rescue"},
	{"no title", `<body><h1>Only Heading</h1></body>`, "Only Heading"},
	{"title after h1", `<h1>Heading</h1><title>Late Title</title>`, "Late Title"},
	{"placeholder after h1", `<h1>Heading</h1><title>Untitled</title>`, "Heading"},
	{"both placeholders", `<title>Home</title><h1>Home Page</h1>`, ""},
	{"first h1 decides", `<title>Untitled</title><h1>Index</h1><h1>Later Heading</h1>`, ""},
	{"nothing", `<p>Just text</p>`, ""},
	{"near misses", `<titles>Nope</titles><h10>No</h10><h1x>No</h1x>`, ""},

	// An h1's text, as a reader sees it.
	{"h1 nested tags", `<h1 class="title"><a href="/x"><span>Nested</span> <em>Tags</em></a></h1>`, "Nested Tags"},
	{"h1 joins inline tags", `<h1>Fire<b>wall</b> Rules</h1>`, "Firewall Rules"},
	{"h1 br separates", `<H1>Chapter 3<BR>Routing</H1>`, "Chapter 3 Routing"},
	{"h1 svg icon", `<h1><svg viewBox="0 0 8 8"><title>link icon</title><path d="M0"/></svg>Install Guide</h1>`, "Install Guide"},
	{"h1 permalink pilcrow", `<h1>Installation<a class="headerlink" href="#installation" title="Permalink to this heading">¶</a></h1>`, "Installation"},
	{"h1 entities", `<h1>Q&amp;A</h1>`, "Q&A"},
	{"h1 less-than", `<h1>a < b</h1>`, "a < b"},
	{"h1 quoted bracket", `<h1 data-x="a>b">Quoted Attr</h1>`, "Quoted Attr"},
	{"h1 ended by h2", `<h1>Unclosed Heading<h2>Next</h2>`, "Unclosed Heading"},
	{"h1 ended by a wrong end tag", `<h1>Typo Heading</h2><p>x</p>`, "Typo Heading"},
	{"empty h1 skipped", `<h1><img src="logo.png" alt="Logo"></h1><p>x</p><h1>Second Heading</h1>`, "Second Heading"},
	{"h1 script", `<h1>Title<script>var x = "<h2>";</script> Text</h1>`, "Title Text"},
	{"h1 ended by body", `<h1>Body Ends It</body>`, "Body Ends It"},
	{"unclosed h1", `<h1>Runs Off The End`, ""},
	{"h1 with unclosed svg", `<h1>Icon<svg><path/>`, ""},

	// What is not markup is not read as markup.
	{"comment", `<!-- <title>Old Title</title> --><title>New Title</title>`, "New Title"},
	{"empty comments", `<!--><!---><title>After Empty Comments</title>`, "After Empty Comments"},
	{"script", `<script>document.write("<title>Fake</title>")</script><title>Real Title</title>`, "Real Title"},
	{"svg title", `<body><svg><title>Search icon</title></svg><h1>Heading Wins</h1></body>`, "Heading Wins"},
	{"noscript", `<noscript><h1>Enable JavaScript</h1></noscript><h1>Docs Portal</h1>`, "Docs Portal"},
	{"unclosed comment", `<!-- <title>Hidden</title>`, ""},

	// Legacy encodings.
	{"windows-1252", "<title>Caf\xe9 \x93Quotes\x94</title>", "Café “Quotes”"},
	{"declared latin-1", `<meta http-equiv="Content-Type" content="text/html; charset=ISO-8859-1"><title>Gr` + "\xfc\xdf" + `e</title>`, "Grüße"},
	{"unreadable charset", `<meta charset="shift_jis"><title>` + "\x83e\x83X\x83g" + `</title><h1>Fallback</h1>`, "Fallback"},
}

func TestHTMLTitle(t *testing.T) {
	for _, c := range htmlCases {
		t.Run(c.name, func(t *testing.T) {
			r := strings.NewReader(c.in)
			if got := Of("page.html", r, r.Size()); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestHTMLTitleReadsOnlyTheHead: a title past the first 256 KiB is not found,
// and the bytes after them are never read.
func TestHTMLTitleReadsOnlyTheHead(t *testing.T) {
	page := "<html><head>" + strings.Repeat("<meta name=x content=y>", headLimit/20) +
		"<title>Too Far Down</title></head></html>"
	r := &countingReader{r: strings.NewReader(page)}
	if got := Of("big.html", r, int64(len(page))); got != "" {
		t.Errorf("found %q past the first %d bytes", got, headLimit)
	}
	if n := r.n.Load(); n > headLimit {
		t.Errorf("read %d bytes, want at most %d", n, headLimit)
	}
}

// TestHTMLPathologicalPagesStayFast: every page here would cost time in
// proportion to its square if a scan went back over what it had read.
func TestHTMLPathologicalPagesStayFast(t *testing.T) {
	pages := map[string]string{
		"empty h1s":          strings.Repeat("<h1></h1>", headLimit/9),
		"unclosed h1s":       strings.Repeat("<h1>x", headLimit/5),
		"undecodable h1s":    strings.Repeat("<h1>\x81</h1>", headLimit/10),
		"lone brackets":      strings.Repeat("<", headLimit),
		"end tag starts":     strings.Repeat("</", headLimit/2),
		"nested svg":         strings.Repeat("<svg>", headLimit/5),
		"titles":             strings.Repeat("<title>", headLimit/7),
		"open quotes":        strings.Repeat(`<a b="`, headLimit/6),
		"scripts":            strings.Repeat("<script>x</script>", headLimit/18),
		"near-miss end tags": "<script>" + strings.Repeat("</scrip", headLimit/7),
		"metas":              "<title>\xff</title>" + strings.Repeat("<meta>", headLimit/6),
	}
	for name, page := range pages {
		start := time.Now()
		checkTitle(t, htmlTitle([]byte(page)))
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s: %v for %d bytes", name, d, len(page))
		}
	}
}

func FuzzHTMLTitle(f *testing.F) {
	for _, c := range htmlCases {
		f.Add([]byte(c.in))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		checkTitle(t, htmlTitle(b))
	})
}
