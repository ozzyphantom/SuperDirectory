package chm

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
	"unicode/utf16"
)

// A CHM writer, for tests only. It lays an archive out the way Microsoft's
// compiler does: the ITSF header, header section 0, the directory (an ITSP
// header, then listing chunks and, if asked, an index chunk), then content
// section 0, which holds the uncompressed entries and the MSCompressed
// section's own files, its compressed stream among them.

// testEntry is one directory entry.
type testEntry struct {
	name    string // as the directory stores it, e.g. "/html/a.htm"
	section int
	data    []byte
}

type chmSpec struct {
	version   int        // ITSF version; 3 when zero
	chunkSize int        // directory chunk size; 4096 when zero
	enc       encOptions // section 1's compression; a 64 KiB window and 2-frame resets when zero
	index     bool       // add an index chunk, which the reader must pass over
	scramble  bool       // store the listing chunks in reverse, linked in order
	nameList  []string   // the sections' names; Uncompressed and MSCompressed when nil

	// tableFrames is how many frames the reset table lists: every frame when
	// zero, none when negative.
	tableFrames int
}

// built is an archive and where its parts landed.
type built struct {
	data      []byte
	chunks    int // archive offset of directory chunk 0
	chunkSize int
	numChunks int
	sec0      int            // archive offset of content section 0
	content   int            // archive offset of the compressed stream
	frames    []int64        // each frame's offset in the compressed stream
	at        map[string]int // each section-0 entry's archive offset
}

func guid(s string) []byte {
	h, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(h) != 16 {
		panic("bad GUID " + s)
	}
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b, binary.BigEndian.Uint32(h))
	binary.LittleEndian.PutUint16(b[4:], binary.BigEndian.Uint16(h[4:]))
	binary.LittleEndian.PutUint16(b[6:], binary.BigEndian.Uint16(h[6:]))
	copy(b[8:], h[8:])
	return b
}

func putEncint(b []byte, v int64) []byte {
	var tmp [10]byte
	i := len(tmp) - 1
	tmp[i] = byte(v & 0x7F)
	for v >>= 7; v > 0; v >>= 7 {
		i--
		tmp[i] = byte(v&0x7F) | 0x80
	}
	return append(b, tmp[i:]...)
}

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}

type dirEntry struct {
	name           string
	section        int
	offset, length int64
}

