package chm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
)

// sample is a small help file: compressed and stored pages, an image, an
// empty page, folders, and the archive's own bookkeeping, which Entries hides.
func sample() []testEntry {
	return []testEntry{
		{name: "/", section: 0},
		{name: "/#IDXHDR", section: 0, data: []byte("index header")},
		{name: "/#SYSTEM", section: 0, data: []byte("system")},
		{name: "/$FIftiMain", section: 1, data: textData(5000, 1)},
		{name: "/$WWKeywordLinks/", section: 0},
		{name: "/$WWKeywordLinks/BTree", section: 0, data: []byte("btree")},
		{name: "/html/", section: 0},
		{name: "/html/index.htm", section: 1, data: textData(50_000, 2)},
		{name: "/html/setup.htm", section: 1, data: textData(120_000, 3)},
		{name: "/images/logo.gif", section: 0, data: noise(3000, 4)},
		{name: "/html/empty.htm", section: 1},
		{name: "/notes.txt", section: 0, data: []byte("plain text")},
	}
}

// userFiles is what Entries should list for entries: archive order, without
// the leading "/", the folders and the bookkeeping.
func userFiles(entries []testEntry) []testEntry {
	var out []testEntry
	for _, e := range entries {
		n := strings.TrimPrefix(e.name, "/")
		if n == "" || strings.HasSuffix(n, "/") || strings.HasPrefix(n, "#") || strings.HasPrefix(n, "$") || strings.HasPrefix(n, "::") {
			continue
		}
		out = append(out, testEntry{name: n, section: e.section, data: e.data})
	}
	return out
}

func checkAll(t *testing.T, f *File, entries []testEntry) {
	t.Helper()
	want := userFiles(entries)
	got := f.Entries()
	if len(got) != len(want) {
		t.Fatalf("Entries lists %d files, want %d: %v", len(got), len(want), got)
	}
	for i, e := range want {
		if got[i].Name != e.name || got[i].Size != int64(len(e.data)) {
			t.Fatalf("entry %d is %q (%d bytes), want %q (%d bytes)", i, got[i].Name, got[i].Size, e.name, len(e.data))
		}
		b, err := f.ReadFile(e.name)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", e.name, err)
		}
		if !bytes.Equal(b, e.data) {
			t.Fatalf("ReadFile(%q) differs at byte %d", e.name, firstDiff(b, e.data))
		}
	}
}

func TestEntriesAndReadFile(t *testing.T) {
	entries := sample()
	f := openBuilt(t, buildCHM(t, chmSpec{}, entries))
	checkAll(t, f, entries)

	for _, name := range []string{"#SYSTEM", "/html/index.htm", "html", "html/", "::DataSpace/NameList", "missing.htm"} {
		if _, err := f.ReadFile(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadFile(%q) = %v, want fs.ErrNotExist", name, err)
		}
	}
	if b, err := f.ReadFile("html/empty.htm"); err != nil || b == nil || len(b) != 0 {
		t.Errorf("an empty entry reads as %v, %v; want empty and no error", b, err)
	}
}

// TestArchiveLayouts reads the same files from every container layout the
// reader must handle.
func TestArchiveLayouts(t *testing.T) {
	entries := sample()
	for i := range 80 { // enough entries to fill several small chunks
		entries = append(entries, testEntry{name: fmt.Sprintf("/pages/p%02d.htm", i), section: 1 - i%2, data: textData(500+i*91, uint64(i))})
	}
	for name, spec := range map[string]chmSpec{
		"version 3":                    {},
		"version 2":                    {version: 2},
		"small chunks":                 {chunkSize: 256},
		"small chunks, version 2":      {chunkSize: 256, version: 2},
		"with an index chunk":          {chunkSize: 1024, index: true},
		"chunks stored out of order":   {chunkSize: 256, scramble: true},
		"window 2^15, reset per frame": {enc: encOptions{windowBits: 15, resetFrames: 1, mode: modeVerbatim}},
		"window 2^21, 4-frame resets":  {enc: encOptions{windowBits: 21, resetFrames: 4, mode: modeAligned}},
		"uncompressed blocks":          {enc: encOptions{mode: modeUncompressed}},
		"mixed blocks":                 {enc: encOptions{mode: modeMixed, resetFrames: 3}},
	} {
		t.Run(name, func(t *testing.T) {
			b := buildCHM(t, spec, entries)
			if spec.chunkSize == 256 && b.numChunks < 3 {
				t.Fatalf("only %d chunks; the test wants several", b.numChunks)
			}
			checkAll(t, openBuilt(t, b), entries)
		})
	}
}

