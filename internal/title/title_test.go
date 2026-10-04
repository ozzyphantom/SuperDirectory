package title

import (
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

// checkTitle fails when s breaks a promise Of makes about every title: valid
// UTF-8, trimmed, single-spaced, free of characters that show nothing, at most
// maxRunes long, and never a placeholder.
func checkTitle(t *testing.T, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("title %q is not valid UTF-8", s)
	}
	if c := clean(s); c != s {
		t.Fatalf("title %q is not clean: clean makes it %q", s, c)
	}
}

// countingReader counts the bytes read through it, to prove a reader kept to
// its bounds.
type countingReader struct {
	r io.ReaderAt
	n atomic.Int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.n.Add(int64(n))
	return n, err
}

func TestSupported(t *testing.T) {
	yes := []string{
		"a.html", "a.HTM", "a.xhtml", "page.shtml", "notes.md", "README.markdown", "x.mdown",
		"r.docx", "r.DOCM", "s.xlsx", "s.xlsm", "p.pptx", "p.pptm",
		"w.odt", "c.ods", "i.odp", "d.odg", "b.epub", "/deep/dir.with.dots/file.Html",
	}
	for _, name := range yes {
		if !Supported(name) {
			t.Errorf("Supported(%q) = false, want true", name)
		}
	}
	// A leading dot starts a name, as in package organize: ".md" has no extension.
	no := []string{"a.txt", "a.pdf", "README", ".md", "a.doc", "a.rtf", "a.tar.gz", "a.html.bak", ""}
	for _, name := range no {
		if Supported(name) {
			t.Errorf("Supported(%q) = true, want false", name)
		}
	}
}

func TestOfWithNothingToRead(t *testing.T) {
	page := strings.NewReader("<title>Present</title>")
	cases := []struct {
		name string
		r    io.ReaderAt
		size int64
	}{
		{"a.html", nil, 10},
		{"a.html", page, 0},
		{"a.html", page, -5},
		{"a.txt", page, page.Size()},
		{".html", page, page.Size()},
	}
	for _, c := range cases {
		if got := Of(c.name, c.r, c.size); got != "" {
			t.Errorf("Of(%q, size %d) = %q, want \"\"", c.name, c.size, got)
		}
	}
}

// TestOfToleratesAWrongSize: a file can shrink between the walk that measured it
// and the read. What is there still counts.
func TestOfToleratesAWrongSize(t *testing.T) {
	page := strings.NewReader("<title>Short File</title>")
	if got := Of("a.html", page, 1<<30); got != "Short File" {
		t.Errorf("Of = %q, want %q", got, "Short File")
	}
}

