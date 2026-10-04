package chm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"unicode/utf16"
)

// The archive's own files that describe the compressed section. All of them
// live in section 0.
const (
	nameListName = "::DataSpace/NameList"
	controlName  = "::DataSpace/Storage/MSCompressed/ControlData"
	spanInfoName = "::DataSpace/Storage/MSCompressed/SpanInfo"
	resetName    = "::DataSpace/Storage/MSCompressed/Transform/{7FC28940-9D31-11D0-9B27-00A0C91E9C7C}/InstanceData/ResetTable"
	contentName  = "::DataSpace/Storage/MSCompressed/Content"
)

var sysNames = map[string]bool{
	nameListName: true,
	controlName:  true,
	spanInfoName: true,
	resetName:    true,
	contentName:  true,
}

// lzxSection is the MSCompressed section: one LZX stream, stored in a
// section-0 file, that every section-1 entry is a slice of.
type lzxSection struct {
	r           io.ReaderAt
	size        int64 // the archive's
	windowBits  uint
	resetFrames int64 // frames from one reset to the next
	length      int64 // decoded length, padded to whole reset intervals
	data        int64 // archive offset of the compressed stream
	dataLen     int64
	table       *resetTable // nil when the archive's is missing or malformed

	dec     *lzx
	window  []byte // a dead decoder's window, for the next to reuse
	decNext int64  // the frame dec decodes next
	cur     []byte // the last frame decoded, kept for the next read
	curIdx  int64  // which frame cur holds, or -1
	e8      bool   // some frame needed E8 translation
	decoded int64  // frames decoded in all; tests read it
}

// resetTable says where each frame's compressed data begins. Decoding can
// start at any frame where the compressor reset its state.
type resetTable struct {
	at        int64 // archive offset of the table
	size      int64 // its length
	entries   int64
	entrySize int64
	first     int64 // offset of the first entry within the table
	length    int64 // the stream's decoded length
}

func (f *File) loadCompressed() (*lzxSection, error) {
	if e, ok := f.sys[nameListName]; ok {
		b, err := f.sysFile(e, 1<<16)
		if err != nil {
			return nil, fmt.Errorf("NameList: %w", err)
		}
		names, err := parseNameList(b)
		if err != nil {
			return nil, fmt.Errorf("NameList: %w", err)
		}
		if len(names) < 2 {
			return nil, errors.New("NameList names no section 1")
		}
		if names[1] != "MSCompressed" {
			return nil, fmt.Errorf("section 1 is %q, which this reader does not know", names[1])
		}
	} // without a NameList, the files below can only be MSCompressed's

	e, ok := f.sys[controlName]
	if !ok {
		return nil, errors.New("no ControlData")
	}
	ctl, err := f.sysFile(e, 1024)
	if err != nil {
		return nil, fmt.Errorf("ControlData: %w", err)
	}
	// A count of DWORDs, "LZXC", a version, the reset interval and the window
	// size, then a cache size: how many reset intervals Microsoft's reader keeps
	// decoded. This reader keeps one frame instead, so it skips that one.
	if len(ctl) < 0x18 || string(ctl[4:8]) != "LZXC" {
		return nil, errors.New("ControlData is not LZXC")
	}
	reset := uint64(binary.LittleEndian.Uint32(ctl[0x0C:]))
	window := uint64(binary.LittleEndian.Uint32(ctl[0x10:]))
	switch v := binary.LittleEndian.Uint32(ctl[8:]); v {
	case 1: // sizes in bytes
	case 2: // sizes in frames
		reset *= frameSize
		window *= frameSize
	default:
		return nil, fmt.Errorf("ControlData version %d; only 1 and 2 are known", v)
	}
	windowBits := uint(bits.TrailingZeros64(window))
	if window != 1<<windowBits || windowBits < minWindowBits || windowBits > maxWindowBits {
		return nil, fmt.Errorf("a window of %d bytes; LZX allows 32 KiB to 2 MiB", window)
	}
	if reset == 0 || reset%frameSize != 0 {
		return nil, fmt.Errorf("a reset interval of %d bytes, not a whole number of frames", reset)
	}

	e, ok = f.sys[contentName]
	if !ok {
		return nil, errors.New("no Content")
	}
	if e.section != 0 {
		return nil, errors.New("Content is not stored in section 0")
	}
	if e.offset > f.size-f.sec0 {
		return nil, fmt.Errorf("Content: %w", errPastEnd)
	}
	s := &lzxSection{
		r:          f.r,
		size:       f.size,
		windowBits: windowBits,
		data:       f.sec0 + e.offset,
		curIdx:     -1,
	}
	// A truncated archive keeps what it has: entries before the cut still read.
	s.dataLen = min(e.Size, f.size-s.data)

	s.table, err = f.loadResetTable()
	if err != nil {
		return nil, err
	}
	var length int64
	if s.table != nil {
		length = s.table.length
	} else if length, err = f.spanInfo(); err != nil {
		return nil, err
	}
	if length > f.lim.stream {
		return nil, fmt.Errorf("it decodes to %d bytes, more than the %d this reader allows", length, f.lim.stream)
	}
	if length > 0 {
		// Frames decode whole, so the stream runs on past its stated length to
		// the end of its last frame. chmd.c lets an entry reach as far as the
		// end of that reset interval, and so does this.
		if reset > uint64(f.lim.stream) {
			return nil, fmt.Errorf("a reset interval of %d bytes, more than the %d this reader allows", reset, f.lim.stream)
		}
		r := int64(reset)
		s.length = (length + r - 1) / r * r
	}
	s.resetFrames = max(1, int64(min(reset, uint64(f.lim.stream)))/frameSize)
	return s, nil
}

