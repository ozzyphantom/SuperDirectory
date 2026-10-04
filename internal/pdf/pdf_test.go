package pdf

import (
	"bytes"
	"compress/lzw"
	"encoding/ascii85"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func open(t *testing.T, b []byte) *Doc {
	t.Helper()
	d, err := Open(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return d
}

func text(t *testing.T, d *Doc) string {
	t.Helper()
	s, err := d.Text(0)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	return s
}

// The fixtures, each a whole file built in code. FuzzOpen seeds from them too.

func classicFile() []byte {
	var p testPDF
	cat := p.onePage([]byte(`BT /F1 12 Tf 72 720 Td (Hello, world!) Tj 0 -14 Td (Caf\351 cr\350me) Tj ET`), "", helvetica)
	info := p.add("<< /Title (A Plain Title) /Producer (test) >>")
	return p.classic(fmt.Sprintf("/Root %d 0 R /Info %d 0 R", cat, info))
}

func modernFile() []byte {
	var p testPDF
	font := p.pack(helvetica)
	content := p.addStream("/Filter /FlateDecode", deflate([]byte("BT /F1 11 Tf 50 750 Td (Packed objects) Tj ET")))
	pages := p.next() + 1
	page := p.pack(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, font, content))
	p.pack(fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	cat := p.pack(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	info := p.pack("<< /Title (Title From An Object Stream) >>")
	return p.modern(fmt.Sprintf("/Root %d 0 R /Info %d 0 R", cat, info))
}

const toUnicode = `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CMapName /Adobe-Identity-UCS def
/CMapType 2 def
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
7 beginbfchar
<0001> <0048>
<0002> <0065>
<0030> <0057>
<0031> <0072>
<0032> <0064>
<0040> <FB01>
<0041> <006E>
endbfchar
2 beginbfrange
<0010> <0012> <006B>
<0020> <0022> [<006F> <0020> <00F6>]
endbfrange
endcmap
CMapName currentdict /CMap defineresource pop
end
end`

// type0File shows "Hello Wörld fine" in a Type0 font with Identity-H encoding. Its
// codes resolve through bfchar entries, a bfrange counting up from k, and a
// bfrange in array form, and code 0040 maps to the fi ligature.
func type0File() []byte {
	var p testPDF
	tu := p.addStream("/Filter /FlateDecode", deflate([]byte(toUnicode)))
	desc := p.add("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /ABCDEF+Noto " +
		"/CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /DW 1000 " +
		"/W [1 [722 556] 16 18 222 32 [556 278 556] 48 [944 333 556] 64 [556 556]] /CIDToGIDMap /Identity >>")
	font := fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /ABCDEF+Noto /Encoding /Identity-H "+
		"/DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", desc, tu)
	content := "BT /F1 12 Tf 72 720 Td <0001000200110011002000210030002200310011003200210040004100020> Tj ET"
	cat := p.onePage(deflate([]byte(content)), "/Filter /FlateDecode", font)
	return p.classic(fmt.Sprintf("/Root %d 0 R", cat))
}

func differencesFile() []byte {
	var p testPDF
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /Custom /Encoding << /Type /Encoding " +
		"/BaseEncoding /WinAnsiEncoding /Differences [1 /f_i /fl /ffi 128 /Euro /quotedblleft " +
		"/quotedblright 200 /uni00E9 /u1F600 /a.sc /germandbls] >> >>"
	content := `BT /F1 12 Tf 72 720 Td (\001nd \002ow o\003ce \200 \201quote\202 caf\310 \311 \312\313) Tj ET`
	cat := p.onePage([]byte(content), "", font)
	return p.classic(fmt.Sprintf("/Root %d 0 R", cat))
}

func utf16TitleFile() []byte {
	var p testPDF
	cat := p.onePage([]byte("BT /F1 12 Tf 72 720 Td (x) Tj ET"), "", helvetica)
	info := p.add("<< /Title " + utf16Hex("\u0000 Ünïcödé 日本語 𝄞 Title\u0000 ") + " >>")
	return p.classic(fmt.Sprintf("/Root %d 0 R /Info %d 0 R", cat, info))
}

const xmp = "<?xpacket begin=\"\ufeff\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n" + `<x:xmpmeta xmlns:x="adobe:ns:meta/">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">
   <dc:format>application/pdf</dc:format>
   <dc:title>
    <rdf:Alt>
     <rdf:li xml:lang="en-US">English Title</rdf:li>
     <rdf:li xml:lang="x-default"> Title &amp; Subtitle From XMP </rdf:li>
    </rdf:Alt>
   </dc:title>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

// xmpFile has its title only in the XMP metadata; info is its Info dictionary.
func xmpFile(info string) []byte {
	var p testPDF
	md := p.addStream("/Type /Metadata /Subtype /XML", []byte(xmp))
	font := p.add(helvetica)
	content := p.addStream("", []byte("BT /F1 12 Tf 72 720 Td (Body) Tj ET"))
	pages := p.next() + 1
	page := p.add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, font, content))
	p.add(fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	cat := p.add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R /Metadata %d 0 R >>", pages, md))
	in := p.add(info)
	return p.classic(fmt.Sprintf("/Root %d 0 R /Info %d 0 R", cat, in))
}

// updatedFile carries an incremental update that rewrites the Info dictionary.
func updatedFile() []byte {
	var p testPDF
	cat := p.onePage([]byte("BT /F1 12 Tf 72 720 Td (Version one) Tj ET"), "", helvetica)
	info := p.add("<< /Title (Old Title) >>")
	trailer := fmt.Sprintf("/Root %d 0 R /Info %d 0 R", cat, info)
	base := p.classic(trailer)
	return update(base, map[int]string{info: "<< /Title (New Title) >>"}, len(p.objs)+1, trailer)
}

func spacingFile() []byte {
	var p testPDF
	content := `BT /F1 10 Tf
1 0 0 1 72 700 Tm [(Hel) -20 (lo) -300 (World)] TJ
1 0 0 1 72 686 Tm (Name:) Tj
1 0 0 1 172 686 Tm (Value) Tj
1 0 0 1 72 672 Tm (Hel) Tj
1 0 0 1 87 672 Tm (lo) Tj
0 -14 Td (Next) Tj
14 TL T* (Leading) Tj (Quote) ' 2 0 (Double) "
T* (E = mc) Tj 4 Ts (2) Tj 0 Ts
ET`
	cat := p.onePage([]byte(content), "", helvetica)
	return p.classic(fmt.Sprintf("/Root %d 0 R", cat))
}

func encryptedFile(c *testCrypt) []byte {
	var p testPDF
	p.crypt = c
	cat := p.onePage([]byte("BT /F1 12 Tf 72 720 Td (Secret text) Tj ET"), "", helvetica)
	info := p.next()
	p.add("<< /Title " + c.str(info, "Encrypted Title") + " >>")
	enc := p.add(c.dict)
	return p.classic(fmt.Sprintf("/Root %d 0 R /Info %d 0 R %s", cat, info, c.trailer(enc)))
}

// encryptedModernFile keeps its title in an encrypted object stream.
func encryptedModernFile(c *testCrypt) []byte {
	var p testPDF
	p.crypt = c
	font := p.pack(helvetica)
	content := p.addStream("/Filter /FlateDecode", deflate([]byte("BT /F1 11 Tf 50 750 Td (Packed secret) Tj ET")))
	pages := p.next() + 1
	page := p.pack(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, font, content))
	p.pack(fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	cat := p.pack(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	info := p.pack("<< /Title (Packed Encrypted Title) >>")
	enc := p.add(c.dict)
	return p.modern(fmt.Sprintf("/Root %d 0 R /Info %d 0 R %s", cat, info, c.trailer(enc)))
}

func TestClassicFile(t *testing.T) {
	d := open(t, classicFile())
	if got := d.Title(); got != "A Plain Title" {
		t.Errorf("Title = %q", got)
	}
	if got, want := text(t, d), "Hello, world!\nCafé crème"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
	if n := d.Pages(); n != 1 {
		t.Errorf("Pages = %d, want 1", n)
	}
}

func TestXrefStreamAndObjectStream(t *testing.T) {
	d := open(t, modernFile())
	if got := d.Title(); got != "Title From An Object Stream" {
		t.Errorf("Title = %q", got)
	}
	if got := text(t, d); got != "Packed objects" {
		t.Errorf("Text = %q", got)
	}
}

func TestHybridFile(t *testing.T) {
	var p testPDF
	font := p.pack(helvetica)
	content := p.addStream("", []byte("BT /F1 11 Tf 50 750 Td (Hybrid reference) Tj ET"))
	pages := p.next() + 1
	page := p.pack(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, font, content))
	p.pack(fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	cat := p.add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	info := p.pack("<< /Title (Found Through XRefStm) >>")
	d := open(t, p.hybrid(fmt.Sprintf("/Root %d 0 R /Info %d 0 R", cat, info)))
	if d.scan != nil {
		t.Error("a sound hybrid file was rebuilt by scanning")
	}
	if got := d.Title(); got != "Found Through XRefStm" {
		t.Errorf("Title = %q", got)
	}
	if got := text(t, d); got != "Hybrid reference" {
		t.Errorf("Text = %q", got)
	}
}

func TestType0IdentityH(t *testing.T) {
	d := open(t, type0File())
	if got, want := text(t, d), "Hello Wörld fine"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

func TestDifferences(t *testing.T) {
	d := open(t, differencesFile())
	if got, want := text(t, d), "find flow office € “quote” café 😀 aß"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

// texFontFile shows text in a font like TeX's Computer Modern: no /Encoding, so
// the codes mean what the embedded font program's own encoding says, where 12
// is the fi ligature and 92 and 34 are curly quotes. F2 is a Type 3 font whose
// glyphs are named for their codes, as TeX's bitmap fonts name them.
func texFontFile() []byte {
	program := "%!PS-AdobeFont-1.0: CMR10 003.002\n/FontName /CMR10 def\n/Encoding 256 array\n" +
		"0 1 255 {1 index exch /.notdef put} for\n" +
		"dup 12 /fi put\ndup 32 /space put\ndup 34 /quotedblright put\ndup 49 /one put\n" +
		"dup 50 /two put\ndup 65 /A put\ndup 92 /quotedblleft put\ndup 100 /d put\n" +
		"dup 110 /n put\ndup 123 /endash put\nreadonly def\ncurrentdict end\ncurrentfile eexec\n"
	var p testPDF
	file := p.addStream(fmt.Sprintf("/Length1 %d /Length2 8 /Length3 0", len(program)), []byte(program+"\x8e\x1f\xa0\x01\x02\x03\x04\x05"))
	desc := p.add(fmt.Sprintf("<< /Type /FontDescriptor /FontName /CMR10 /Flags 4 /FontFile %d 0 R >>", file))
	t3 := p.add("<< /Type /Font /Subtype /Type3 /FontMatrix [0.01 0 0 0.01 0 0] /FontBBox [0 0 100 100] " +
		"/CharProcs << >> /Encoding << /Differences [72 /a72 /a105] >> /FirstChar 72 /LastChar 73 /Widths [50 50] >>")
	content := `BT /F1 10 Tf 72 700 Td (\014nd \\A" 1{2) Tj /F2 10 Tf 0 -14 Td (HI) Tj ET`
	cat := p.onePage([]byte(content), "", fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /CMR10 "+
		"/FirstChar 0 /LastChar 127 /Widths [%s] /FontDescriptor %d 0 R >>", strings.TrimSpace(strings.Repeat("500 ", 128)), desc))
	page := cat - 2
	p.objs[page-1].body = strings.Replace(p.objs[page-1].body, ">> >>", fmt.Sprintf("/F2 %d 0 R >> >>", t3), 1)
	return p.classic(fmt.Sprintf("/Root %d 0 R", cat))
}

func TestBuiltinEncodings(t *testing.T) {
	d := open(t, texFontFile())
	if got, want := text(t, d), "find “A” 1–2\nHi"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

func TestUTF16Title(t *testing.T) {
	d := open(t, utf16TitleFile())
	if got, want := d.Title(), "Ünïcödé 日本語 𝄞 Title"; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
}

func TestXMPTitle(t *testing.T) {
	for _, info := range []string{"<< /Author (Someone) >>", "<< /Title () >>", "<< /Title ( \\000 ) >>"} {
		d := open(t, xmpFile(info))
		if got, want := d.Title(), "Title & Subtitle From XMP"; got != want {
			t.Errorf("Info %s: Title = %q, want %q", info, got, want)
		}
	}
	if got := xmpTitle([]byte(`<x><dc:title><rdf:Alt><rdf:li xml:lang="fr">Seul</rdf:li></rdf:Alt></dc:title></x>`)); got != "Seul" {
		t.Errorf("first rdf:li = %q, want Seul", got)
	}
}

func TestTitleStrings(t *testing.T) {
	cases := map[string]string{
		`(Annual \(2024\) Report \204 Draft\040\0612)`: "Annual (2024) Report — Draft 12",
		`<48656C6C6F2C20776F726C64>`:                   "Hello, world",
		`<EFBBBF54C3BC72>`:                             "Tür",
		`<FEFF001B656E001B0048006900>`:                 "Hi",
		`(\223nancial)`:                                "financial",
		"(Split \\\nline)":                             "Split line",
	}
	for lit, want := range cases {
		var p testPDF
		cat := p.onePage([]byte("BT ET"), "", helvetica)
		info := p.add("<< /Title " + lit + " >>")
		d := open(t, p.classic(fmt.Sprintf("/Root %d 0 R /Info %d 0 R", cat, info)))
		if got := d.Title(); got != want {
			t.Errorf("%s: Title = %q, want %q", lit, got, want)
		}
	}
}

func TestIncrementalUpdate(t *testing.T) {
	d := open(t, updatedFile())
	if got := d.Title(); got != "New Title" {
		t.Errorf("Title = %q, want the update's", got)
	}
	if got := text(t, d); got != "Version one" {
		t.Errorf("Text = %q", got)
	}
}

func TestBrokenXref(t *testing.T) {
	good := classicFile()
	start := bytes.LastIndex(good, []byte("startxref"))
	xref := bytes.LastIndex(good, []byte("\nxref")) + 1
	shifted := bytes.Replace(good, []byte("%\xe2\xe3\xcf\xd3\n"), []byte("%\xe2\xe3\xcf\xd3\n% padding\n"), 1)
	files := map[string][]byte{
		"startxref points nowhere": append(append([]byte(nil), good[:start]...), "startxref\n9\n%%EOF\n"...),
		"no startxref":             good[:start],
		"no xref table or trailer": good[:xref],
		// Every offset in the table is ten bytes short, though the table is found.
		"offsets wrong": fixStartxref(shifted),
	}
	for name, b := range files {
		t.Run(name, func(t *testing.T) {
			d := open(t, b)
			if got := d.Title(); name != "no xref table or trailer" && got != "A Plain Title" {
				t.Errorf("Title = %q", got)
			}
			if got := text(t, d); got != "Hello, world!\nCafé crème" {
				t.Errorf("Text = %q", got)
			}
		})
	}
}

// TestTruncatedFile reads a file cut off before its page tree and catalog, as
// writers put those last: the pages left behind are still read.
func TestTruncatedFile(t *testing.T) {
	var p testPDF
	cat := p.onePage([]byte("BT /F1 12 Tf 72 720 Td (Survivor) Tj ET"), "", helvetica)
	b := p.classic(fmt.Sprintf("/Root %d 0 R", cat))
	b = b[:bytes.Index(b, []byte(fmt.Sprintf("\n%d 0 obj", cat-1)))+1] // up to the page tree
	d := open(t, b)
	if got := text(t, d); got != "Survivor" {
		t.Errorf("Text = %q", got)
	}
	if n := d.Pages(); n != 1 {
		t.Errorf("Pages = %d, want 1", n)
	}
}

// fixStartxref points startxref at the file's last xref keyword.
func fixStartxref(b []byte) []byte {
	xref := bytes.LastIndex(b, []byte("\nxref")) + 1
	start := bytes.LastIndex(b, []byte("startxref"))
	return append(append([]byte(nil), b[:start]...), fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xref)...)
}

func TestBrokenXrefWithObjectStreams(t *testing.T) {
	good := modernFile()
	start := bytes.LastIndex(good, []byte("startxref"))
	xrefObj := bytes.LastIndex(good, []byte("obj\n<< /Type /XRef"))
	xrefObj = bytes.LastIndexByte(good[:xrefObj-3], '\n') + 1
	files := map[string][]byte{
		// The cross-reference stream survives; the scan uses its packed entries.
		"startxref points nowhere": append(append([]byte(nil), good[:start]...), "startxref\n9\n%%EOF\n"...),
		// Nothing indexes the object stream: the scan opens it to find the catalog.
		"no xref stream": good[:xrefObj],
	}
	for name, b := range files {
		t.Run(name, func(t *testing.T) {
			d := open(t, b)
			if got := text(t, d); got != "Packed objects" {
				t.Errorf("Text = %q", got)
			}
			if got := d.Title(); name == "startxref points nowhere" && got != "Title From An Object Stream" {
				t.Errorf("Title = %q", got)
			}
		})
	}
}

func TestEncrypted(t *testing.T) {
	cryptos := map[string]*testCrypt{
		"RC4 40-bit, revision 2":  newRC4(2),
		"RC4 128-bit, revision 3": newRC4(3),
		"AES-128, revision 4":     newAESV2(),
		"AES-256, revision 6":     newAES256(),
	}
	for name, c := range cryptos {
		t.Run(name, func(t *testing.T) {
			d := open(t, encryptedFile(c))
			if got := d.Title(); got != "Encrypted Title" {
				t.Errorf("Title = %q", got)
			}
			if got := text(t, d); got != "Secret text" {
				t.Errorf("Text = %q", got)
			}
			d = open(t, encryptedModernFile(c))
			if got := d.Title(); got != "Packed Encrypted Title" {
				t.Errorf("object streams: Title = %q", got)
			}
			if got := text(t, d); got != "Packed secret" {
				t.Errorf("object streams: Text = %q", got)
			}
		})
	}
}

func TestEncryptedNeedsPassword(t *testing.T) {
	locked := newRC4(3)
	locked.dict = strings.Replace(locked.dict, "/U <", "/U <00", 1) // no longer the empty password's
	files := map[string][]byte{
		"a user password": encryptedFile(locked),
		"only an /Encrypt entry": func() []byte {
			var p testPDF
			cat := p.onePage([]byte("BT /F1 12 Tf 72 720 Td (Hidden) Tj ET"), "", helvetica)
			info := p.add("<< /Title (Garbled) >>")
			return p.classic(fmt.Sprintf("/Root %d 0 R /Info %d 0 R /Encrypt << /Filter /Standard /V 1 /R 2 "+
				"/O (0123456789abcdef0123456789abcdef) /U (0123456789abcdef0123456789abcdef) /P -4 >>", cat, info))
		}(),
		"another handler": func() []byte {
			var p testPDF
			cat := p.onePage([]byte("BT ET"), "", helvetica)
			return p.classic(fmt.Sprintf("/Root %d 0 R /Encrypt << /Filter /Adobe.PubSec /V 4 >>", cat))
		}(),
	}
	for name, b := range files {
		d := open(t, b)
		if s, err := d.Text(0); !errors.Is(err, ErrEncrypted) || s != "" {
			t.Errorf("%s: Text = %q, %v; want ErrEncrypted", name, s, err)
		}
		if got := d.Title(); got != "" {
			t.Errorf("%s: Title = %q, want none", name, got)
		}
	}
}

func TestSpacing(t *testing.T) {
	d := open(t, spacingFile())
	want := "Hello World\nName: Value\nHello\nNext\nLeading\nQuote\nDouble\nE = mc2"
	if got := text(t, d); got != want {
		t.Errorf("Text = %q,\nwant  %q", got, want)
	}
}

func TestWidthsFromFont(t *testing.T) {
	// Glyphs 500 wide: "ab" ends 10 units on at size 10, so a run 10 units on
	// touches it and one 13 units on is a word apart.
	var p testPDF
	font := "<< /Type /Font /Subtype /TrueType /BaseFont /Arial /FirstChar 97 /LastChar 98 /Widths [500 500] >>"
	content := "BT /F1 10 Tf 1 0 0 1 0 0 Tm (ab) Tj 1 0 0 1 10 0 Tm (ab) Tj 1 0 0 1 23 0 Tm (ab) Tj ET"
	cat := p.onePage([]byte(content), "", font)
	d := open(t, p.classic(fmt.Sprintf("/Root %d 0 R", cat)))
	if got := text(t, d); got != "abab ab" {
		t.Errorf("Text = %q, want %q", got, "abab ab")
	}
}

func TestTextLimit(t *testing.T) {
	d := open(t, classicFile())
	for limit, want := range map[int]string{5: "Hello", 13: "Hello, world!", 14: "Hello, world!", 18: "Hello, world!\nCafé"} {
		got, err := d.Text(limit)
		if err != nil || got != want {
			t.Errorf("Text(%d) = %q, %v; want %q", limit, got, err, want)
		}
	}
	if got, _ := d.Text(-1); got != "Hello, world!\nCafé crème" {
		t.Errorf("Text(-1) = %q, want everything", got)
	}
}

func TestPageTree(t *testing.T) {
	var p testPDF
	font := p.add(helvetica)
	c1 := p.addStream("", []byte("BT /F1 10 Tf 0 0 Td (one) Tj ET"))
	c2 := p.addStream("", []byte("BT /F1 10 Tf 0 0 Td (two) Tj ET"))
	c3 := p.addStream("", []byte("BT /F1 10 Tf 0 0 Td (three) Tj ET"))
	root := p.next() + 4
	inner := p.next() + 3
	p1 := p.add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Contents %d 0 R >>", inner, c1))
	p2 := p.add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Contents %d 0 R >>", inner, c2))
	p3 := p.add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Contents [%d 0 R] >>", root, c3))
	// The inner node loops back to the root; the walk must not follow it.
	p.add(fmt.Sprintf("<< /Type /Pages /Parent %d 0 R /Kids [%d 0 R %d 0 R %d 0 R] /Count 2 >>", root, p1, p2, root))
	p.add(fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 3 /Resources << /Font << /F1 %d 0 R >> >> >>", inner, p3, font))
	cat := p.add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", root))
	d := open(t, p.classic(fmt.Sprintf("/Root %d 0 R", cat)))
	if n := d.Pages(); n != 3 {
		t.Errorf("Pages = %d, want 3", n)
	}
	if got := text(t, d); got != "one\ntwo\nthree" {
		t.Errorf("Text = %q", got)
	}
}

func TestFormsAndImages(t *testing.T) {
	var p testPDF
	font := p.add(helvetica)
	img := p.addStream("/Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray /Filter /DCTDecode",
		[]byte("not really a JPEG"))
	form := p.next()
	p.addStream(fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 600 800] /Matrix [1 0 0 1 0 100] "+
		"/Resources << /Font << /F1 %d 0 R >> /XObject << /Self %d 0 R /Im %d 0 R >> >>", font, form, img),
		[]byte("BT /F1 10 Tf 72 600 Td (Inside the form) Tj ET /Self Do /Im Do"))
	content := "q /Fm Do Q /Im Do BI /W 2 /H 1 /CS /G /BPC 8 ID \x00EI\xff EI\nBT /F1 10 Tf 72 600 Td (After the form) Tj ET"
	cat := p.onePage([]byte(content), "", helvetica)
	page := cat - 2
	p.objs[page-1].body = strings.Replace(p.objs[page-1].body, "/Font", fmt.Sprintf("/XObject << /Fm %d 0 R /Im %d 0 R >> /Font", form, img), 1)
	d := open(t, p.classic(fmt.Sprintf("/Root %d 0 R", cat)))
	if got := text(t, d); got != "Inside the form\nAfter the form" {
		t.Errorf("Text = %q", got)
	}
}

