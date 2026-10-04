package title

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// entry is one file in a test archive.
type entry struct{ name, body string }

// buildZip writes an archive the way office suites and e-book tools do: every
// entry deflated except mimetype, which EPUB and OpenDocument store uncompressed,
// and empty files, which have nothing to compress.
func buildZip(t testing.TB, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		method := zip.Deflate
		if e.name == "mimetype" || e.body == "" {
			method = zip.Store
		}
		f, err := w.CreateHeader(&zip.FileHeader{Name: e.name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const xmlDecl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// coreXML is a core properties part as Office writes it, holding title.
func coreXML(title string) string {
	return xmlDecl + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" ` +
		`xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<dc:title>` + title + `</dc:title><dc:creator>Oscar</dc:creator><cp:revision>2</cp:revision>` +
		`<dcterms:created xsi:type="dcterms:W3CDTF">2024-01-02T03:04:05Z</dcterms:created></cp:coreProperties>`
}

// office builds an Office Open XML package: content types, relationships, a
// main part that holds the document's text, and the core properties.
func office(t testing.TB, mainPart, mainType, mainXML, core string) []byte {
	types := xmlDecl + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/` + mainPart + `" ContentType="` + mainType + `"/>` +
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/></Types>`
	rels := xmlDecl + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="` + mainPart + `"/>` +
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`
	entries := []entry{{"[Content_Types].xml", types}, {"_rels/.rels", rels}, {mainPart, mainXML}}
	if core != "" {
		entries = append(entries, entry{"docProps/core.xml", core})
	}
	return buildZip(t, entries...)
}

func docx(t testing.TB, core string) []byte {
	return office(t, "word/document.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml",
		xmlDecl+`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Body text is not the title</w:t></w:r></w:p></w:body></w:document>`,
		core)
}

func xlsx(t testing.TB, core string) []byte {
	return office(t, "xl/workbook.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml",
		xmlDecl+`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets><sheet name="Ports" sheetId="1"/></sheets></workbook>`,
		core)
}

func pptx(t testing.TB, core string) []byte {
	return office(t, "ppt/presentation.xml", "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml",
		xmlDecl+`<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldIdLst/></p:presentation>`,
		core)
}

// metaXML is an OpenDocument meta.xml as LibreOffice writes it, holding title.
func metaXML(title string) string {
	return xmlDecl + `<office:document-meta xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0" office:version="1.3">` +
		`<office:meta><meta:generator>LibreOffice/7.6</meta:generator><dc:title>` + title + `</dc:title>` +
		`<meta:creation-date>2024-01-02T03:04:05</meta:creation-date></office:meta></office:document-meta>`
}

func odf(t testing.TB, mime, meta string) []byte {
	return buildZip(t,
		entry{"mimetype", mime},
		entry{"META-INF/manifest.xml", xmlDecl + `<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.3">` +
			`<manifest:file-entry manifest:full-path="/" manifest:media-type="` + mime + `"/></manifest:manifest>`},
		entry{"content.xml", xmlDecl + `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" ` +
			`xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"><office:body><office:text><text:p>Body text is not the title</text:p></office:text></office:body></office:document-content>`},
		entry{"meta.xml", meta},
	)
}

// containerXML points an EPUB at its package document.
func containerXML(rootfiles string) string {
	return xmlDecl + `<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles>` +
		rootfiles + `</rootfiles></container>`
}

func rootfile(path string) string {
	return `<rootfile full-path="` + path + `" media-type="application/oebps-package+xml"/>`
}

// opfXML is an EPUB 3 package document whose metadata holds the given elements.
func opfXML(metadata string) string {
	return xmlDecl + `<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">` +
		`<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="uid">urn:uuid:1</dc:identifier>` +
		metadata + `<dc:language>en</dc:language></metadata>` +
		`<manifest><item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/></manifest>` +
		`<spine><itemref idref="c1"/></spine></package>`
}

func epub(t testing.TB, container, opfPath, opf string, extra ...entry) []byte {
	entries := []entry{{"mimetype", "application/epub+zip"}, {"META-INF/container.xml", container}}
	entries = append(entries, extra...)
	return buildZip(t, append(entries,
		entry{opfPath, opf},
		entry{"OEBPS/chapter1.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter 1</title></head><body><h1>Chapter 1</h1></body></html>`},
	)...)
}

func ofBytes(name string, data []byte) string {
	return Of(name, bytes.NewReader(data), int64(len(data)))
}

func TestOfficeTitles(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"report.docx", docx(t, coreXML("Quarterly Network Report")), "Quarterly Network Report"},
		{"macro.docm", docx(t, coreXML("Macro Document")), "Macro Document"},
		{"ports.xlsx", xlsx(t, coreXML("Port Inventory")), "Port Inventory"},
		{"ports.xlsm", xlsx(t, coreXML("Port Inventory With Macros")), "Port Inventory With Macros"},
		{"plan.pptx", pptx(t, coreXML("Migration Plan")), "Migration Plan"},
		{"plan.pptm", pptx(t, coreXML("  Migration\n\tPlan  ")), "Migration Plan"},
		{"default.pptx", pptx(t, coreXML("PowerPoint Presentation")), ""},
		{"entities.docx", docx(t, coreXML("Q&amp;A &#8211; Draft")), "Q&A – Draft"},
		{"html entity.docx", docx(t, coreXML("Data&nbsp;Sheet")), "Data Sheet"},
		{"empty.docx", docx(t, strings.Replace(coreXML(""), "<dc:title></dc:title>", "<dc:title/>", 1)), ""},
		{"missing.docx", docx(t, ""), ""},
		{"nested.docx", docx(t, strings.Replace(coreXML("Hidden"), "<dc:title>Hidden</dc:title>",
			`<x:wrap xmlns:x="urn:x"><dc:title>Hidden</dc:title></x:wrap>`, 1)), ""},
		{"bom.docx", docx(t, "\xef\xbb\xbf"+coreXML("After A Byte Order Mark")), "After A Byte Order Mark"},
		{"latin1.docx", docx(t, strings.Replace(coreXML("Caf\xe9 Menu"), "UTF-8", "ISO-8859-1", 1)), "Café Menu"},
		{"utf8 spelled oddly.docx", docx(t, strings.Replace(coreXML("Spelled UTF8"), "UTF-8", "UTF8", 1)), "Spelled UTF8"},
		{"unknown charset.docx", docx(t, strings.Replace(coreXML("Never Read"), "UTF-8", "EBCDIC", 1)), ""},
	}
	for _, c := range cases {
		if got := ofBytes(c.name, c.data); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestOpenDocumentTitles(t *testing.T) {
	cases := []struct {
		name, mime, meta, want string
	}{
		{"notes.odt", "application/vnd.oasis.opendocument.text", metaXML("Field Notes"), "Field Notes"},
		{"budget.ods", "application/vnd.oasis.opendocument.spreadsheet", metaXML("Lab Budget"), "Lab Budget"},
		{"talk.odp", "application/vnd.oasis.opendocument.presentation", metaXML("Lightning Talk"), "Lightning Talk"},
		{"rack.odg", "application/vnd.oasis.opendocument.graphics", metaXML("Rack Diagram"), "Rack Diagram"},
		{"empty.odt", "application/vnd.oasis.opendocument.text", metaXML(""), ""},
		// A dc:title outside office:meta is not the document's.
		{"stray.odt", "application/vnd.oasis.opendocument.text", strings.Replace(metaXML("Hidden"),
			"<office:meta>", `<office:meta/><x:other xmlns:x="urn:x">`, 1), ""},
	}
	for _, c := range cases {
		if got := ofBytes(c.name, odf(t, c.mime, c.meta)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestEPUBTitles(t *testing.T) {
	main := `<dc:title id="t1">The Main Title</dc:title><dc:title id="t2">A Subtitle</dc:title>`
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"first title", epub(t, containerXML(rootfile("OEBPS/content.opf")), "OEBPS/content.opf", opfXML(main)), "The Main Title"},
		{"path in another case", epub(t, containerXML(rootfile("OEBPS/Content.OPF")), "OEBPS/content.opf", opfXML(main)), "The Main Title"},
		{"leading slash", epub(t, containerXML(rootfile("/OEBPS/content.opf")), "OEBPS/content.opf", opfXML(main)), "The Main Title"},
		{"package rootfile chosen", epub(t, containerXML(`<rootfile full-path="book.pdf" media-type="application/pdf"/>`+rootfile("pkg.opf")),
			"pkg.opf", opfXML(main)), "The Main Title"},
		{"untyped rootfile", epub(t, containerXML(`<rootfile full-path="pkg.opf"/>`), "pkg.opf", opfXML(main)), "The Main Title"},
		{"bare ampersand", epub(t, containerXML(rootfile("pkg.opf")), "pkg.opf", opfXML(`<dc:title>Tips & Tricks</dc:title>`)), "Tips & Tricks"},
		{"empty first title", epub(t, containerXML(rootfile("pkg.opf")), "pkg.opf",
			opfXML(`<dc:title></dc:title><dc:title>Second Is Used</dc:title>`)), "Second Is Used"},
		{"undeclared prefix", epub(t, containerXML(rootfile("pkg.opf")), "pkg.opf",
			strings.Replace(opfXML(`<dc:title>Sloppy Book</dc:title>`), ` xmlns:dc="http://purl.org/dc/elements/1.1/"`, "", 1)), "Sloppy Book"},
		{"open ebook", epub(t, containerXML(rootfile("pkg.opf")), "pkg.opf",
			xmlDecl+`<package><metadata><dc-metadata xmlns:dc="http://purl.org/dc/elements/1.0/"><dc:Title>Old Book</dc:Title></dc-metadata></metadata></package>`),
			"Old Book"},
		{"title outside metadata", epub(t, containerXML(rootfile("pkg.opf")), "pkg.opf",
			strings.Replace(opfXML(""), "<manifest>", `<dc:title xmlns:dc="http://purl.org/dc/elements/1.1/">Not Metadata</dc:title><manifest>`, 1)), ""},
		{"no container", buildZip(t, entry{"mimetype", "application/epub+zip"}, entry{"pkg.opf", opfXML(main)}), ""},
		{"missing package", epub(t, containerXML(rootfile("gone.opf")), "pkg.opf", opfXML(main)), ""},
	}
	for _, c := range cases {
		if got := ofBytes("book.epub", c.data); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestEPUBWithManyFiles: a comic or a picture book holds thousands of images. Its
// directory stays well inside the read budget.
func TestEPUBWithManyFiles(t *testing.T) {
	var images []entry
	for i := range 5000 {
		images = append(images, entry{fmt.Sprintf("OEBPS/images/page%04d.jpg", i), ""})
	}
	data := epub(t, containerXML(rootfile("OEBPS/content.opf")), "OEBPS/content.opf",
		opfXML(`<dc:title>Picture Book</dc:title>`), images...)
	if got := ofBytes("comic.epub", data); got != "Picture Book" {
		t.Errorf("got %q, want %q", got, "Picture Book")
	}
}

// TestArchivesThatAreNot: a file named like an archive can hold anything.
func TestArchivesThatAreNot(t *testing.T) {
	for _, name := range []string{"a.docx", "a.odt", "a.epub"} {
		for _, data := range [][]byte{
			nil,
			[]byte("PK\x03\x04 not really a zip"),
			bytes.Repeat([]byte("PK\x05\x06"), 100),
			[]byte("<title>An HTML page wearing the wrong extension</title>"),
		} {
			if got := ofBytes(name, data); got != "" {
				t.Errorf("%s holding %q: got %q", name, data, got)
			}
		}
	}
}

// TestDamagedArchives cuts and corrupts real archives at every few bytes. Each
// damaged file gives a clean title or none, and never a panic.
func TestDamagedArchives(t *testing.T) {
	files := map[string][]byte{
		"a.docx": docx(t, coreXML("Quarterly Network Report")),
		"a.odt":  odf(t, "application/vnd.oasis.opendocument.text", metaXML("Field Notes")),
		"a.epub": epub(t, containerXML(rootfile("OEBPS/content.opf")), "OEBPS/content.opf", opfXML(`<dc:title>The Book</dc:title>`)),
	}
	for name, data := range files {
		for i := 0; i < len(data); i += 3 {
			checkTitle(t, ofBytes(name, data[:i]))
			bad := bytes.Clone(data)
			bad[i] ^= 0xFF
			checkTitle(t, ofBytes(name, bad))
		}
	}
}

// TestZipBombStopsAtTheEntryLimit: a core properties part that inflates to 8 MiB
// of nothing before its title. Reading stops at entryLimit, and the title past it
// is never reached.
func TestZipBombStopsAtTheEntryLimit(t *testing.T) {
	core := coreXML("Past The Limit")
	core = strings.Replace(core, "<dc:title>", strings.Repeat(" ", 8<<20)+"<dc:title>", 1)
	data := docx(t, core)
	if got := ofBytes("bomb.docx", data); got != "" {
		t.Errorf("got %q from past the entry limit", got)
	}

	a := openZip(bytes.NewReader(data), int64(len(data)))
	var n int64
	a.read("docProps/core.xml", func(r io.Reader) { n, _ = io.Copy(io.Discard, r) })
	if n != entryLimit {
		t.Errorf("an entry gave %d bytes, want the limit, %d", n, entryLimit)
	}
}

// TestHostileDirectoryIsBounded: an archive whose directory runs to 1.4 MB is
// abandoned at the read budget, not read through.
func TestHostileDirectoryIsBounded(t *testing.T) {
	entries := []entry{{"docProps/core.xml", coreXML("Behind A Huge Directory")}}
	long := strings.Repeat("d", 190)
	for i := range 6000 {
		entries = append(entries, entry{fmt.Sprintf("%s%05d", long, i), ""})
	}
	data := buildZip(t, entries...)
	r := &countingReader{r: bytes.NewReader(data)}
	if got := Of("huge.docx", r, int64(len(data))); got != "" {
		t.Errorf("got %q; the directory should have been abandoned", got)
	}
	if n := r.n.Load(); n > dirBudget {
		t.Errorf("read %d bytes, budget %d", n, dirBudget)
	}
}

func TestBudget(t *testing.T) {
	b := &budget{r: strings.NewReader("0123456789"), left: 4}
	p := make([]byte, 3)
	if n, err := b.ReadAt(p, 0); n != 3 || err != nil {
		t.Fatalf("first read = %d, %v; want 3, nil", n, err)
	}
	if n, err := b.ReadAt(p, 3); n != 1 || err != errBudget {
		t.Fatalf("second read = %d, %v; want 1, errBudget", n, err)
	}
	if n, err := b.ReadAt(p, 4); n != 0 || err != errBudget {
		t.Fatalf("third read = %d, %v; want 0, errBudget", n, err)
	}
}
