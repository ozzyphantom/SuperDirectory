package sniff

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func detectFile(t *testing.T, data []byte) string {
	t.Helper()
	got, err := DetectFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("DetectFile: %v", err)
	}
	return got
}

// TestZipEntriesDecide: the rules that turn a ZIP into a document, and the near
// misses that leave it a ZIP.
func TestZipEntriesDecide(t *testing.T) {
	cases := []struct {
		name    string
		entries []zipEntry
		want    string
	}{
		{"mimetype deflated", []zipEntry{{name: "mimetype", body: "application/epub+zip"}, {name: "OEBPS/a.xhtml"}}, "zip"},
		{"mimetype not first", []zipEntry{{name: "META-INF/container.xml"}, {name: "mimetype", body: "application/epub+zip", store: true}}, "zip"},
		{"mimetype of another type", []zipEntry{{name: "mimetype", body: "application/x-krita", store: true}, {name: "maindoc.xml"}}, "zip"},
		{"mimetype with a newline", []zipEntry{{name: "mimetype", body: "application/epub+zip\n", store: true}}, "epub"},
		{"content types without a part folder", []zipEntry{{name: "[Content_Types].xml"}, {name: "visio/document.xml"}}, "zip"},
		{"a word folder without content types", []zipEntry{{name: "word/document.xml"}}, "zip"},
		{"a manifest in another case", []zipEntry{{name: "META-INF/manifest.mf"}}, "zip"},
		{"android before java", []zipEntry{{name: "META-INF/MANIFEST.MF"}, {name: "AndroidManifest.xml"}}, "apk"},
		{"ooxml before android", append(ooxml("ppt/presentation.xml"), zipEntry{name: "AndroidManifest.xml"}), "pptx"},
	}
	for _, c := range cases {
		if got := detectFile(t, zipFile(t, c.entries...)); got != c.want {
			t.Errorf("%s: DetectFile = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestZipFindsADirectoryFarFromTheHead: the decision rests on the central
// directory at the end of the file, and on entries far down it.
func TestZipFindsADirectoryFarFromTheHead(t *testing.T) {
	var entries []zipEntry
	for i := range 2000 {
		entries = append(entries, zipEntry{name: fmt.Sprintf("media/image%04d.png", i), body: "x"})
	}
	entries = append(entries, zipEntry{name: "big.bin", body: strings.Repeat("y", 100<<10), store: true})
	entries = append(entries, ooxml("xl/workbook.xml")...) // content types come last
	if got := detectFile(t, zipFile(t, entries...)); got != "xlsx" {
		t.Errorf("DetectFile = %q, want xlsx", got)
	}
}

// TestZipComment: an archive comment sits after the end record, and the record
// is still found.
func TestZipComment(t *testing.T) {
	data := zipFile(t, ooxml("word/document.xml")...)
	withComment := setComment(data, strings.Repeat("c", 60000))
	if got := detectFile(t, withComment); got != "docx" {
		t.Errorf("DetectFile with a 60000-byte comment = %q, want docx", got)
	}
}

// setComment replaces a ZIP's (empty) comment with c.
func setComment(data []byte, c string) []byte {
	i := bytes.LastIndex(data, []byte("PK\x05\x06"))
	out := append([]byte(nil), data[:i+22]...)
	binary.LittleEndian.PutUint16(out[i+20:], uint16(len(c)))
	return append(out, c...)
}

// TestZipDamage: a ZIP cut short or with lying offsets stays a ZIP, without an
// error.
func TestZipDamage(t *testing.T) {
	docx := zipFile(t, ooxml("word/document.xml")...)
	end := bytes.LastIndex(docx, []byte("PK\x05\x06"))

	cut := docx[:len(docx)-10]
	bothPastEnd := patch32(patch32(docx, end+16, 1<<30), end+12, 1<<30)
	lengthPastEnd := patch32(docx, end+12, 1<<30)
	commentPastEnd := patch16(docx, end+20, 500)

	cases := []struct {
		name string
		data []byte
	}{
		{"end record cut off", cut},
		{"directory offset and length past the end", bothPastEnd},
		{"directory length past the end", lengthPastEnd},
		{"comment longer than the file", commentPastEnd},
		{"zip64 markers", patch32(patch32(docx, end+12, 0xffffffff), end+16, 0xffffffff)},
		{"head alone", docx[:30]},
	}
	for _, c := range cases {
		if got := detectFile(t, c.data); got != "zip" {
			t.Errorf("%s: DetectFile = %q, want zip", c.name, got)
		}
	}
}

// TestZipMisplacedDirectory: when the end record's directory offset is wrong, the
// directory is looked for just before the end record, where it must be.
func TestZipMisplacedDirectory(t *testing.T) {
	for _, entries := range [][]zipEntry{ooxml("word/document.xml"), epub()} {
		data := zipFile(t, entries...)
		end := bytes.LastIndex(data, []byte("PK\x05\x06"))
		broken := patch32(data, end+16, 7)
		want := detectFile(t, data)
		if got := detectFile(t, broken); got != want {
			t.Errorf("DetectFile with a misplaced directory = %q, want %q", got, want)
		}
	}
}

// TestZipBehindAnotherZip: two archives back to back. The end record found is the
// second's, and its offsets count from where the second starts, so the directory
// and the mimetype entry are found shifted.
func TestZipBehindAnotherZip(t *testing.T) {
	first := zipFile(t, zipEntry{name: "readme.txt", body: "first"})
	second := zipFile(t, epub()...)
	if got := detectFile(t, join(first, second)); got != "epub" {
		t.Errorf("DetectFile = %q, want epub", got)
	}
}

func patch16(b []byte, off int, v uint16) []byte {
	out := append([]byte(nil), b...)
	binary.LittleEndian.PutUint16(out[off:], v)
	return out
}

func patch32(b []byte, off int, v uint32) []byte {
	out := append([]byte(nil), b...)
	binary.LittleEndian.PutUint32(out[off:], v)
	return out
}
