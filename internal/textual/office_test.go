package textual

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

// zipped builds a ZIP archive from name, content pairs.
func zipped(t testing.TB, pairs ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i+1 < len(pairs); i += 2 {
		w, err := zw.Create(pairs[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, pairs[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const wordNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
	`xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"`

func wordDoc(body string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\r\n" +
		`<w:document ` + wordNS + `><w:body>` + body + `<w:sectPr/></w:body></w:document>`
}

func TestDOCX(t *testing.T) {
	body := `<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Install</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:tabs><w:tab w:val="left" w:pos="720"/></w:tabs></w:pPr>` +
		`<w:r><w:t xml:space="preserve">Run </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>setup</w:t></w:r>` +
		`<w:r><w:tab/><w:t>now.</w:t><w:br/><w:t>Next line</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>First item</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>Kept</w:t></w:r><w:del><w:r><w:delText>gone</w:delText></w:r></w:del>` +
		`<w:moveFrom><w:r><w:t>moved away</w:t></w:r></w:moveFrom>` +
		`<w:ins><w:r><w:t xml:space="preserve"> added</w:t></w:r></w:ins>` +
		`<w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText> PAGE </w:instrText></w:r></w:p>` +
		`<w:p><w:r><mc:AlternateContent><mc:Choice Requires="wps"><w:txbxContent><w:p><w:r><w:t>Box</w:t></w:r></w:p></w:txbxContent></mc:Choice>` +
		`<mc:Fallback><w:pict><w:txbxContent><w:p><w:r><w:t>Box</w:t></w:r></w:p></w:txbxContent></w:pict></mc:Fallback></mc:AlternateContent></w:r></w:p>` +
		`<w:tbl><w:tblPr/><w:tr><w:tc><w:p><w:r><w:t>Key</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Value</w:t></w:r></w:p></w:tc></w:tr>` +
		`<w:tr><w:tc><w:p><w:r><w:t>a</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>1</w:t></w:r></w:p><w:p><w:r><w:t>2</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
		`<w:p><w:pPr><w:pStyle w:val="Heading2"/><w:pPrChange><w:pPr><w:pStyle w:val="Title"/></w:pPr></w:pPrChange></w:pPr><w:r><w:t>Next</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>Caf&#233; &amp; more</w:t><w:noBreakHyphen/><w:t>end</w:t></w:r></w:p>`
	file := zipped(t, "[Content_Types].xml", "<Types/>", "word/document.xml", wordDoc(body))

	want := "# Install\n\n" +
		"Run setup\tnow.\nNext line\n" +
		"- First item\n" +
		"Kept added\n" +
		"Box\n" +
		"Key | Value\na | 1 2\n\n" +
		"## Next\n\n" +
		"Café & more-end"
	for _, name := range []string{"a.docx", "a.DOCM"} {
		if got := extract(t, name, file, 0); got != want {
			t.Errorf("%s: got:\n%q\nwant:\n%q", name, got, want)
		}
	}
}

func TestWordHeading(t *testing.T) {
	for style, want := range map[string]int{
		"Heading1": 1, "heading 2": 2, "Heading9": 6, "Title": 1,
		"Heading": 0, "Heading0": 0, "Heading10": 0, "Normal": 0, "": 0, "HeadingX": 0,
	} {
		if got := wordHeading(style); got != want {
			t.Errorf("wordHeading(%q) = %d, want %d", style, got, want)
		}
	}
}

func TestDOCXDamaged(t *testing.T) {
	for name, data := range map[string][]byte{
		"not a zip":       []byte("PK\x03\x04 but not really"),
		"no document.xml": zipped(t, "word/styles.xml", "<w:styles/>"),
	} {
		if _, err := Text("a.docx", bytes.NewReader(data), int64(len(data)), 0); err == nil {
			t.Errorf("%s: no error", name)
		}
	}

	// A document cut off midway still gives up the paragraphs before the cut.
	full := wordDoc(`<w:p><w:r><w:t>Survives</w:t></w:r></w:p><w:p><w:r><w:t>Lost`)
	cut := zipped(t, "word/document.xml", full[:strings.LastIndex(full, "Lost")])
	if got := extract(t, "a.docx", cut, 0); got != "Survives" {
		t.Errorf("cut document: got %q", got)
	}

	// Names written with backslashes or in another case still resolve.
	odd := zipped(t, `WORD\Document.XML`, wordDoc(`<w:p><w:r><w:t>found</w:t></w:r></w:p>`))
	if got := extract(t, "a.docx", odd, 0); got != "found" {
		t.Errorf("odd entry name: got %q", got)
	}
}

func TestZipEntryCap(t *testing.T) {
	file := zipped(t, "big", strings.Repeat(" ", maxEntry+1000))
	z, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := readEntry(entries(z), "big")
	if err != nil || len(b) != maxEntry {
		t.Fatalf("read %d bytes, %v; want %d", len(b), err, maxEntry)
	}
}

func TestODT(t *testing.T) {
	content := `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0" xmlns:svg="urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0" xmlns:dc="http://purl.org/dc/elements/1.1/">
<office:automatic-styles><text:list-style/></office:automatic-styles>
<office:body><office:text>
<text:sequence-decls><text:sequence-decl text:name="Table"/></text:sequence-decls>
<text:h text:outline-level="2">Getting   started</text:h>
<text:p>One<text:s text:c="3"/>two<text:tab/>three<text:line-break/>four</text:p>
<text:p>Body<text:note text:note-class="footnote"><text:note-citation>1</text:note-citation><text:note-body><text:p>A footnote.</text:p></text:note-body></text:note> text<office:annotation><dc:creator>Ann</dc:creator><text:p>A comment.</text:p></office:annotation>.</text:p>
<text:list><text:list-item><text:p>Alpha</text:p></text:list-item><text:list-item><text:p>Beta</text:p><text:list><text:list-item><text:p>Beta one</text:p></text:list-item></text:list></text:list-item></text:list>
<table:table><table:table-row><table:table-cell><text:p>k</text:p></table:table-cell><table:table-cell><text:p>v</text:p></table:table-cell></table:table-row></table:table>
<draw:frame><svg:title>Logo</svg:title><svg:desc>A logo</svg:desc><draw:image/></draw:frame>
<text:p>Title field: <text:title>Report</text:title></text:p>
<text:h>Untitled level</text:h>
</office:text></office:body></office:document-content>`
	file := zipped(t, "mimetype", "application/vnd.oasis.opendocument.text", "content.xml", content)

	want := "## Getting started\n\n" +
		"One   two\tthree\nfour\n" +
		"Body text.\n" +
		"- Alpha\n- Beta\n  - Beta one\n" +
		"k | v\n" +
		"Title field: Report\n\n" +
		"# Untitled level"
	if got := extract(t, "a.odt", file, 0); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func epubFile(t *testing.T, extra ...string) []byte {
	container := `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">` +
		`<rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`
	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title> A Book </dc:title><dc:creator>Someone</dc:creator></metadata>
<manifest>
<item id="ch2" href="text/ch%202.xhtml" media-type="application/xhtml+xml"/>
<item id="ch1" href="text/ch1.xhtml#start" media-type="application/xhtml+xml"/>
<item id="css" href="style.css" media-type="text/css"/>
<item id="cover" href="images/cover.jpg" media-type="image/jpeg"/>
<item id="gone" href="text/missing.xhtml" media-type="application/xhtml+xml"/>
</manifest>
<spine><itemref idref="cover"/><itemref idref="ch1"/><itemref idref="gone"/><itemref idref="nowhere"/><itemref idref="ch2"/><itemref idref="ch1"/></spine>
</package>`
	ch1 := `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>A Book</title></head>` +
		`<body><h1>One</h1><p>First chapter.</p></body></html>`
	ch2 := `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>A Book</title><style>p{}</style></head>` +
		`<body><h1>Two</h1><p>Second &amp; last.</p></body></html>`
	pairs := []string{
		"mimetype", "application/epub+zip",
		"META-INF/container.xml", container,
		"OEBPS/content.opf", opf,
		"OEBPS/text/ch1.xhtml", ch1,
		"OEBPS/text/ch 2.xhtml", ch2,
		"OEBPS/style.css", "p { margin: 0 }",
		"OEBPS/images/cover.jpg", "\xFF\xD8\xFF",
	}
	return zipped(t, append(pairs, extra...)...)
}

func TestEPUB(t *testing.T) {
	want := "# A Book\n\n# One\n\nFirst chapter.\n\n# Two\n\nSecond & last."
	if got := extract(t, "book.epub", epubFile(t), 0); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestEPUBDamaged(t *testing.T) {
	noContainer := zipped(t, "mimetype", "application/epub+zip", "OEBPS/content.opf", "<package/>")
	if _, err := Text("a.epub", bytes.NewReader(noContainer), int64(len(noContainer)), 0); err == nil {
		t.Error("no container.xml: no error")
	}
	noRoot := zipped(t, "META-INF/container.xml", "<container><rootfiles/></container>")
	if _, err := Text("a.epub", bytes.NewReader(noRoot), int64(len(noRoot)), 0); err == nil {
		t.Error("no rootfile: no error")
	}
	noPackage := zipped(t, "META-INF/container.xml",
		`<container><rootfiles><rootfile full-path="content.opf"/></rootfiles></container>`)
	if _, err := Text("a.epub", bytes.NewReader(noPackage), int64(len(noPackage)), 0); err == nil {
		t.Error("missing package document: no error")
	}
}

func TestOfficeLimit(t *testing.T) {
	body := strings.Repeat(`<w:p><w:r><w:t>paragraph text</w:t></w:r></w:p>`, 1000)
	file := zipped(t, "word/document.xml", wordDoc(body))
	if got := extract(t, "a.docx", file, 20); got != "paragraph text\nparag" {
		t.Errorf("docx: got %q", got)
	}
	if got := extract(t, "a.epub", epubFile(t), 13); got != "# A Book\n\n# O" {
		t.Errorf("epub: got %q", got)
	}
}