func buildCHM(t testing.TB, spec chmSpec, entries []testEntry) built {
	t.Helper()
	if spec.version == 0 {
		spec.version = 3
	}
	if spec.chunkSize == 0 {
		spec.chunkSize = 4096
	}
	enc := spec.enc
	if enc.windowBits == 0 {
		enc.windowBits = 16
	}
	if enc.resetFrames == 0 {
		enc.resetFrames = 2
	}
	names := spec.nameList
	if names == nil {
		names = []string{"Uncompressed", "MSCompressed"}
	}
	b := built{chunkSize: spec.chunkSize}

	// Section 1: every compressed entry is a slice of one stream. The
	// compressor pads it to a whole frame, and no further: 7-Zip refuses a reset
	// table that lists a frame past the last one holding data.
	var stream []byte
	at := make([]int64, len(entries))
	for i, e := range entries {
		if e.section == 1 {
			at[i] = int64(len(stream))
			stream = append(stream, e.data...)
		}
	}
	length := len(stream)
	stream = append(stream, make([]byte, (frameSize-length%frameSize)%frameSize)...)
	compressed, frames := encodeLZX(t, stream, enc)
	b.frames = frames

	// Section 0: the uncompressed entries, then the files that describe section 1.
	var sec0 []byte
	var dir []dirEntry
	b.at = make(map[string]int)
	place := func(name string, data []byte) {
		if _, dup := b.at[name]; !dup {
			b.at[name] = len(sec0) // made absolute below, once section 0's place is known
		}
		dir = append(dir, dirEntry{name, 0, int64(len(sec0)), int64(len(data))})
		sec0 = append(sec0, data...)
	}
	for i, e := range entries {
		if e.section == 1 {
			dir = append(dir, dirEntry{e.name, 1, at[i], int64(len(e.data))})
		} else {
			place(e.name, e.data)
		}
	}

	var nl []byte
	for _, n := range names {
		nl = binary.LittleEndian.AppendUint16(nl, uint16(len(utf16.Encode([]rune(n)))))
		nl = append(nl, utf16le(n)...)
		nl = append(nl, 0, 0)
	}
	nl = append(binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(nil, uint16((len(nl)+4)/2)), uint16(len(names))), nl...)
	place(nameListName, nl)

	le32 := func(v ...uint32) []byte {
		var out []byte
		for _, x := range v {
			out = binary.LittleEndian.AppendUint32(out, x)
		}
		return out
	}
	window := uint32(1) << enc.windowBits
	ctl := append(le32(6), "LZXC"...)
	ctl = append(ctl, le32(2, uint32(enc.resetFrames), window/frameSize, uint32(enc.resetFrames), 0)...)
	place(controlName, ctl)
	place(spanInfoName, binary.LittleEndian.AppendUint64(nil, uint64(length)))
	place("::DataSpace/Storage/MSCompressed/Transform/List", utf16le("{7FC28940-9D31-11D0-9B27-00A0C91E9C7C}"))

	// The table lists every frame, and its compressed length ends with the
	// last; 7-Zip reads each frame's compressed size off the two.
	listed := len(frames)
	if spec.tableFrames != 0 {
		listed = max(0, min(spec.tableFrames, len(frames)))
	}
	rt := le32(2, uint32(listed), 8, 0x28)
	rt = binary.LittleEndian.AppendUint64(rt, uint64(length))
	rt = binary.LittleEndian.AppendUint64(rt, uint64(len(compressed)))
	rt = binary.LittleEndian.AppendUint64(rt, frameSize)
	for _, f := range frames[:listed] {
		rt = binary.LittleEndian.AppendUint64(rt, uint64(f))
	}
	place(resetName, rt)
	contentAt := len(sec0)
	place(contentName, compressed)

	// The directory: listing chunks in order, each linked to the next.
	type chunk struct {
		body  []byte
		count int
		first string
	}
	var chunks []chunk
	cur := chunk{}
	for _, d := range dir {
		rec := putEncint(nil, int64(len(d.name)))
		rec = append(rec, d.name...)
		rec = putEncint(rec, int64(d.section))
		rec = putEncint(rec, d.offset)
		rec = putEncint(rec, d.length)
		if pmglHeader+len(rec)+2 > spec.chunkSize {
			t.Fatalf("buildCHM: %s does not fit a chunk of %d bytes", d.name, spec.chunkSize)
		}
		if pmglHeader+len(cur.body)+len(rec)+2 > spec.chunkSize {
			chunks = append(chunks, cur)
			cur = chunk{}
		}
		if cur.count == 0 {
			cur.first = d.name
		}
		cur.body = append(cur.body, rec...)
		cur.count++
	}
	chunks = append(chunks, cur)

	n := len(chunks)
	phys := make([]int, n) // where each listing chunk is stored
	for i := range phys {
		phys[i] = i
		if spec.scramble {
			phys[i] = n - 1 - i
		}
	}
	numChunks := n
	if spec.index {
		numChunks++
	}
	dirBytes := make([]byte, numChunks*spec.chunkSize)
	link := func(i int) uint32 {
		if i < 0 || i >= n {
			return noChunk
		}
		return uint32(phys[i])
	}
	for i, c := range chunks {
		out := dirBytes[phys[i]*spec.chunkSize : (phys[i]+1)*spec.chunkSize]
		copy(out, "PMGL")
		binary.LittleEndian.PutUint32(out[4:], uint32(spec.chunkSize-pmglHeader-len(c.body)))
		binary.LittleEndian.PutUint32(out[0x0C:], link(i-1))
		binary.LittleEndian.PutUint32(out[0x10:], link(i+1))
		copy(out[pmglHeader:], c.body)
		binary.LittleEndian.PutUint16(out[spec.chunkSize-2:], uint16(c.count))
	}
	indexRoot, depth := uint32(noChunk), uint32(1)
	if spec.index {
		// An index chunk names the first entry of every listing chunk.
		out := dirBytes[n*spec.chunkSize:]
		copy(out, "PMGI")
		var body []byte
		for i, c := range chunks {
			body = putEncint(body, int64(len(c.first)))
			body = append(body, c.first...)
			body = putEncint(body, int64(phys[i]))
		}
		if 8+len(body)+2 > spec.chunkSize {
			t.Fatal("buildCHM: the index does not fit one chunk")
		}
		binary.LittleEndian.PutUint32(out[4:], uint32(spec.chunkSize-8-len(body)))
		copy(out[8:], body)
		binary.LittleEndian.PutUint16(out[spec.chunkSize-2:], uint16(n))
		indexRoot, depth = uint32(n), 2
	}

	itsp := append([]byte("ITSP"), le32(1, itspLen, 0x0A, uint32(spec.chunkSize), 2, depth, indexRoot,
		link(0), link(n-1), noChunk, uint32(numChunks), 0x409)...)
	itsp = append(itsp, guid("5D02926A-212E-11D0-9DF9-00A0C922E6EC")...)
	itsp = append(itsp, le32(itspLen, noChunk, noChunk, noChunk)...)

	headerLen := itsfV3Len
	if spec.version == 2 {
		headerLen = itsfV2Len
	}
	hs0 := headerLen
	hs1 := hs0 + 0x18
	b.chunks = hs1 + itspLen
	b.numChunks = numChunks
	b.sec0 = b.chunks + len(dirBytes)
	b.content = b.sec0 + contentAt
	for name := range b.at {
		b.at[name] += b.sec0
	}
	total := b.sec0 + len(sec0)

	var out bytes.Buffer
	out.WriteString("ITSF")
	out.Write(le32(uint32(spec.version), uint32(headerLen), 1, 0x12345678, 0x409))
	out.Write(guid("7C01FD10-7BAA-11D0-9E0C-00A0C922E6EC"))
	out.Write(guid("7C01FD11-7BAA-11D0-9E0C-00A0C922E6EC"))
	for _, v := range []int{hs0, 0x18, hs1, itspLen + len(dirBytes)} {
		out.Write(binary.LittleEndian.AppendUint64(nil, uint64(v)))
	}
	if spec.version != 2 {
		out.Write(binary.LittleEndian.AppendUint64(nil, uint64(b.sec0)))
	}
	out.Write(le32(0x01FE, 0))
	out.Write(binary.LittleEndian.AppendUint64(nil, uint64(total)))
	out.Write(le32(0, 0))
	out.Write(itsp)
	out.Write(dirBytes)
	out.Write(sec0)
	b.data = out.Bytes()
	if len(b.data) != total {
		t.Fatalf("buildCHM: wrote %d bytes, planned %d", len(b.data), total)
	}
	return b
}

func openBuilt(t testing.TB, b built) *File {
	t.Helper()
	f, err := Open(bytes.NewReader(b.data), int64(len(b.data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return f
}