func TestFilters(t *testing.T) {
	var a85 bytes.Buffer
	w := ascii85.NewEncoder(&a85)
	w.Write(deflate([]byte("BT /F1 10 Tf 0 0 Td (Through two filters) Tj ET")))
	w.Close()
	var p testPDF
	cat := p.onePage(append(a85.Bytes(), "~>"...), "/Filter [/ASCII85Decode /FlateDecode]", helvetica)
	d := open(t, p.classic(fmt.Sprintf("/Root %d 0 R", cat)))
	if got := text(t, d); got != "Through two filters" {
		t.Errorf("filter array: Text = %q", got)
	}

	if got := string(unhexData([]byte("48 65 6c\n6C 6f 4>"))); got != "Hello@" {
		t.Errorf("ASCIIHex = %q", got)
	}
	for _, src := range []string{"Hello, world", "abcd", "abcde", "\x00\x00\x00\x00 zero group"} {
		enc := make([]byte, ascii85.MaxEncodedLen(len(src)))
		enc = enc[:ascii85.Encode(enc, []byte(src))]
		if got := string(un85(slices.Concat([]byte("<~"), enc, []byte("~>")))); got != src {
			t.Errorf("ASCII85 of %q = %q", src, got)
		}
	}
	if got := un85([]byte("z!!~>")); !bytes.Equal(got, []byte{0, 0, 0, 0, 0}) {
		t.Errorf("ASCII85 z = %v", got)
	}
	if got, _ := unrunlength([]byte{2, 'a', 'b', 'c', 254, 'x', 128, 'z'}, 100); string(got) != "abcxxx" {
		t.Errorf("RunLength = %q", got)
	}
}

