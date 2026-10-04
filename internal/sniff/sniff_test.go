package sniff

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// TestDetect runs every fixture through Detect, which sees only the head: a ZIP
// or compound file is "zip" or "ole" until DetectFile looks inside.
func TestDetect(t *testing.T) {
	for _, f := range fixtures(t) {
		if got := Detect(f.data); got != f.detect {
			t.Errorf("%s: Detect = %q, want %q", f.name, got, f.detect)
		}
	}
}

// TestDetectFile runs every fixture through DetectFile, which also reads inside
// containers.
func TestDetectFile(t *testing.T) {
	for _, f := range fixtures(t) {
		got, err := DetectFile(bytes.NewReader(f.data), int64(len(f.data)))
		if err != nil {
			t.Errorf("%s: DetectFile: %v", f.name, err)
			continue
		}
		if got != f.file {
			t.Errorf("%s: DetectFile = %q, want %q", f.name, got, f.file)
		}
	}
}

// TestFixturesCoverEveryType keeps the list of types honest: every type the
// package names has a fixture that produces it, and no fixture produces a type
// outside the list.
func TestFixturesCoverEveryType(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range fixtures(t) {
		seen[f.detect], seen[f.file] = true, true
	}
	for _, typ := range types {
		if !seen[typ] {
			t.Errorf("no fixture is detected as %q", typ)
		}
	}
	for typ := range seen {
		if !slices.Contains(types, typ) {
			t.Errorf("a fixture is detected as %q, which types does not list", typ)
		}
	}
}