// TestReadAcrossResetBoundary reads, before anything else, a file that starts
// in the middle of a reset interval and ends in the next. Decoding must start
// at the reset point before the file, not at the start of the stream, so the
// file still reads when the compressed data before that point is destroyed.
func TestReadAcrossResetBoundary(t *testing.T) {
	first := textData(100_000, 1) // frames 0 to 3
	second := textData(80_000, 2) // from 100,000, in frame 3, to 180,000, in frame 5
	entries := []testEntry{
		{name: "/first.htm", section: 1, data: first},
		{name: "/second.htm", section: 1, data: second},
	}
	b := buildCHM(t, chmSpec{enc: encOptions{resetFrames: 2, mode: modeMixed}}, entries)
	// Ruin the first reset interval, frames 0 and 1.
	for i := b.content; i < b.content+int(b.frames[2]); i++ {
		b.data[i] = 0xFF
	}
	f := openBuilt(t, b)
	got, err := f.ReadFile("second.htm")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Fatalf("differs at byte %d", firstDiff(got, second))
	}
	if n := f.lzx.decoded; n != 4 {
		t.Errorf("decoded %d frames; frames 2 to 5 are 4", n)
	}
	if _, err := f.ReadFile("first.htm"); err == nil {
		t.Error("read the file whose compressed data was destroyed")
	}
}

// TestReadsResume: files read in stream order decode each frame once. A read
// further back starts again at the reset point before it.
func TestReadsResume(t *testing.T) {
	var entries []testEntry
	for i := range 40 {
		entries = append(entries, testEntry{name: fmt.Sprintf("/p%02d.htm", i), section: 1, data: textData(7000+i*13, uint64(i))})
	}
	b := buildCHM(t, chmSpec{enc: encOptions{resetFrames: 2}}, entries)
	f := openBuilt(t, b)
	checkAll(t, f, entries)
	frames := int64(len(b.frames))
	if f.lzx.decoded != frames {
		t.Errorf("decoded %d frames reading front to back; the stream has %d", f.lzx.decoded, frames)
	}
	before := f.lzx.decoded
	if _, err := f.ReadFile("p00.htm"); err != nil {
		t.Fatal(err)
	}
	if n := f.lzx.decoded - before; n != 1 {
		t.Errorf("going back to the first file decoded %d frames, want 1", n)
	}
}

// TestResetTableFallback: with no usable reset table entry, chmd.c decodes
// from the start of the stream, taking the length from SpanInfo if the table
// itself is unusable. So does this reader.
func TestResetTableFallback(t *testing.T) {
	entries := sample()
	t.Run("no entries", func(t *testing.T) {
		b := buildCHM(t, chmSpec{tableFrames: -1}, entries)
		f := openBuilt(t, b)
		if _, err := f.ReadFile("html/setup.htm"); err != nil {
			t.Fatal(err)
		}
		if f.lzx.decoded != int64(len(b.frames)) {
			t.Errorf("decoded %d frames; from the start, setup.htm needs all %d", f.lzx.decoded, len(b.frames))
		}
		checkAll(t, f, entries)
	})
	t.Run("only the first entry", func(t *testing.T) {
		checkAll(t, openBuilt(t, buildCHM(t, chmSpec{tableFrames: 1}, entries)), entries)
	})
	t.Run("bad frame length", func(t *testing.T) {
		b := buildCHM(t, chmSpec{}, entries)
		binary.LittleEndian.PutUint64(b.data[b.at[resetName]+0x20:], 0x10000)
		checkAll(t, openBuilt(t, b), entries)
	})
	t.Run("no reset table", func(t *testing.T) {
		b := buildCHM(t, chmSpec{}, entries)
		rename(t, b.data, resetName)
		checkAll(t, openBuilt(t, b), entries)
	})
	t.Run("neither table nor span info", func(t *testing.T) {
		b := buildCHM(t, chmSpec{}, entries)
		rename(t, b.data, resetName)
		rename(t, b.data, spanInfoName)
		f := openBuilt(t, b)
		if _, err := f.ReadFile("html/index.htm"); err == nil {
			t.Error("read a compressed file with no way to know the stream's length")
		}
		if _, err := f.ReadFile("notes.txt"); err != nil {
			t.Errorf("a stored file should still read: %v", err)
		}
	})
}

// rename changes the last letter of a directory entry's name, so the reader
// no longer finds it.
func rename(t *testing.T, data []byte, name string) {
	t.Helper()
	i := bytes.Index(data, []byte(name))
	if i < 0 {
		t.Fatalf("no %s in the directory", name)
	}
	data[i+len(name)-1] ^= 0x20
}