func TestLZW(t *testing.T) {
	// The example in the PDF specification, section 7.4.4.2.
	got, err := unlzw([]byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01}, 1, 1<<20)
	if want := []byte{45, 45, 45, 45, 45, 65, 45, 45, 45, 66}; err != nil || !bytes.Equal(got, want) {
		t.Errorf("spec example = %v, %v; want %v", got, err, want)
	}

	// Long enough to widen the codes to 12 bits and fill the table.
	var in []byte
	for i := 0; len(in) < 40000; i++ {
		in = append(in, fmt.Sprintf("line %d of %d\n", i*i%977, i)...)
	}
	for _, early := range []int{0, 1} {
		out, err := unlzw(lzwEncode(in, early), early, 1<<20)
		if err != nil || !bytes.Equal(out, in) {
			t.Errorf("EarlyChange %d: round trip failed (%v)", early, err)
		}
	}
	// Go's own encoder widens late, as EarlyChange 0 does.
	var b bytes.Buffer
	lw := lzw.NewWriter(&b, lzw.MSB, 8)
	lw.Write(in)
	lw.Close()
	if out, err := unlzw(b.Bytes(), 0, 1<<20); err != nil || !bytes.Equal(out, in) {
		t.Errorf("compress/lzw output: round trip failed (%v)", err)
	}
	if _, err := unlzw(lzwEncode(in, 1), 1, 1000); err != errTooLarge {
		t.Errorf("over the limit: err = %v", err)
	}
}