func TestPDFHook(t *testing.T) {
	saved := PDF
	t.Cleanup(func() { PDF = saved })
	doc := strings.NewReader("%PDF-1.7 not really")

	PDF = nil
	if Supported("manual.pdf") || Of("manual.pdf", doc, doc.Size()) != "" {
		t.Error("PDFs are read without a hook")
	}

	var gotSize int64
	PDF = func(r io.ReaderAt, size int64) string {
		gotSize = size
		return "  Switch\tConfiguration\n Manual "
	}
	if !Supported("MANUAL.PDF") {
		t.Error("PDFs unsupported with the hook set")
	}
	if got := Of("manual.pdf", doc, doc.Size()); got != "Switch Configuration Manual" {
		t.Errorf("Of = %q, want the hook's title, normalized", got)
	}
	if gotSize != doc.Size() {
		t.Errorf("hook saw size %d, want %d", gotSize, doc.Size())
	}

	PDF = func(io.ReaderAt, int64) string { return "Untitled" }
	if got := Of("manual.pdf", doc, doc.Size()); got != "" {
		t.Errorf("Of = %q, want the hook's placeholder dropped", got)
	}

	PDF = func(io.ReaderAt, int64) string { panic("malformed cross-reference table") }
	if got := Of("manual.pdf", doc, doc.Size()); got != "" {
		t.Errorf("Of = %q after the hook panicked, want \"\"", got)
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Network \t Setup\n\nGuide  ", "Network Setup Guide"},
		{"tab\tand\x00nul\x1bescape", "tab and nul escape"},
		{"soft\xc2\xadhyphen zero\xe2\x80\x8bwidth \xef\xbb\xbfmark", "softhyphen zerowidth mark"},
		{"evil\xe2\x80\xaetxt.exe", "eviltxt.exe"}, // a right-to-left override
		{"no\xc2\xa0break\xe3\x80\x80ideographic", "no break ideographic"},
		{"bad \xff byte \xef\xbf\xbd", "bad byte"},
		{strings.Repeat("word ", 60), strings.TrimSpace(strings.Repeat("word ", 40))},
		{strings.Repeat("é", 250), strings.Repeat("é", 200)},
		// The 200th character is an "e" whose accent is the 201st: both go.
		{strings.Repeat("a", 199) + "e\xcc\x81zz", strings.Repeat("a", 199)},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalize(c.in); got != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGeneric(t *testing.T) {
	placeholders := []string{
		"Untitled", "UNTITLED DOCUMENT", "Untitled-3", "Untitled spreadsheet", "Document1",
		"document 2", "Index", "home page", "Homepage", "Home", "Page 2", "New Page 1",
		"new document", "Default", "PowerPoint Presentation", "404", "1.2.3", "-- | --", "™", "",
	}
	for _, s := range placeholders {
		if !generic(s) {
			t.Errorf("generic(%q) = false, want true", s)
		}
	}
	titles := []string{
		"Untitled Goose Game Manual", "Home Network Setup", "Index of /pub/docs", "3D Printing",
		"Page Layout Guide", "C#", "日本語のタイトル", "Document Control Procedures", "v2",
	}
	for _, s := range titles {
		if generic(s) {
			t.Errorf("generic(%q) = true, want false", s)
		}
	}
}

func TestDecode(t *testing.T) {
	cases := []struct{ in, charset, want string }{
		{"plain ascii", "", "plain ascii"},
		{"caf\xc3\xa9", "windows-1252", "café"}, // valid UTF-8 wins over the label
		{"caf\xe9", "", "café"},
		{"\x93quoted\x94 \x80 \x85", "", "“quoted” € …"},
		{"Gr\xfc\xdfe", "ISO-8859-1", "Grüße"},
		{"caf\xe9", ` "UTF-8" `, "café"}, // Latin-1 labelled UTF-8, as old servers do
		{"\x83e\x83X", "shift_jis", ""},  // a charset this package cannot read
	}
	for _, c := range cases {
		if got := decode([]byte(c.in), c.charset); got != c.want {
			t.Errorf("decode(%q, %q) = %q, want %q", c.in, c.charset, got, c.want)
		}
	}
}

// TestHeadDropsAHalfCharacter: a character cut in two by the head's end is not
// UTF-8, and would make the whole head read as Windows-1252.
func TestHeadDropsAHalfCharacter(t *testing.T) {
	data := strings.Repeat("a", headLimit-1) + "€tail"
	b := head(strings.NewReader(data), int64(len(data)))
	if len(b) != headLimit-1 || !utf8.Valid(b) {
		t.Errorf("head is %d bytes, valid %v; want %d valid bytes", len(b), utf8.Valid(b), headLimit-1)
	}
	short := "abc€"
	if b := head(strings.NewReader(short), int64(len(short))); string(b) != short {
		t.Errorf("head of a short file = %q, want all of it", b)
	}
}

func TestShorten(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"Network Setup Guide", 13, "Network Setup"}, // the cut falls on a space
		{"Network Setup Guide", 15, "Network Setup"},
		{"Network Setup Guide", 4, "Netw"}, // no space in reach: cut inside the word
		{"日本語のタイトル", 10, "日本語"},            // never half a character
		// A family emoji is one picture made of three, joined: it stays whole or goes.
		{"Team \xf0\x9f\x91\xa8\xe2\x80\x8d\xf0\x9f\x91\xa9\xe2\x80\x8d\xf0\x9f\x91\xa7 Notes", 12, "Team"},
		{"short", 10, "short"},
	}
	for _, c := range cases {
		if got := shorten(c.in, c.n); got != c.want {
			t.Errorf("shorten(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestFileNameTitlesBecomePlainTitles(t *testing.T) {
	cases := map[string]string{
		"Microsoft Word - VLAN guide.docx":         "VLAN guide",
		"Microsoft PowerPoint - Q3 Review.pptx":    "Q3 Review",
		`C:\Users\bob\Desktop\Wiring Diagram.doc`:  "Wiring Diagram",
		"/Users/bob/Documents/Install Notes.pages": "Install Notes",
		"report.PDF":                 "report",
		"TCP/IP Basics":              "TCP/IP Basics",
		"Node.js Guide":              "Node.js Guide",
		"Release 2.0":                "Release 2.0",
		"Microsoft Word - Document1": "",
		"untitled.pdf":               "",
	}
	for in, want := range cases {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMetadataTitlesThatAreFileNames(t *testing.T) {
	old := PDF
	t.Cleanup(func() { PDF = old })
	for title, want := range map[string]string{
		"DMTB_View-Diagram":         "",
		"Setup_Guide.docx":          "",
		"Quick start_guide":         "Quick start_guide",
		"Code Review of the Go TUF": "Code Review of the Go TUF",
	} {
		PDF = func(io.ReaderAt, int64) string { return title }
		if got := Of("x.pdf", strings.NewReader("%PDF"), 4); got != want {
			t.Errorf("PDF titled %q: got %q, want %q", title, got, want)
		}
	}
	md := "# my_module_name\n"
	if got := Of("README.md", strings.NewReader(md), int64(len(md))); got != "my_module_name" {
		t.Errorf("a Markdown heading was judged as metadata: %q", got)
	}
}
