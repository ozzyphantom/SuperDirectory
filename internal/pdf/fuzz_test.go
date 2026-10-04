package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// seeds are the test fixtures, whole files that reach every part of the reader.
func seeds() [][]byte {
	files := [][]byte{
		classicFile(), modernFile(), type0File(), differencesFile(), utf16TitleFile(),
		xmpFile("<< /Title () >>"), updatedFile(), spacingFile(), texFontFile(),
		encryptedFile(newRC4(2)), encryptedFile(newAESV2()), encryptedModernFile(newAES256()),
	}
	good := classicFile()
	files = append(files, good[:bytes.LastIndex(good, []byte("startxref"))])
	var p testPDF
	font := p.add(helvetica)
	form := p.next()
	p.addStream(fmt.Sprintf("/Subtype /Form /Resources << /Font << /F1 %d 0 R >> /XObject << /X %d 0 R >> >>", font, form),
		[]byte("BT /F1 9 Tf (form) Tj ET /X Do"))
	content := "q /X Do Q BI /W 1 /H 1 ID \x00 EI BT /F1 9 Tf [(a) -500 (b)] TJ ET"
	cat := p.onePage(deflate([]byte(content)), "/Filter /FlateDecode", helvetica)
	page := cat - 2
	p.objs[page-1].body = fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Resources << /Font << /F1 %d 0 R >> "+
		"/XObject << /X %d 0 R >> >> /Contents %d 0 R >>", cat-1, font, form, page-1)
	files = append(files, p.classic(fmt.Sprintf("/Root %d 0 R", cat)))
	return files
}

// resourcesFile is a page whose resources hold one font of each kind, a form that
// draws itself, and an image, for running fuzzed content streams against.
func resourcesFile() []byte {
	var p testPDF
	tu := p.addStream("", []byte(toUnicode))
	desc := p.add("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X /DW 500 /W [1 [600 700] 16 18 400] >>")
	f2 := p.add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /Encoding /Identity-V /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", desc, tu))
	f3 := p.add("<< /Type /Font /Subtype /Type3 /FontMatrix [0.01 0 0 0.01 0 0] /CharProcs << >> " +
		"/Encoding << /Differences [65 /a65 /g66 /uni0043 /f_f] >> /FirstChar 65 /LastChar 68 /Widths [50 50 50 50] >>")
	f4 := p.add("<< /Type /Font /Subtype /TrueType /BaseFont /Arial,Bold /Encoding << /BaseEncoding /MacRomanEncoding " +
		"/Differences [1 /fi /fl 200 /uni05D0 /uni05D1 /uni0301] >> >>")
	img := p.addStream("/Subtype /Image /Filter /DCTDecode", []byte("x"))
	form := p.next()
	p.addStream(fmt.Sprintf("/Subtype /Form /Matrix [2 0 0 2 10 10] /Resources << /XObject << /X %d 0 R >> >>", form),
		[]byte("BT (form) Tj ET /X Do"))
	content := p.addStream("", []byte("BT ET"))
	pages := p.next() + 1
	page := p.add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Contents %d 0 R /Resources << /Font << /F1 %s /F2 %d 0 R "+
		"/F3 %d 0 R /F4 %d 0 R >> /XObject << /X %d 0 R /I %d 0 R >> >> >>", pages, content, helvetica, f2, f3, f4, form, img))
	p.add(fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	cat := p.add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	return p.classic(fmt.Sprintf("/Root %d 0 R", cat))
}

// FuzzContent runs arbitrary content streams against real resources.
func FuzzContent(f *testing.F) {
	f.Add([]byte("BT /F1 12 Tf 72 720 Td (Hello) Tj 0 -14 Td [(W) -300 (orld)] TJ ET"))
	f.Add([]byte("BT /F2 12 Tf <0001000200110020> Tj /F3 9 Tf (ABCD) ' /F4 10 Tf (\\001 \\310\\311\\312) Tj ET"))
	f.Add([]byte("q 1 0 0 1 5 5 cm /X Do /I Do Q BI /W 1 /H 1 /L 2 ID xy EI 2 Tz 3 Ts 4 TL T* 1 2 (a) \" ET"))
	file := resourcesFile()
	f.Fuzz(func(t *testing.T, content []byte) {
		d, err := Open(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		pages, _ := d.pageList()
		out := &textOut{limit: 500}
		x := &interp{d: d, out: out}
		x.gs = gstate{ctm: identity, th: 1}
		x.tm, x.tlm = identity, identity
		x.run(content, pages[0].res, 0)
		if s := out.String(); utf8.RuneCountInString(s) > 500 || !utf8.ValidString(s) {
			t.Errorf("text over the limit or not UTF-8: %q", s)
		}
	})
}

// FuzzCMap parses arbitrary character maps and looks codes up in them.
func FuzzCMap(f *testing.F) {
	f.Add([]byte(toUnicode))
	f.Add([]byte("1 begincodespacerange <00> <ff> endcodespacerange 1 begincidrange <20> <7e> 1 endcidrange /WMode 1 def"))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, n := parseCMap(b, 1000)
		if n > 1000 {
			t.Errorf("kept %d entries past a limit of 1000", n)
		}
		for code := uint32(0); code < 300; code++ {
			c.lookup(code, 1)
			c.lookup(code, 2)
			c.cid(code, 2)
		}
	})
}