// TestCompressedSectionErrors breaks each file that describes the compressed
// section. Compressed entries must then fail to read, with an error; stored
// entries must still read.
func TestCompressedSectionErrors(t *testing.T) {
	entries := sample()
	put32 := func(name string, off int, v uint32) func(b built) {
		return func(b built) { binary.LittleEndian.PutUint32(b.data[b.at[name]+off:], v) }
	}
	cases := map[string]struct {
		spec  chmSpec
		patch func(b built)
	}{
		"section 1 named otherwise":    {spec: chmSpec{nameList: []string{"Uncompressed", "Bogus"}}},
		"NameList with one section":    {spec: chmSpec{nameList: []string{"Uncompressed"}}},
		"no LZXC signature":            {patch: put32(controlName, 4, 0x41414141)},
		"ControlData version 3":        {patch: put32(controlName, 8, 3)},
		"window not a power of two":    {patch: put32(controlName, 0x10, 3)},
		"window too large":             {patch: put32(controlName, 0x10, 128)},
		"reset interval of zero":       {patch: put32(controlName, 0x0C, 0)},
		"no ControlData":               {patch: func(b built) { rename(t, b.data, controlName) }},
		"no Content":                   {patch: func(b built) { rename(t, b.data, contentName) }},
		"reset point past the content": {patch: put32(resetName, 0x28, 0x7FFFFFF0)}, // frame 0, where setup.htm's read starts
		"stream longer than allowed": {patch: func(b built) {
			binary.LittleEndian.PutUint64(b.data[b.at[resetName]+0x10:], 1<<40)
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := buildCHM(t, c.spec, entries)
			if c.patch != nil {
				c.patch(b)
			}
			f := openBuilt(t, b)
			if _, err := f.ReadFile("html/setup.htm"); err == nil {
				t.Error("read a compressed file")
			}
			if got, err := f.ReadFile("images/logo.gif"); err != nil || !bytes.Equal(got, entries[9].data) {
				t.Errorf("a stored file should still read: %v", err)
			}
		})
	}
}

// TestOpenRejects breaks the container in each way the reader checks.
func TestOpenRejects(t *testing.T) {
	good := buildCHM(t, chmSpec{chunkSize: 1024, index: true}, sample())
	itsp := good.chunks - itspLen
	patch := func(off int, v uint32) []byte {
		b := bytes.Clone(good.data)
		binary.LittleEndian.PutUint32(b[off:], v)
		return b
	}
	longName := buildCHM(t, chmSpec{}, []testEntry{{name: "/" + strings.Repeat("n", 1100), section: 0, data: []byte("x")}})
	cases := map[string][]byte{
		"empty":                       nil,
		"short":                       good.data[:0x40],
		"not ITSF":                    patch(0, 0x46535458),
		"version 1":                   patch(4, 1),
		"version 4":                   patch(4, 4),
		"directory past the end":      patch(0x48, uint32(len(good.data))),
		"content past the end":        patch(0x58, uint32(len(good.data)+1)),
		"no ITSP":                     patch(itsp, 0),
		"directory header of 0x60":    patch(itsp+8, 0x60),
		"chunks of 0 bytes":           patch(itsp+0x10, 0),
		"chunks of 15 bytes":          patch(itsp+0x10, 15),
		"chunks of 9000 bytes":        patch(itsp+0x10, 9000),
		"no chunks":                   patch(itsp+0x2C, 0),
		"200,000 chunks":              patch(itsp+0x2C, 200_000),
		"more chunks than the file":   patch(itsp+0x2C, 5000),
		"no listing chunk":            patch(itsp+0x20, noChunk),
		"first chunk out of range":    patch(itsp+0x20, 99),
		"first chunk is the index":    patch(itsp+0x20, uint32(good.numChunks-1)),
		"chunk linked to itself":      patch(good.chunks+0x10, 0),
		"chunk linked out of range":   patch(good.chunks+0x10, 1000),
		"more entries than the chunk": patch(good.chunks+good.chunkSize-4, 0xFFFF0000),
		"endless ENCINT": func() []byte {
			b := bytes.Clone(good.data)
			for i := range 10 {
				b[good.chunks+pmglHeader+i] = 0x80
			}
			return b
		}(),
		"name of 1,100 bytes": longName.data,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Open(bytes.NewReader(data), int64(len(data))); err == nil {
				t.Fatal("opened")
			}
		})
	}
	t.Run("more entries than allowed", func(t *testing.T) {
		lim := defaultLimits
		lim.entries = 5
		if _, err := open(bytes.NewReader(good.data), int64(len(good.data)), lim); err == nil {
			t.Fatal("opened")
		}
	})
}

// TestTruncated cuts an archive at many points. Open may fail, and reads may
// fail, but nothing may panic, and what does read must be right.
func TestTruncated(t *testing.T) {
	entries := sample()
	data := buildCHM(t, chmSpec{}, entries).data
	want := map[string][]byte{}
	for _, e := range userFiles(entries) {
		want[e.name] = e.data
	}
	for n := 0; n < len(data); n += 997 {
		f, err := Open(bytes.NewReader(data[:n]), int64(n))
		if err != nil {
			continue
		}
		for _, e := range f.Entries() {
			if got, err := f.ReadFile(e.Name); err == nil && !bytes.Equal(got, want[e.Name]) {
				t.Fatalf("cut at %d: %s read wrong", n, e.Name)
			}
		}
	}
}