// loadResetTable reads the ResetTable's header. When the table is missing or
// malformed, chmd.c decodes from the start of the stream and takes its length
// from SpanInfo; so does this, by returning a nil table.
func (f *File) loadResetTable() (*resetTable, error) {
	e, ok := f.sys[resetName]
	if !ok || e.section != 0 || e.Size < 0x28 {
		return nil, nil
	}
	if e.offset > f.size-f.sec0 || e.Size > f.size-f.sec0-e.offset {
		return nil, nil
	}
	var h [0x28]byte
	if err := readAt(f.r, f.size, f.sec0+e.offset, h[:]); err != nil {
		return nil, fmt.Errorf("ResetTable: %w", err)
	}
	// A version, the entry count, the entry size, the entries' offset, then the
	// decoded length, the compressed length and the frame length.
	length, ok := off64(h[0x10:])
	if !ok || binary.LittleEndian.Uint64(h[0x20:]) != frameSize {
		return nil, nil
	}
	return &resetTable{
		at:        f.sec0 + e.offset,
		size:      e.Size,
		entries:   int64(binary.LittleEndian.Uint32(h[4:])),
		entrySize: int64(binary.LittleEndian.Uint32(h[8:])),
		first:     int64(binary.LittleEndian.Uint32(h[0x0C:])),
		length:    length,
	}, nil
}

// spanInfo reads SpanInfo, which holds just the decoded length.
func (f *File) spanInfo() (int64, error) {
	e, ok := f.sys[spanInfoName]
	if !ok {
		return 0, errors.New("neither a usable ResetTable nor a SpanInfo")
	}
	b, err := f.sysFile(e, 8)
	if err != nil {
		return 0, fmt.Errorf("SpanInfo: %w", err)
	}
	if len(b) != 8 {
		return 0, fmt.Errorf("a SpanInfo of %d bytes; it should be 8", len(b))
	}
	length, ok := off64(b)
	if !ok {
		return 0, errors.New("SpanInfo's length is out of range")
	}
	return length, nil
}

// start returns where frame k's compressed data begins, when the table lists it.
func (t *resetTable) start(r io.ReaderAt, size, k int64) (int64, bool, error) {
	if t == nil || k >= t.entries || (t.entrySize != 4 && t.entrySize != 8) {
		return 0, false, nil
	}
	pos := t.first + k*t.entrySize
	if pos > t.size-t.entrySize {
		return 0, false, nil
	}
	var b [8]byte
	if err := readAt(r, size, t.at+pos, b[:t.entrySize]); err != nil {
		return 0, false, fmt.Errorf("ResetTable: %w", err)
	}
	if t.entrySize == 4 {
		return int64(binary.LittleEndian.Uint32(b[:])), true, nil
	}
	v, ok := off64(b[:])
	return v, ok, nil
}

