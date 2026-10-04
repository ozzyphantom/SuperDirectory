package textual

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// extract runs Text over data and fails the test on an error.
func extract(t *testing.T, name string, data []byte, limit int) string {
	t.Helper()
	s, err := Text(name, bytes.NewReader(data), int64(len(data)), limit)
	if err != nil {
		t.Fatalf("Text(%q): %v", name, err)
	}
	return s
}

func utf16Bytes(s string, order binary.AppendByteOrder, bom bool) []byte {
	var b []byte
	if bom {
		b = order.AppendUint16(b, 0xFEFF)
	}
	for _, u := range utf16.Encode([]rune(s)) {
		b = order.AppendUint16(b, u)
	}
	return b
}

func TestSupported(t *testing.T) {
	for name, want := range map[string]bool{
		"notes.txt": true, "NOTES.TXT": true, "dir/readme.Md": true, "a.markdown": true,
		"page.html": true, "page.XHTML": true, "x.shtml": true, "doc.rtf": true,
		"doc.docx": true, "doc.docm": true, "doc.odt": true, "book.epub": true,
		"data.csv": true, "app.log": true, "main.go": true, "setup.cfg": true,
		".txt": false, "README": false, "data.json": false, "app.js": false,
		"site.css": false, "photo.jpg": false, "old.doc": false, "archive.zip": false,
	} {
		if got := Supported(name); got != want {
			t.Errorf("Supported(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestUnsupportedFormat(t *testing.T) {
	_, err := Text("photo.jpg", bytes.NewReader([]byte{0xFF, 0xD8}), 2, 0)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestPlainEncodings(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		want string
	}{
		{"utf-8", []byte("héllo wörld"), "héllo wörld"},
		{"utf-8 with BOM", []byte("\xEF\xBB\xBFhéllo"), "héllo"},
		{"utf-16le", utf16Bytes("héllo\r\nwörld 😀", binary.LittleEndian, true), "héllo\nwörld 😀"},
		{"utf-16be", utf16Bytes("héllo\r\nwörld 😀", binary.BigEndian, true), "héllo\nwörld 😀"},
		{"utf-16 unpaired surrogate", append(utf16Bytes("a", binary.LittleEndian, true), 0x00, 0xD8, 'b', 0), "a\uFFFDb"},
		{"utf-16 odd byte", append(utf16Bytes("ab", binary.BigEndian, true), 'c'), "ab"},
		{"windows-1252", []byte("caf\xe9 \x93quoted\x94 costs \x80" + "5 \x96 na\xefve"), "café “quoted” costs €5 – naïve"},
		{"1252 undefined bytes dropped", []byte("a\x81b\x8dc\xe9"), "abcé"},
		{"crlf and cr", []byte("one\r\ntwo\rthree\n"), "one\ntwo\nthree"},
		{"control characters", []byte("a\x00b\x07c\x1bd\fe"), "abcd\ne"},
		{"stray BOM and line separators", []byte("a\xEF\xBB\xBFb\xE2\x80\xA8c"), "ab\nc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := extract(t, "a.txt", tc.in, 0); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMarkdownAsIs(t *testing.T) {
	in := "# Title\r\n\r\nSome *text* with `code`.\r\n\r\n- item\r\n"
	want := "# Title\n\nSome *text* with `code`.\n\n- item"
	for _, name := range []string{"a.md", "a.markdown", "a.mdown"} {
		if got := extract(t, name, []byte(in), 0); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestLimit(t *testing.T) {
	in := []byte(strings.Repeat("ab\r\n", 100))
	if got := extract(t, "a.txt", in, 10); got != "ab\nab\nab\na" {
		t.Errorf("got %q, want 10 runes", got)
	}
	// The read stops partway through a three-byte character. The partial
	// sequence must not tip the file into Windows-1252.
	euros := []byte(strings.Repeat("€", 100))
	if got := extract(t, "a.txt", euros, 1); got != "€" {
		t.Errorf("got %q, want %q", got, "€")
	}
	page := []byte("<p>" + strings.Repeat("word ", 10000) + "</p>")
	if got := extract(t, "a.html", page, 50); utf8.RuneCountInString(got) > 50 {
		t.Errorf("html: %d runes, want at most 50", utf8.RuneCountInString(got))
	}
}

// farReader fails any read past max, to prove a reader stays near the front.
type farReader struct {
	r   io.ReaderAt
	max int64
}

func (f farReader) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.max {
		return 0, errors.New("read past the limit's reach")
	}
	return f.r.ReadAt(p, off)
}

func TestPlainReadsOnlyWhatTheLimitNeeds(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1<<20)
	r := farReader{bytes.NewReader(data), 4*10 + 4}
	s, err := Text("big.log", r, int64(len(data)), 10)
	if err != nil || s != "xxxxxxxxxx" {
		t.Fatalf("got %q, %v", s, err)
	}
}

func TestShortFile(t *testing.T) {
	// A file shorter than its stated size yields what it has.
	s, err := Text("a.txt", strings.NewReader("short"), 1000, 0)
	if err != nil || s != "short" {
		t.Fatalf("got %q, %v", s, err)
	}
}

func TestPDFHook(t *testing.T) {
	saved := PDF
	t.Cleanup(func() { PDF = saved })

	PDF = nil
	if Supported("a.pdf") {
		t.Error("PDF supported with no hook")
	}
	if _, err := Text("a.pdf", strings.NewReader("%PDF"), 4, 0); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no hook: err = %v, want ErrUnsupported", err)
	}

	var gotLimit int
	var gotSize int64
	PDF = func(r io.ReaderAt, size int64, limit int) (string, error) {
		gotLimit, gotSize = limit, size
		return "one\r\ntwo\x00 three  \n\n", nil
	}
	if !Supported("A.PDF") {
		t.Error("PDF unsupported with a hook")
	}
	if got := extract(t, "doc.PDF", []byte("%PDF-1.7"), 0); got != "one\ntwo three" {
		t.Errorf("got %q", got)
	}
	if gotLimit != defaultLimit || gotSize != 8 {
		t.Errorf("hook got limit %d, size %d; want %d, 8", gotLimit, gotSize, defaultLimit)
	}
	if got := extract(t, "doc.pdf", []byte("%PDF"), 3); got != "one" || gotLimit != 3 {
		t.Errorf("limit 3: got %q, hook limit %d", got, gotLimit)
	}

	PDF = func(io.ReaderAt, int64, int) (string, error) { return "", errors.New("encrypted") }
	if _, err := Text("doc.pdf", strings.NewReader("%PDF"), 4, 0); err == nil || err.Error() != "encrypted" {
		t.Errorf("hook error: got %v", err)
	}

	PDF = func(io.ReaderAt, int64, int) (string, error) { panic("malformed xref") }
	if _, err := Text("doc.pdf", strings.NewReader("%PDF"), 4, 0); err == nil || !strings.Contains(err.Error(), "malformed xref") {
		t.Errorf("a panicking reader should come back as an error, got %v", err)
	}
}