// TestDetectRejects holds what must not be named: data that imitates a
// signature, text that mentions one, and binary that is no format at all.
func TestDetectRejects(t *testing.T) {
	badTar := tarFile(t, tar.FormatUSTAR)
	copy(badTar[148:156], "0000001\x00")
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"pdf mentioned late in text", []byte("Notes.\nA PDF starts with %PDF-1.7 and ends with %%EOF.\n"), "txt"},
		{"pdf signature cut short", []byte("%PDF"), "txt"},
		{"MZ starting a sentence", []byte("MZ Ventures quarterly report, prepared for the board of directors in March.\n"), "txt"},
		{"BM starting a sentence", []byte("BM: the bowel movement log, kept daily since the procedure in June 2024.\n"), "txt"},
		{"ID3 starting a sentence", []byte("ID3 tags hold an MP3's title and artist.\n"), "txt"},
		{"true starting a sentence", []byte("true story: the build passed on the first try, which surprised everyone.\n"), "txt"},
		{"FWS starting a sentence", []byte("FWS: Fair Work Statement, effective from the first of July this year.\n"), "txt"},
		{"fLaC alone", []byte("fLaC"), "txt"},
		{"ftyp in text", []byte("abcdftypisom and then more text\n"), "txt"},
		{"utf-16 whose byte order mark passes for an mpeg header", utf16Text(binary.LittleEndian, "Hello"), "txt"},
		{"latin-1 text", []byte("caf\xe9 cr\xe8me br\xfbl\xe9e\n"), ""},
		{"text with a NUL", []byte("hello\x00world\n"), ""},
		{"mostly control characters", []byte("\x01\x02\x03\x04\x05\x06 abc \x0e\x0f\x10\x11"), ""},
		{"empty", nil, ""},
		{"byte order mark alone", []byte("\xef\xbb\xbf"), ""},
		{"unpaired surrogate in utf-16", []byte("\xff\xfeA\x00\x00\xdcB\x00"), ""},
		{"png signature cut short", []byte("\x89PNG\r\n"), ""},
		{"ftyp box cut short", isoFile("isom", "isom")[:11], ""},
		{"ftyp box of absurd size", join(be32(1<<20), "ftypisom", be32(0)), ""},
		{"ftyp of unknown brands", isoFile("abcd", "efgh"), ""},
		{"ebml of another doctype", ebmlFile("animation"), ""},
		{"ebml cut short", ebmlFile("webm")[:12], ""},
		{"one mpeg frame alone", join("\xff\xfb\x90\x00", zeros(413)), ""},
		{"mpeg frames that disagree", join("\xff\xfb\x90\x00", zeros(413), "\xff\xf3\x90\x00", zeros(200)), ""},
		{"mpeg header with a reserved rate", join("\xff\xfb\x9c\x00", zeros(500)), ""},
		{"one adts frame alone", adtsFrames(1), ""},
		{"cafebabe between fat and class", join("\xca\xfe\xba\xbe", be32(40), zeros(8)), ""},
		{"tar header with a bad checksum", badTar, ""},
		{"bmp with an unknown info header", join("BM", le32(58), le32(0), le32(54), le32(41), zeros(36)), ""},
		{"ico with no images", join("\x00\x00\x01\x00", le16(0), zeros(16)), ""},
		{"psd of version 3", psdFile(3), ""},
		{"elf of class 3", join("\x7fELF", 3, 1, 1, zeros(9)), ""},
		{"gzip with reserved flags", join("\x1f\x8b\x08\xe0", zeros(6)), ""},
		{"bzip2 without a block", join("BZh9", zeros(8)), ""},
		{"sfnt of 600 tables", join("\x00\x01\x00\x00", be16(600), zeros(30)), ""},
		{"woff of an unknown flavor", join("wOFF", "abcd", zeros(36)), ""},
		{"chm of version 4", join("ITSF", le32(4), zeros(8)), ""},
		{"flv with unknown flags", join("FLV\x01", 0xff, be32(9), be32(0)), ""},
		{"swf of version 0", join("FWS", 0, le32(25), zeros(17)), ""},
	}
	for _, c := range cases {
		if got := Detect(c.head); got != c.want {
			t.Errorf("%s: Detect = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDetectRandomBytes: binary that is no format at all is not named.
func TestDetectRandomBytes(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := range 200 {
		b := make([]byte, HeadSize)
		r.Read(b)
		if got := Detect(b); got != "" {
			t.Errorf("random head %d (starting % x) detected as %q", i, b[:8], got)
		}
	}
}

// TestTruncatedHeadsNeitherPanicNorInvent cuts every fixture short at every
// length up to 600 bytes. A cut file may read as another type (a PDF's first
// four bytes are text) but only as a type in the list.
func TestTruncatedHeadsNeitherPanicNorInvent(t *testing.T) {
	for _, f := range fixtures(t) {
		for n := range min(len(f.data), 600) {
			cut := f.data[:n]
			if got := Detect(cut); got != "" && !slices.Contains(types, got) {
				t.Fatalf("%s cut to %d bytes: Detect = %q, which types does not list", f.name, n, got)
			}
			got, err := DetectFile(bytes.NewReader(cut), int64(n))
			if err != nil {
				t.Fatalf("%s cut to %d bytes: DetectFile: %v", f.name, n, err)
			}
			if got != "" && !slices.Contains(types, got) {
				t.Fatalf("%s cut to %d bytes: DetectFile = %q, which types does not list", f.name, n, got)
			}
		}
	}
}

// TestJSONNeedsTheWholeFile: a prefix of JSON is not JSON, and a head that may be
// a prefix is not judged as JSON at all.
func TestJSONNeedsTheWholeFile(t *testing.T) {
	doc := []byte(`{"items": [` + strings.Repeat(`"abcdefghij", `, 400) + `"end"]}`)
	if len(doc) <= HeadSize {
		t.Fatalf("the document must outgrow the head: %d bytes", len(doc))
	}
	if got := Detect(doc); got != "txt" {
		t.Errorf("Detect of a long JSON document = %q, want txt: its head is cut", got)
	}
	got, err := DetectFile(bytes.NewReader(doc), int64(len(doc)))
	if err != nil || got != "txt" {
		t.Errorf("DetectFile of a long JSON document = %q, %v, want txt", got, err)
	}

	exact := []byte(`{"pad": "` + strings.Repeat("x", HeadSize-11) + `"}`)
	if len(exact) != HeadSize {
		t.Fatalf("exact is %d bytes, want %d", len(exact), HeadSize)
	}
	if got := Detect(exact); got != "txt" {
		t.Errorf("Detect of a HeadSize head = %q, want txt: it cannot know the file ends there", got)
	}
	got, err = DetectFile(bytes.NewReader(exact), int64(len(exact)))
	if err != nil || got != "json" {
		t.Errorf("DetectFile of a %d-byte JSON file = %q, %v, want json: it knows the size", len(exact), got, err)
	}

	if got := Detect([]byte(`{"open": [1, 2`)); got != "txt" {
		t.Errorf("Detect of invalid JSON = %q, want txt", got)
	}
}

// TestTextCutMidCharacter: a head that stops inside a multi-byte character is
// still text, but a whole file that ends inside one is not valid UTF-8.
func TestTextCutMidCharacter(t *testing.T) {
	full := []byte(strings.Repeat("日本語のテキスト、", 200)) // three bytes a character
	if HeadSize%3 == 0 {
		t.Fatal("HeadSize must end inside a three-byte character")
	}
	if got := Detect(full[:HeadSize]); got != "txt" {
		t.Errorf("Detect of a head cut mid-character = %q, want txt", got)
	}
	if got := Detect(full[:100]); got != "" {
		t.Errorf("Detect of a whole file ending mid-character = %q, want \"\"", got)
	}
	u16 := utf16Text(binary.LittleEndian, "Hello, world")
	if got := Detect(append(u16, 'x')); got != "" {
		t.Errorf("Detect of whole UTF-16 with an odd byte at the end = %q, want \"\"", got)
	}
}

// TestDetectLooksAtHeadSizeBytes: bytes past HeadSize make no difference.
func TestDetectLooksAtHeadSizeBytes(t *testing.T) {
	text := []byte(strings.Repeat("plain text line\n", HeadSize/16))
	long := append(text[:HeadSize:HeadSize], "\x00\x01\x02 binary tail"...)
	if got := Detect(long); got != "txt" {
		t.Errorf("Detect = %q, want txt: the binary tail is past HeadSize", got)
	}
}

// failing is a file whose reads fail past its first limit bytes.
type failing struct {
	data  []byte
	limit int64
}

var errDisk = errors.New("disk on fire")

func (f failing) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.limit {
		return 0, errDisk
	}
	return bytes.NewReader(f.data).ReadAt(p, off)
}

// TestDetectFileReportsReadErrors: a failing read is an error, whether it is the
// head or a container's directory that cannot be read.
func TestDetectFileReportsReadErrors(t *testing.T) {
	docx := zipFile(t, append(ooxml("word/document.xml"), zipEntry{name: "word/media/scan.bin", body: strings.Repeat("x", 3*HeadSize), store: true})...)
	doc := oleFile(12, chain("WordDocument")) // the directory starts at byte 8192
	cases := []struct {
		name string
		f    failing
	}{
		{"head", failing{docx, 0}},
		{"zip directory", failing{docx, HeadSize}},
		{"ole directory", failing{doc, HeadSize}},
	}
	for _, c := range cases {
		got, err := DetectFile(c.f, int64(len(c.f.data)))
		if !errors.Is(err, errDisk) {
			t.Errorf("%s: DetectFile = %q, %v, want the read error", c.name, got, err)
		}
	}
	if _, err := DetectFile(bytes.NewReader(docx), -1); err == nil {
		t.Error("DetectFile accepted a negative size")
	}
}

// TestDetectFileTrustsTheBytesOverTheSize: a file shorter than its stated size is
// read for what it holds, and is not an error.
func TestDetectFileTrustsTheBytesOverTheSize(t *testing.T) {
	for _, f := range fixtures(t) {
		if len(f.data) >= HeadSize {
			continue // the head is full either way, and a container's tail is not where its size says
		}
		got, err := DetectFile(bytes.NewReader(f.data), int64(len(f.data))+1000)
		if err != nil {
			t.Errorf("%s: DetectFile with a size too large: %v", f.name, err)
			continue
		}
		if got != f.file {
			t.Errorf("%s: DetectFile with a size too large = %q, want %q", f.name, got, f.file)
		}
	}
}

// counting is a file that tallies the bytes read from it.
type counting struct {
	r    io.ReaderAt
	read int64
}

func (c *counting) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.read += int64(n)
	return n, err
}

// TestDetectFileReadsLittle: telling a large OpenDocument file from a ZIP reads
// the head, the tail where the end record sits, the directory and a few bytes of
// the mimetype entry, never the content.
func TestDetectFileReadsLittle(t *testing.T) {
	big := strings.Repeat("<text:p>a paragraph of the document</text:p>\n", 20000)
	entries := append(odf("application/vnd.oasis.opendocument.text"), zipEntry{name: "Pictures/scan.png", body: big, store: true})
	data := zipFile(t, entries...)
	c := &counting{r: bytes.NewReader(data)}
	got, err := DetectFile(c, int64(len(data)))
	if err != nil || got != "odt" {
		t.Fatalf("DetectFile = %q, %v, want odt", got, err)
	}
	if limit := int64(HeadSize + zipTail + 1024 + 30 + 512); c.read > limit {
		t.Errorf("read %d bytes of a %d-byte file, want at most %d", c.read, len(data), limit)
	}
}