// sysFile reads one of the archive's own files whole.
func (f *File) sysFile(e entry, limit int64) ([]byte, error) {
	if e.section != 0 {
		return nil, errors.New("it is not stored in section 0")
	}
	if e.Size > limit {
		return nil, fmt.Errorf("%d bytes is implausibly large", e.Size)
	}
	if e.offset > f.size-f.sec0 || e.Size > f.size-f.sec0-e.offset {
		return nil, errPastEnd
	}
	b := make([]byte, e.Size)
	if err := readAt(f.r, f.size, f.sec0+e.offset, b); err != nil {
		return nil, err
	}
	return b, nil
}

// parseNameList reads ::DataSpace/NameList: the file's length in 16-bit words,
// a count of names, then each name as a length, that many UTF-16 characters and
// a terminating zero. The names are the sections', in order.
func parseNameList(b []byte) ([]string, error) {
	if len(b) < 4 {
		return nil, errors.New("too short")
	}
	n := int(binary.LittleEndian.Uint16(b[2:]))
	b = b[4:]
	var names []string
	for range n {
		if len(b) < 2 {
			return nil, errors.New("truncated")
		}
		l := int(binary.LittleEndian.Uint16(b))
		if len(b) < 2+2*l+2 {
			return nil, errors.New("truncated")
		}
		u := make([]uint16, l)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(b[2+2*i:])
		}
		names = append(names, string(utf16.Decode(u)))
		b = b[2+2*l+2:]
	}
	return names, nil
}

// read returns n bytes of the decoded stream from off.
func (s *lzxSection) read(off, n int64) ([]byte, error) {
	out := make([]byte, 0, min(n, 1<<20)) // grow with what decodes, not with what the directory claims
	for pos := off; pos < off+n; {
		k := pos / frameSize
		f, err := s.frame(k, pos == off)
		if err != nil {
			return nil, err
		}
		from := pos - k*frameSize
		take := min(int64(len(f))-from, off+n-pos)
		out = append(out, f[from:from+take]...)
		pos += take
	}
	return out, nil
}

// frame returns decoded frame k. Decoding carries on from the last read when k
// lies ahead within the same reset interval. Otherwise it starts again at the
// reset point at or before k, as chmd.c does.
func (s *lzxSection) frame(k int64, first bool) ([]byte, error) {
	if first && s.e8 {
		// With E8 translation on, lzxd.c's output depends on where decoding
		// began, because it counts the translation position from there. So each
		// read then starts afresh at its own reset point, as chmd.c does on a
		// freshly opened archive, and a file reads the same whatever came before.
		s.drop()
		s.curIdx = -1
	}
	if k == s.curIdx {
		return s.cur, nil
	}
	reset := k / s.resetFrames * s.resetFrames
	if s.dec == nil || s.decNext > k || s.decNext < reset {
		if err := s.restart(reset); err != nil {
			return nil, err
		}
	}
	for {
		f, err := s.dec.next()
		s.e8 = s.e8 || s.dec.e8
		if err != nil {
			err = fmt.Errorf("frame %d: %w", s.decNext, err)
			s.drop() // its state is past saving
			return nil, err
		}
		s.decoded++
		s.decNext++
		if s.decNext > k {
			s.cur = append(s.cur[:0], f...)
			s.curIdx = k
			return s.cur, nil
		}
	}
}

// drop discards the decoder but keeps its window for the next one.
func (s *lzxSection) drop() {
	if s.dec != nil {
		s.window = s.dec.window
	}
	s.dec = nil
}

// restart begins decoding at frame k, a reset point.
func (s *lzxSection) restart(k int64) error {
	s.drop()
	start, ok, err := s.table.start(s.r, s.size, k)
	if err != nil {
		return err
	}
	if !ok {
		k, start = 0, 0 // the table does not reach k: decode from the beginning, as chmd.c does
	}
	if start > s.dataLen {
		return fmt.Errorf("the reset table puts frame %d past the end of the compressed data", k)
	}
	in := &stream{r: s.r, next: s.data + start, end: s.data + s.dataLen}
	d, err := newLZX(in, s.windowBits, int(s.resetFrames), s.window)
	if err != nil {
		return err
	}
	s.dec, s.decNext, s.window = d, k, nil
	return nil
}