// FuzzFilters runs the decoders and predictors over arbitrary data.
func FuzzFilters(f *testing.F) {
	f.Add(deflate([]byte("hello hello hello")), uint8(2))
	f.Add([]byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01}, uint8(1))
	f.Add([]byte("<~87cURD]i,\"Ebo80~>"), uint8(0))
	f.Fuzz(func(t *testing.T, b []byte, k uint8) {
		const limit = 1 << 20
		unhexData(b)
		un85(b)
		if out, err := unrunlength(b, limit); err == nil && len(out) > limit {
			t.Errorf("RunLength gave %d bytes past its limit", len(out))
		}
		if out, err := unlzw(b, int(k&1), limit); err == nil && len(out) > limit {
			t.Errorf("LZW gave %d bytes past its limit", len(out))
		}
		inflate(b, limit)
		d := &Doc{budget: maxDecoded}
		d.unpredict(b, dict{"Predictor": int64(k%16) + 1, "Colors": int64(k%4 + 1),
			"BitsPerComponent": int64(1 << (k % 5)), "Columns": int64(k%9 + 1)})
	})
}

// TestHostileFiles checks files built to make a reader loop, recurse, or redo
// work without end. Each must finish with an error or partial results.
//
// The time bound is loose, for slow machines and the race detector, and still
// far below what an unbounded reader takes: about a minute for the nested
// objects, which grow quadratically, and forever for the forms.
func TestHostileFiles(t *testing.T) {
	const n = 60000
	files := map[string][]byte{}

	// Each object's header sits inside the previous object's string, and the page
	// tree lists them all: reading each to its end would read the file n times.
	{
		var b bytes.Buffer
		b.WriteString(header)
		offs := make([]int, n+4)
		for i := 4; i < n+4; i++ {
			offs[i] = b.Len()
			fmt.Fprintf(&b, "%d 0 obj\n(", i)
		}
		b.WriteString(strings.Repeat(")", n) + "\n")
		var kids strings.Builder
		for i := 4; i < n+4; i++ {
			fmt.Fprintf(&kids, "%d 0 R ", i)
		}
		offs[1] = b.Len()
		b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
		offs[2] = b.Len()
		fmt.Fprintf(&b, "2 0 obj\n<< /Type /Pages /Kids [%s] >>\nendobj\n", kids.String())
		xref := b.Len()
		fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", n+4)
		for i := 1; i < n+4; i++ {
			fmt.Fprintf(&b, "%010d 00000 n \n", offs[i])
		}
		fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", n+4, xref)
		files["objects nested in strings"] = b.Bytes()
	}

	// Sixteen forms, each drawing the next ten times: 10^16 runs unless bounded.
	{
		var p testPDF
		font := p.add(helvetica)
		first := p.next()
		for i := range 16 {
			body := "BT /F1 9 Tf (x) Tj ET"
			if i < 15 {
				body = strings.Repeat("/N Do ", 10)
			}
			p.addStream(fmt.Sprintf("/Subtype /Form /Resources << /Font << /F1 %d 0 R >> /XObject << /N %d 0 R >> >>",
				font, first+i+1), []byte(body))
		}
		cat := p.onePage([]byte("/N Do"), "", helvetica)
		page := cat - 2
		p.objs[page-1].body = strings.Replace(p.objs[page-1].body, "/Font", fmt.Sprintf("/XObject << /N %d 0 R >> /Font", first), 1)
		files["forms drawing forms"] = p.classic(fmt.Sprintf("/Root %d 0 R", cat))
	}

	// References that loop, a /Prev that points at itself, and nesting past
	// any sane depth.
	{
		var p testPDF
		cat := p.onePage([]byte("BT /F1 9 Tf (ok) Tj ET"), "", helvetica)
		a := p.add(fmt.Sprintf("%d 0 R", p.next()+1))
		p.add(fmt.Sprintf("%d 0 R", a))
		deep := p.add("<< /Deep " + strings.Repeat("[", 10000) + strings.Repeat("]", 10000) + " >>")
		b := p.classic(fmt.Sprintf("/Root %d 0 R /Info %d 0 R /Extra %d 0 R", cat, a, deep))
		start := bytes.LastIndex(b, []byte("startxref"))
		var xref int
		fmt.Sscan(string(b[start+len("startxref"):]), &xref)
		b = bytes.Replace(b, []byte("trailer\n<<"), []byte(fmt.Sprintf("trailer\n<< /Prev %d", xref)), 1)
		files["loops and depth"] = b
	}

	for name, b := range files {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			d, err := Open(bytes.NewReader(b), int64(len(b)))
			if err == nil {
				d.Title()
				d.Text(0)
				d.Pages()
			}
			if took := time.Since(start); took > 20*time.Second {
				t.Errorf("took %v", took)
			}
		})
	}
}

// TestCMapEdgeCases holds what fuzzing found in character maps.
func TestCMapEdgeCases(t *testing.T) {
	// A bfrange whose array form is empty maps nothing; it once indexed past
	// the start of an empty destination.
	c, _ := parseCMap([]byte("1 beginbfrange <0000> <0005> [] endbfrange"), maxCMap)
	if s, ok := c.lookup(3, 2); ok || s != "" {
		t.Errorf("empty array range: lookup = %q, %v", s, ok)
	}
}

// FuzzOpen feeds the reader arbitrary files. It must not panic, hang, or blow
// its limits, whatever the bytes.
func FuzzOpen(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := Open(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return
		}
		title := d.Title()
		if !utf8.ValidString(title) {
			t.Errorf("Title is not UTF-8: %q", title)
		}
		s, err := d.Text(200)
		if err == nil && utf8.RuneCountInString(s) > 200 {
			t.Errorf("Text(200) returned %d runes", utf8.RuneCountInString(s))
		}
		d.Pages()
	})
}
