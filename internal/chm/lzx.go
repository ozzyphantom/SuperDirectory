package chm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// LZX is the compression in a CHM's MSCompressed section, the same LZX that
// Microsoft's cabinet files use. This decoder follows libmspack's lzxd.c. The
// output comes in 32 KiB frames, each built from verbatim, aligned-offset or
// uncompressed blocks. A block sends its Huffman code lengths as deltas against
// the previous block's. At every reset interval the decoder starts afresh, which
// is what lets a reader begin in the middle of the stream.

const (
	frameSize = 32768 // LZX produces its output in frames of this size

	minMatch            = 2
	numChars            = 256
	numPrimaryLengths   = 7
	numSecondaryLengths = 249
	pretreeSyms         = 20
	alignedSyms         = 8
	maxCodeLen          = 16

	// lenSafety is how far a run in a code-length table may spill past the
	// range being read. lzxd.c allows the spill rather than rejecting it, and a
	// spill out of the first 256 main-tree lengths becomes the delta base for
	// the rest.
	lenSafety = 64

	blockVerbatim     = 1
	blockAligned      = 2
	blockUncompressed = 3

	minWindowBits = 15
	maxWindowBits = 21
	maxSlots      = 50 // position slots at the largest window
	maxMainSyms   = numChars + maxSlots*8

	// e8Frames is how many frames get Intel E8 translation: the first gigabyte.
	e8Frames = 32768
)

// positionSlots is the number of position slots for windows of 2^15 to 2^21 bytes.
var positionSlots = [...]int{30, 32, 34, 36, 38, 42, 50}

// extraBits and positionBase describe the position slots. A match offset is the
// slot's base, plus that many footer bits, minus 2.
var extraBits, positionBase = slotTables()

func slotTables() (extra [maxSlots + 1]uint8, base [maxSlots + 1]uint32) {
	for i := range extra {
		switch {
		case i < 4:
		case i >= 36:
			extra[i] = 17
		default:
			extra[i] = uint8(i/2 - 1)
		}
	}
	for i := 1; i < len(base); i++ {
		base[i] = base[i-1] + 1<<extra[i-1]
	}
	return extra, base
}

var errInputEnd = errors.New("lzx: the compressed data ends early")

// stream is LZX's input: a span of the archive, read in chunks.
type stream struct {
	r    io.ReaderAt
	next int64 // archive offset of the next chunk
	end  int64 // archive offset where the compressed data ends
	buf  []byte
	pos  int
}

const streamChunk = 64 << 10

func (s *stream) fill() error {
	if s.next >= s.end {
		return errInputEnd
	}
	n := int(min(streamChunk, s.end-s.next))
	if cap(s.buf) < n {
		s.buf = make([]byte, n)
	}
	s.buf = s.buf[:n]
	if m, err := s.r.ReadAt(s.buf, s.next); m < n {
		s.buf, s.pos = s.buf[:0], 0
		if err == nil || errors.Is(err, io.EOF) {
			return errInputEnd // the archive is shorter than its directory says
		}
		return err
	}
	s.next += int64(n)
	s.pos = 0
	return nil
}

func (s *stream) byte() (byte, error) {
	if s.pos == len(s.buf) {
		if err := s.fill(); err != nil {
			return 0, err
		}
	}
	c := s.buf[s.pos]
	s.pos++
	return c, nil
}

func (s *stream) read(p []byte) error {
	for len(p) > 0 {
		if s.pos == len(s.buf) {
			if err := s.fill(); err != nil {
				return err
			}
		}
		n := copy(p, s.buf[s.pos:])
		s.pos += n
		p = p[n:]
	}
	return nil
}

// bitReader reads LZX's bitstream: 16-bit little-endian words, each read from
// its top bit down. It loads a word only when a read needs more bits, exactly
// as lzxd.c does, because the padding before an uncompressed block is defined
// by what the reader has loaded.
type bitReader struct {
	in     *stream
	buf    uint64 // unread bits, from the top
	n      uint   // how many
	padded bool   // the zero word past the end has been handed out
}