func TestReadFileLimits(t *testing.T) {
	entries := sample()
	b := buildCHM(t, chmSpec{}, entries)
	lim := defaultLimits
	lim.entry = 60_000
	f, err := open(bytes.NewReader(b.data), int64(len(b.data)), lim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadFile("html/setup.htm"); err == nil {
		t.Error("read a 120,000-byte entry with a 60,000-byte limit")
	}
	if _, err := f.ReadFile("html/index.htm"); err != nil {
		t.Errorf("a 50,000-byte entry is within the limit: %v", err)
	}
}

// TestEntryBounds: entries whose directory records point outside their
// section must fail to read, not read something else.
func TestEntryBounds(t *testing.T) {
	entries := []testEntry{
		{name: "/stored.htm", section: 0, data: []byte("stored")},
		{name: "/packed.htm", section: 1, data: []byte("packed")},
	}
	b := buildCHM(t, chmSpec{}, entries)
	f := openBuilt(t, b)
	f.files[0].offset = int64(len(b.data))
	f.files[1].offset = 1 << 40
	for _, name := range []string{"stored.htm", "packed.htm"} {
		if _, err := f.ReadFile(name); err == nil {
			t.Errorf("%s read from outside its section", name)
		}
	}
}

func TestDuplicateNames(t *testing.T) {
	entries := []testEntry{
		{name: "/a.htm", section: 1, data: []byte("first")},
		{name: "/a.htm", section: 1, data: []byte("second")},
		{name: "a.htm", section: 0, data: []byte("third")}, // the same name, without its slash
	}
	f := openBuilt(t, buildCHM(t, chmSpec{}, entries))
	if got := f.Entries(); len(got) != 1 {
		t.Fatalf("Entries lists %v; a name listed again keeps its first entry only", got)
	}
	if got, _ := f.ReadFile("a.htm"); string(got) != "first" {
		t.Errorf("read %q, want the first entry", got)
	}
}

func TestParseNameList(t *testing.T) {
	good := []byte{30, 0, 2, 0}
	for _, n := range []string{"Uncompressed", "MSCompressed"} {
		good = binary.LittleEndian.AppendUint16(good, uint16(len(n)))
		good = append(good, utf16le(n)...)
		good = append(good, 0, 0)
	}
	names, err := parseNameList(good)
	if err != nil || len(names) != 2 || names[0] != "Uncompressed" || names[1] != "MSCompressed" {
		t.Fatalf("got %q, %v", names, err)
	}
	for n := range len(good) - 1 {
		if _, err := parseNameList(good[:n]); err == nil {
			t.Errorf("parsed a NameList cut to %d bytes", n)
		}
	}
}

// TestConcurrentReads reads every entry from many goroutines at once. The
// race detector checks the shared decoder is guarded.
func TestConcurrentReads(t *testing.T) {
	var entries []testEntry
	for i := range 24 {
		entries = append(entries, testEntry{name: fmt.Sprintf("/f%02d.htm", i), section: i % 2, data: textData(9000+i*500, uint64(i))})
	}
	f := openBuilt(t, buildCHM(t, chmSpec{}, entries))
	var wg sync.WaitGroup
	errs := make(chan error, 8*len(entries))
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range entries {
				e := entries[(i*7+g)%len(entries)]
				got, err := f.ReadFile(e.name[1:])
				if err != nil || !bytes.Equal(got, e.data) {
					errs <- fmt.Errorf("%s: %v", e.name, err)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestE8Archive: a stream with E8 translation on reads right whatever order
// its files are read in, because each read then starts afresh.
func TestE8Archive(t *testing.T) {
	const size = 3_000_000
	var entries []testEntry
	for i := range 6 {
		entries = append(entries, testEntry{name: fmt.Sprintf("/x%d.exe", i), section: 1, data: e8Data(30_000+i*1000, size, uint64(i))})
	}
	// One reset interval covers the whole stream, so every read starts at its
	// beginning, where the test compressor counts positions from.
	b := buildCHM(t, chmSpec{enc: encOptions{resetFrames: 16, e8Size: size, mode: modeMixed}}, entries)
	f := openBuilt(t, b)
	for i := len(entries) - 1; i >= 0; i-- {
		got, err := f.ReadFile(entries[i].name[1:])
		if err != nil || !bytes.Equal(got, entries[i].data) {
			t.Fatalf("%s: %v", entries[i].name, err)
		}
	}
	checkAll(t, f, entries)
}