// lzwEncode is a plain LZW encoder that starts with a clear code and resets the
// table when it fills.
func lzwEncode(in []byte, early int) []byte {
	var out []byte
	var acc uint32
	var nacc int
	width := 9
	emit := func(code int) {
		acc = acc<<width | uint32(code)
		nacc += width
		for nacc >= 8 {
			out = append(out, byte(acc>>(nacc-8)))
			nacc -= 8
		}
	}
	dict := map[string]int{}
	next := 258
	reset := func() {
		clear(dict)
		for i := range 256 {
			dict[string([]byte{byte(i)})] = i
		}
		next, width = 258, 9
	}
	reset()
	emit(256)
	w := ""
	for _, c := range in {
		wc := w + string(c)
		if _, ok := dict[wc]; ok {
			w = wc
			continue
		}
		emit(dict[w])
		if next < 4096 {
			dict[wc] = next
			next++
			// The decoder adds each entry one code later than this, so it widens
			// when its next code plus early reaches the power of two; here that
			// is one code on.
			if next+early > 1<<width && width < 12 {
				width++
			}
		} else {
			emit(256)
			reset()
		}
		w = string(c)
	}
	emit(dict[w])
	emit(257)
	if nacc > 0 {
		out = append(out, byte(acc<<(8-nacc)))
	}
	return out
}