func (b *bitReader) need(n uint) error {
	for b.n < n {
		lo, err := b.in.byte()
		var hi byte
		if err == nil {
			hi, err = b.in.byte()
		}
		if err != nil {
			// A Huffman read peeks 16 bits even at the very end of the data, so
			// lzxd.c hands out two zero bytes once before it calls the input short.
			if err != errInputEnd || b.padded {
				return err
			}
			b.padded = true
			lo, hi = 0, 0
		}
		b.buf |= (uint64(hi)<<8 | uint64(lo)) << (48 - b.n)
		b.n += 16
	}
	return nil
}

func (b *bitReader) drop(n uint) {
	b.buf <<= n
	b.n -= n
}

func (b *bitReader) read(n uint) (uint32, error) {
	if err := b.need(n); err != nil {
		return 0, err
	}
	v := uint32(b.buf >> (64 - n))
	b.drop(n)
	return v, nil
}

// decode reads one Huffman-coded symbol.
func (b *bitReader) decode(h *huffman) (int, error) {
	if err := b.need(maxCodeLen); err != nil {
		return 0, err
	}
	peek := uint32(b.buf >> (64 - maxCodeLen))
	e := h.table[peek>>(maxCodeLen-h.bits)]
	sym, n := int(e>>8), uint(e&0xFF)
	if n == 0 {
		var ok bool
		if sym, n, ok = h.long(peek); !ok {
			return 0, errors.New("lzx: a code matches no symbol")
		}
	}
	b.drop(n)
	return sym, nil
}

// huffman decodes a canonical Huffman code: shorter codes first, and within a
// length, lower symbols first. Codes up to bits long resolve in one table
// lookup; longer ones fall back to a search by length.
type huffman struct {
	bits   uint
	table  []uint32 // symbol<<8 | length, or 0 for the prefix of a longer code
	first  [maxCodeLen + 1]uint32
	count  [maxCodeLen + 1]uint32
	offset [maxCodeLen + 1]uint32 // where each length's symbols start in syms
	syms   []uint16
}

var errIncomplete = errors.New("code lengths that do not form a complete code")

// build makes the decoding table for the given code lengths. LZX sends only
// complete codes, and lzxd.c refuses anything else; so does this. A length over
// 16, which a malformed delta can leave behind, gives its symbol no code, as in
// lzxd.c.
func (h *huffman) build(lens []byte, bits uint) error {
	var count [maxCodeLen + 1]uint32
	for _, l := range lens {
		if l >= 1 && l <= maxCodeLen {
			count[l]++
		}
	}
	var space uint32 // a complete code fills the 2^16 code space exactly
	for l := 1; l <= maxCodeLen; l++ {
		space += count[l] << (maxCodeLen - l)
	}
	if space != 1<<maxCodeLen {
		return errIncomplete
	}

	h.bits, h.count = bits, count
	var code, n uint32
	for l := 1; l <= maxCodeLen; l++ {
		code = (code + count[l-1]) << 1 // count[0] stays 0: unused symbols take no codes
		h.first[l], h.offset[l] = code, n
		n += count[l]
	}
	if cap(h.syms) < int(n) {
		h.syms = make([]uint16, n)
	}
	h.syms = h.syms[:n]
	if size := 1 << bits; cap(h.table) < size {
		h.table = make([]uint32, size)
	} else {
		h.table = h.table[:size]
	}

	next := h.first
	placed := h.offset
	for sym, l := range lens {
		if l < 1 || l > maxCodeLen {
			continue
		}
		c := next[l]
		next[l]++
		h.syms[placed[l]] = uint16(sym)
		placed[l]++
		if uint(l) <= bits {
			entry := uint32(sym)<<8 | uint32(l)
			for i, end := c<<(bits-uint(l)), (c+1)<<(bits-uint(l)); i < end; i++ {
				h.table[i] = entry
			}
		} else {
			h.table[c>>(uint(l)-bits)] = 0
		}
	}
	return nil
}

// long decodes a code longer than the table's index from 16 peeked bits.
func (h *huffman) long(peek uint32) (int, uint, bool) {
	for l := h.bits + 1; l <= maxCodeLen; l++ {
		code := peek >> (maxCodeLen - l)
		if code >= h.first[l] && code-h.first[l] < h.count[l] {
			return int(h.syms[h.offset[l]+code-h.first[l]]), l, true
		}
	}
	return 0, 0, false
}

