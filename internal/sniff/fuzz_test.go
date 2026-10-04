package sniff

import (
	"bytes"
	"slices"
	"testing"
)

// refinements are the types DetectFile may name where the head showed another.
var refinements = map[string][]string{
	"zip": {"docx", "xlsx", "pptx", "odt", "ods", "odp", "odg", "epub", "jar", "apk"},
	"ole": {"doc", "xls", "ppt", "msg"},
	"mp3": {"flac", "aac"},
}

// FuzzDetect feeds Detect arbitrary heads. It must not panic, must name only
// listed types, and must leave its input alone.
func FuzzDetect(f *testing.F) {
	for _, fx := range fixtures(f) {
		f.Add(fx.data)
	}
	f.Fuzz(func(t *testing.T, head []byte) {
		orig := bytes.Clone(head)
		got := Detect(head)
		if got != "" && !slices.Contains(types, got) {
			t.Fatalf("Detect = %q, which types does not list", got)
		}
		if !bytes.Equal(head, orig) {
			t.Fatal("Detect changed its input")
		}
		if !Agrees(got, got) {
			t.Fatalf("Agrees(%q, %q) = false", got, got)
		}
	})
}

// FuzzDetectFile feeds DetectFile arbitrary files, with sizes that may overstate
// or understate them. It must not panic, must not fail to read a file in memory,
// and may only refine what the head shows: a ZIP into a document, a compound
// file into an Office format, MP3 behind an ID3 tag into FLAC or AAC.
func FuzzDetectFile(f *testing.F) {
	for _, fx := range fixtures(f) {
		f.Add(fx.data, int16(0))
	}
	docx := zipFile(f, ooxml("word/document.xml")...)
	for _, seed := range [][]byte{
		setComment(docx, "a comment"),
		join(zipFile(f, zipEntry{name: "a.txt", body: "first"}), zipFile(f, epub()...)),
		oleFile(9, chain("a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "WordDocument")),
		oleFile(9, []oleEntry{rootEntry(1), stream("Data", none, 2), stream("WordDocument", 1, 1)}),
		oleFile(9, embeddedDocument()),
	} {
		f.Add(seed, int16(0))
	}
	f.Fuzz(func(t *testing.T, data []byte, delta int16) {
		// A negative delta understates the size, as for a file that grew since it
		// was measured; a positive one overstates it. Sizes stay positive: a
		// negative one is refused before anything is read.
		size := int64(len(data))
		if delta < 0 {
			size -= int64(-int32(delta)) % (size + 1)
		} else {
			size += int64(delta)
		}
		got, err := DetectFile(bytes.NewReader(data), size)
		if err != nil {
			t.Fatalf("DetectFile: %v", err)
		}
		if got != "" && !slices.Contains(types, got) {
			t.Fatalf("DetectFile = %q, which types does not list", got)
		}

		// The head DetectFile reads, and whether it is the whole file: a file
		// shorter than its size is taken at its real length.
		want := min(size, HeadSize)
		n := min(want, int64(len(data)))
		whole := n == size || n < want
		base := detect(data[:n], whole)
		if got != base && !slices.Contains(refinements[base], got) {
			t.Fatalf("DetectFile = %q, but the head shows %q", got, base)
		}
	})
}