func TestPredictors(t *testing.T) {
	rows := [][]byte{{10, 20, 30, 40, 50, 60}, {11, 19, 33, 40, 52, 70}, {0, 255, 1, 254, 2, 253}, {9, 9, 9, 9, 9, 9}}
	var want []byte
	for _, r := range rows {
		want = append(want, r...)
	}
	const bpp = 2 // Colors 2, 8 bits each
	var png []byte
	prev := make([]byte, 6)
	for ft, r := range rows {
		png = append(png, byte(ft+1)) // Sub, Up, Average, Paeth
		for i := range r {
			var left, ul int
			if i >= bpp {
				left, ul = int(r[i-bpp]), int(prev[i-bpp])
			}
			up := int(prev[i])
			pred := [...]int{left, up, (left + up) / 2, int(paeth(byte(left), byte(up), byte(ul)))}[ft]
			png = append(png, r[i]-byte(pred))
		}
		prev = r
	}
	d := &Doc{budget: maxDecoded}
	got, err := d.unpredict(png, dict{"Predictor": int64(15), "Colors": int64(2), "Columns": int64(3)})
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("PNG = %v, %v; want %v", got, err, want)
	}

	var tiff []byte
	for _, r := range rows {
		for i := range r {
			if i >= bpp {
				tiff = append(tiff, r[i]-r[i-bpp])
			} else {
				tiff = append(tiff, r[i])
			}
		}
	}
	got, err = d.unpredict(tiff, dict{"Predictor": int64(2), "Colors": int64(2), "Columns": int64(3)})
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("TIFF = %v, %v; want %v", got, err, want)
	}
}

func TestGlyphNames(t *testing.T) {
	cases := map[string]string{
		"A": "A", "eacute": "é", "fi": "ﬁ", "f_f_i": "ffi", "one.oldstyle": "1",
		"uni0041": "A", "uni00660069": "fi", "u1F600": "😀", "uniD800": "", "g123": "",
		"Lslash": "Ł", "zcaron": "ž", "endash": "–", "quotesinglbase": "‚",
	}
	for n, want := range cases {
		if got := glyphText(n); got != want {
			t.Errorf("glyphText(%q) = %q, want %q", n, got, want)
		}
	}
}

func TestNotPDF(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("hello"), []byte("%PDF-1.4\n%%EOF\n")} {
		if d, err := Open(bytes.NewReader(b), int64(len(b))); err == nil {
			t.Errorf("Open(%q) = %v, nil; want an error", b, d)
		}
	}
}