// lzx decodes one LZX stream, a frame at a time, from a point where the
// compressor reset its state.
type lzx struct {
	in          bitReader
	window      []byte
	resetFrames int // frames from one reset to the next; 0 for none
	mainSyms    int

	windowPos, framePos int
	frame               int // frames decoded since this decoder began
	r0, r1, r2          uint32
	headerRead          bool
	blockType           int
	blockLen            int
	blockRemaining      int

	intelFileSize int32
	intelCurPos   int32
	intelStarted  bool
	e8            bool // a header turned E8 translation on

	pretreeLen  [pretreeSyms]byte
	mainLen     [maxMainSyms + lenSafety]byte
	lengthLen   [numSecondaryLengths + lenSafety]byte
	alignedLen  [alignedSyms]byte
	pretree     huffman
	main        huffman
	length      huffman
	aligned     huffman
	lengthEmpty bool

	e8buf []byte
}

// newLZX starts a decoder. It takes over window, cleared, when that is the
// right size, so restarting costs no new 2 MiB.
func newLZX(in *stream, windowBits uint, resetFrames int, window []byte) (*lzx, error) {
	if windowBits < minWindowBits || windowBits > maxWindowBits {
		return nil, fmt.Errorf("lzx: a window of 2^%d bytes; LZX allows 2^15 to 2^21", windowBits)
	}
	if resetFrames < 0 {
		return nil, errors.New("lzx: a negative reset interval")
	}
	if len(window) == 1<<windowBits {
		clear(window) // a malformed match can read bytes not yet written; they must not be another read's
	} else {
		window = make([]byte, 1<<windowBits)
	}
	d := &lzx{
		in:          bitReader{in: in},
		window:      window,
		resetFrames: resetFrames,
		mainSyms:    numChars + positionSlots[windowBits-minWindowBits]*8,
	}
	d.resetState()
	return d, nil
}

// resetState is what happens at every reset interval: repeated offsets back to
// 1, code lengths back to zero, and the E8 header read again. The window keeps
// its contents and its position, as in lzxd.c.
func (d *lzx) resetState() {
	d.r0, d.r1, d.r2 = 1, 1, 1
	d.headerRead = false
	d.blockRemaining = 0
	d.blockType = 0
	clear(d.mainLen[:])
	clear(d.lengthLen[:])
}

// next decodes the next 32 KiB frame. The slice is valid until the next call.
func (d *lzx) next() ([]byte, error) {
	if d.resetFrames > 0 && d.frame%d.resetFrames == 0 {
		d.resetState()
	}
	if !d.headerRead {
		// One bit says whether E8 translation is on; if it is, 32 bits follow
		// with the "file size" that bounds which call targets get translated.
		on, err := d.in.read(1)
		if err != nil {
			return nil, err
		}
		d.intelFileSize = 0
		if on == 1 {
			hi, err := d.in.read(16)
			if err != nil {
				return nil, err
			}
			lo, err := d.in.read(16)
			if err != nil {
				return nil, err
			}
			d.intelFileSize = int32(hi<<16 | lo)
		}
		if d.intelFileSize != 0 {
			d.e8 = true
		}
		d.headerRead = true
	}

	for todo := d.framePos + frameSize - d.windowPos; todo > 0; {
		if d.blockRemaining == 0 {
			if err := d.readBlockHeader(); err != nil {
				return nil, err
			}
		}
		run := min(d.blockRemaining, todo)
		todo -= run
		d.blockRemaining -= run
		if d.blockType == blockUncompressed {
			if err := d.in.in.read(d.window[d.windowPos : d.windowPos+run]); err != nil {
				return nil, err
			}
			d.windowPos += run
		} else if err := d.decodeRun(run); err != nil {
			return nil, err
		}
	}

	// Every frame ends on a 16-bit boundary in the input.
	if d.in.n > 0 {
		if err := d.in.need(16); err != nil {
			return nil, err
		}
	}
	d.in.drop(d.in.n & 15)

	out := d.window[d.framePos : d.framePos+frameSize]
	switch {
	case d.intelStarted && d.intelFileSize != 0 && d.frame < e8Frames:
		if d.e8buf == nil {
			d.e8buf = make([]byte, frameSize)
		}
		copy(d.e8buf, out)
		translateE8(d.e8buf, d.intelCurPos, d.intelFileSize)
		out = d.e8buf
		d.intelCurPos += frameSize
	case d.intelFileSize != 0:
		d.intelCurPos += frameSize
	}

	d.frame++
	d.framePos += frameSize
	if d.framePos == len(d.window) {
		d.framePos = 0
	}
	if d.windowPos == len(d.window) {
		d.windowPos = 0
	}
	return out, nil
}

