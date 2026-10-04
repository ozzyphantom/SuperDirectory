package sniff

import (
	"bytes"
	"io"
	"testing"
)

// embeddedDocument is a workbook holding a Word document as an embedded object,
// whose streams sit in a storage two levels down.
func embeddedDocument() []oleEntry {
	return []oleEntry{
		rootEntry(1),
		stream("Workbook", none, 2),
		storage("ObjectPool", none, none, 3),
		storage("_1234567890", none, none, 4),
		stream("WordDocument", none, none),
	}
}

// TestOLEOnlyRootChildrenCount: streams inside a storage belong to an embedded
// object, not to the file.
func TestOLEOnlyRootChildrenCount(t *testing.T) {
	embedded := embeddedDocument()
	if got := detectFile(t, oleFile(9, embedded)); got != "xls" {
		t.Errorf("a workbook with an embedded document: DetectFile = %q, want xls", got)
	}
	embedded[1].name = "Contents"
	if got := detectFile(t, oleFile(9, embedded)); got != "ole" {
		t.Errorf("a file whose only document is embedded: DetectFile = %q, want ole", got)
	}
}

// TestOLELoops: a directory whose siblings point back at one another is walked
// once, and what was seen before the loop still counts.
func TestOLELoops(t *testing.T) {
	cases := []struct {
		name    string
		entries []oleEntry
		want    string
	}{
		{"two siblings in a loop", []oleEntry{rootEntry(1), stream("Data", none, 2), stream("Contents", none, 1)}, "ole"},
		{"an entry its own sibling", []oleEntry{rootEntry(1), stream("Data", 1, 1)}, "ole"},
		{"a loop through the document", []oleEntry{rootEntry(1), stream("Data", none, 2), stream("WordDocument", 1, 1)}, "doc"},
		{"a sibling pointing at the root", []oleEntry{rootEntry(1), stream("Data", 0, 0)}, "ole"},
		{"a child past the cap", []oleEntry{rootEntry(5000)}, "ole"},
		{"an unallocated entry in the tree", []oleEntry{rootEntry(1), {name: "WordDocument", typ: 0, left: none, right: none, child: none}}, "ole"},
		{"a name in another case", chain("worddocument"), "doc"},
		{"a name outside ASCII", chain("Wörkbook"), "ole"},
	}
	for _, c := range cases {
		if got := detectFile(t, oleFile(9, c.entries)); got != c.want {
			t.Errorf("%s: DetectFile = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestOLEDirectoryAcrossSectors: entries past the first directory sector are
// reached through the FAT, and a FAT chain that loops is cut.
func TestOLEDirectoryAcrossSectors(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "WordDocument"} // entry 11: the third sector
	data := oleFile(9, chain(names...))
	if got := detectFile(t, data); got != "doc" {
		t.Errorf("DetectFile = %q, want doc", got)
	}
	looped := append([]byte(nil), data...)
	put32(looped, 512+4*2, 1) // sector 2 leads back to sector 1
	if got := detectFile(t, looped); got != "ole" {
		t.Errorf("with the directory's chain looping before its third sector: DetectFile = %q, want ole", got)
	}
}

// sparse is a file of zeros with a few regions written, to test structures that
// sit megabytes apart without allocating the megabytes.
type sparse struct {
	size  int64
	parts map[int64][]byte
}

func (s sparse) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= s.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), s.size-off))
	clear(p[:n])
	for at, b := range s.parts {
		lo, hi := max(at, off), min(at+int64(len(b)), off+int64(n))
		if lo < hi {
			copy(p[lo-off:hi-off], b[lo-at:hi-at])
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// TestOLEFATBeyondTheHeader: a directory far into a large file is chained by a
// FAT sector that the header does not list; a DIFAT sector does. The file is
// 7 MiB, almost all of it zeros.
func TestOLEFATBeyondTheHeader(t *testing.T) {
	const (
		d1 = 109*128 + 5 // the directory's first sector: its FAT entry is in FAT sector 109
		d2 = d1 + 1
	)
	difat := fill(512, freeSect)
	put32(difat, 0, 2) // FAT sector 109 is sector 2
	put32(difat, 508, endOfChain)
	fat := fill(512, freeSect)
	put32(fat, 4*(d1%128), d2)
	put32(fat, 4*(d2%128), endOfChain)
	unused := oleEntry{left: none, right: none, child: none}.bytes()

	f := sparse{size: (d2 + 2) * 512, parts: map[int64][]byte{
		0:              oleHeader(9, d1, 1, freeSect),
		(1 + 1) * 512:  difat,
		(2 + 1) * 512:  fat,
		(d1 + 1) * 512: join(rootEntry(4).bytes(), unused, unused, unused),
		(d2 + 1) * 512: stream("WordDocument", none, none).bytes(),
	}}
	got, err := DetectFile(f, f.size)
	if err != nil || got != "doc" {
		t.Errorf("DetectFile = %q, %v, want doc", got, err)
	}

	// Without the DIFAT sector, the directory's second sector is out of reach.
	f.parts[0] = oleHeader(9, d1, endOfChain, freeSect)
	got, err = DetectFile(f, f.size)
	if err != nil || got != "ole" {
		t.Errorf("without the DIFAT: DetectFile = %q, %v, want ole", got, err)
	}
}

// TestOLEDamage: a compound file too damaged to read stays "ole", without an
// error.
func TestOLEDamage(t *testing.T) {
	doc := oleFile(9, chain("WordDocument"))
	cases := []struct {
		name string
		data []byte
	}{
		{"header alone", doc[:512]},
		{"header cut short", doc[:100]},
		{"big-endian byte order mark", patched(doc, 0x1c, 0xfeff)},
		{"sectors of 1 KiB", patched(doc, 0x1e, 10)},
		{"directory past the end", patched32(doc, 0x30, 1000)},
		{"directory at a marker", patched32(doc, 0x30, endOfChain)},
		{"root that is a stream", func() []byte { b := bytes.Clone(doc); b[1024+0x42] = 2; return b }()},
	}
	for _, c := range cases {
		if got := detectFile(t, c.data); got != "ole" {
			t.Errorf("%s: DetectFile = %q, want ole", c.name, got)
		}
	}
}

func patched(b []byte, off int, v uint16) []byte {
	out := bytes.Clone(b)
	put16(out, off, v)
	return out
}

func patched32(b []byte, off int, v uint32) []byte {
	out := bytes.Clone(b)
	put32(out, off, v)
	return out
}