func (d *lzx) readBlockHeader() error {
	// An uncompressed block of odd length is followed by a byte of padding.
	if d.blockType == blockUncompressed && d.blockLen&1 == 1 {
		if _, err := d.in.in.byte(); err != nil {
			return err
		}
	}
	typ, err := d.in.read(3)
	if err != nil {
		return err
	}
	hi, err := d.in.read(16)
	if err != nil {
		return err
	}
	lo, err := d.in.read(8)
	if err != nil {
		return err
	}
	d.blockType = int(typ)
	d.blockLen = int(hi<<8 | lo)
	d.blockRemaining = d.blockLen

	switch d.blockType {
	case blockAligned:
		for i := range d.alignedLen {
			v, err := d.in.read(3)
			if err != nil {
				return err
			}
			d.alignedLen[i] = byte(v)
		}
		if err := d.aligned.build(d.alignedLen[:], 7); err != nil {
			return fmt.Errorf("lzx: aligned offset tree: %w", err)
		}
		fallthrough
	case blockVerbatim:
		if err := d.readLengths(d.mainLen[:], 0, numChars); err != nil {
			return err
		}
		if err := d.readLengths(d.mainLen[:], numChars, d.mainSyms); err != nil {
			return err
		}
		if err := d.main.build(d.mainLen[:d.mainSyms], 10); err != nil {
			return fmt.Errorf("lzx: main tree: %w", err)
		}
		if d.mainLen[0xE8] != 0 {
			d.intelStarted = true
		}
		if err := d.readLengths(d.lengthLen[:], 0, numSecondaryLengths); err != nil {
			return err
		}
		// A block with no long matches may send an empty length tree.
		d.lengthEmpty = false
		if err := d.length.build(d.lengthLen[:numSecondaryLengths], 8); err != nil {
			for _, l := range d.lengthLen[:numSecondaryLengths] {
				if l != 0 {
					return fmt.Errorf("lzx: length tree: %w", err)
				}
			}
			d.lengthEmpty = true
		}
	case blockUncompressed:
		d.intelStarted = true // the raw bytes may hold E8s that no tree announced
		// The block's bytes start on a 16-bit boundary, after 1 to 16 bits of
		// padding: the rest of the current word, or a whole word.
		if d.in.n == 0 {
			if err := d.in.need(16); err != nil {
				return err
			}
		}
		d.in.buf, d.in.n = 0, 0
		var r [12]byte
		if err := d.in.in.read(r[:]); err != nil {
			return err
		}
		d.r0 = binary.LittleEndian.Uint32(r[0:])
		d.r1 = binary.LittleEndian.Uint32(r[4:])
		d.r2 = binary.LittleEndian.Uint32(r[8:])
	default:
		return fmt.Errorf("lzx: unknown block type %d", d.blockType)
	}
	return nil
}

// readLengths reads the code lengths lens[first:last]. A pretree of 20 4-bit
// lengths comes first, then pretree symbols: 0 to 16 change a length by that
// delta, modulo 17, from the previous block's; 17 and 18 write runs of zeros;
// 19 writes a run of four or five copies of one changed length.
func (d *lzx) readLengths(lens []byte, first, last int) error {
	for i := range d.pretreeLen {
		v, err := d.in.read(4)
		if err != nil {
			return err
		}
		d.pretreeLen[i] = byte(v)
	}
	if err := d.pretree.build(d.pretreeLen[:], 6); err != nil {
		return fmt.Errorf("lzx: pretree: %w", err)
	}
	// A run may spill up to 51 lengths past last; lens has room for it.
	for x := first; x < last; {
		z, err := d.in.decode(&d.pretree)
		if err != nil {
			return err
		}
		switch z {
		case 17, 18:
			width, base := uint(4), 4
			if z == 18 {
				width, base = 5, 20
			}
			n, err := d.in.read(width)
			if err != nil {
				return err
			}
			for range int(n) + base {
				lens[x] = 0
				x++
			}
		case 19:
			n, err := d.in.read(1)
			if err != nil {
				return err
			}
			z, err := d.in.decode(&d.pretree)
			if err != nil {
				return err
			}
			l := delta(lens[x], z)
			for range int(n) + 4 {
				lens[x] = l
				x++
			}
		default:
			lens[x] = delta(lens[x], z)
			x++
		}
	}
	return nil
}

// delta applies a pretree delta to a previous length. A delta of 17 to 19,
// which only a malformed run can send, leaves a length over 16 behind, exactly
// as lzxd.c's arithmetic does; build then gives that symbol no code.
func delta(prev byte, z int) byte {
	v := int(prev) - z
	if v < 0 {
		v += 17
	}
	return byte(v)
}

// decodeRun decodes run bytes of a verbatim or aligned-offset block.
func (d *lzx) decodeRun(run int) error {
	for run > 0 {
		sym, err := d.in.decode(&d.main)
		if err != nil {
			return err
		}
		if sym < numChars {
			d.window[d.windowPos] = byte(sym)
			d.windowPos++
			run--
			continue
		}
		sym -= numChars
		length := sym & numPrimaryLengths
		if length == numPrimaryLengths {
			if d.lengthEmpty {
				return errors.New("lzx: a match needs the length tree, but the block sent none")
			}
			more, err := d.in.decode(&d.length)
			if err != nil {
				return err
			}
			length += more
		}
		length += minMatch

		var off uint32
		switch slot := sym >> 3; slot {
		case 0:
			off = d.r0
		case 1:
			off = d.r1
			d.r1 = d.r0
			d.r0 = off
		case 2:
			off = d.r2
			d.r2 = d.r0
			d.r0 = off
		default:
			n := uint(extraBits[slot])
			off = positionBase[slot] - 2
			if d.blockType == blockAligned && n >= 3 {
				// The low three bits come from the aligned offset tree.
				if n > 3 {
					v, err := d.in.read(n - 3)
					if err != nil {
						return err
					}
					off += v << 3
				}
				a, err := d.in.decode(&d.aligned)
				if err != nil {
					return err
				}
				off += uint32(a)
			} else if n > 0 {
				v, err := d.in.read(n)
				if err != nil {
					return err
				}
				off += v
			}
			d.r2, d.r1, d.r0 = d.r1, d.r0, off
		}

		// A match never crosses the end of its block or its frame; lzxd.c
		// rejects one that does.
		if length > run {
			return errors.New("lzx: a match runs past the end of its block or frame")
		}
		if err := d.copyMatch(off, length); err != nil {
			return err
		}
		run -= length
	}
	return nil
}

func (d *lzx) copyMatch(off uint32, length int) error {
	w := d.window
	pos := d.windowPos
	dst := w[pos : pos+length]
	if int64(off) <= int64(pos) {
		src := pos - int(off)
		if int(off) >= length {
			copy(dst, w[src:src+length])
		} else {
			for i := range dst { // overlapping: each byte may be one this match wrote
				dst[i] = w[src+i]
			}
		}
	} else {
		// The match reaches back past the start of the window buffer, into the
		// end of it, where the previous pass left its data.
		if int64(off) > int64(d.frame)*frameSize {
			return errors.New("lzx: a match reaches back before the start of the data")
		}
		back := int64(off) - int64(pos)
		if back > int64(len(w)) {
			return errors.New("lzx: a match reaches back further than the window")
		}
		src := len(w) - int(back)
		for i := range dst {
			dst[i] = w[src]
			if src++; src == len(w) {
				src = 0
			}
		}
	}
	d.windowPos += length
	return nil
}

// translateE8 undoes the compressor's x86 preprocessing. E8 is the CALL
// opcode; the compressor turned each call's relative target into an absolute
// one, because absolute targets repeat and compress better. pos is where the
// frame starts in the decoded stream, and size the bound sent in the header.
func translateE8(b []byte, pos, size int32) {
	for i := 0; i < len(b)-10; {
		if b[i] != 0xE8 {
			i++
			pos++
			continue
		}
		abs := int32(binary.LittleEndian.Uint32(b[i+1:]))
		if abs >= -pos && abs < size {
			rel := abs + size
			if abs >= 0 {
				rel = abs - pos
			}
			binary.LittleEndian.PutUint32(b[i+1:], uint32(rel))
		}
		i += 5
		pos += 5
	}
}
