package chm

import (
	"encoding/binary"
	"slices"
	"testing"
)

// A minimal LZX compressor, for tests only. It writes every kind of block the
// decoder reads, so round trips exercise the decoder's Huffman tables, its
// delta-coded lengths, its matches and repeated offsets, and E8 translation.

type blockMode int

const (
	modeUncompressed blockMode = iota
	modeVerbatim
	modeAligned
	modeMixed // verbatim, aligned and uncompressed blocks in turn
)

type encOptions struct {
	windowBits  uint // 16 when zero
	resetFrames int  // frames from one reset to the next; 0 for none
	mode        blockMode
	blockSize   int   // output bytes per block; 0 cycles through odd and even sizes
	e8Size      int32 // E8 translation's file size; 0 leaves it off
	oddTail     bool  // let an odd uncompressed block end a reset interval
}

// bitWriter writes LZX's bitstream: bits from the top of each 16-bit word, the
// words little-endian.
type bitWriter struct {
	out []byte
	cur uint32
	n   uint
}

func (w *bitWriter) bits(v uint32, n uint) {
	for n > 0 {
		take := min(n, 16-w.n)
		w.cur = w.cur<<take | (v>>(n-take))&(1<<take-1)
		w.n += take
		n -= take
		if w.n == 16 {
			w.out = append(w.out, byte(w.cur), byte(w.cur>>8))
			w.cur, w.n = 0, 0
		}
	}
}

// align pads the current word with zeros, which the decoder skips at a
// frame's end.
func (w *bitWriter) align() {
	if w.n > 0 {
		w.bits(0, 16-w.n)
	}
}

type encoder struct {
	t          testing.TB
	opt        encOptions
	w          bitWriter
	window     int
	slots      int
	mainLen    []byte // the last block's lengths, the base of the next block's deltas
	lengthLen  []byte
	r          [3]uint32
	padPending bool // an odd uncompressed block still owes its pad byte
	blocks     int
	frames     []int64 // where each frame's compressed data begins

	// Tests that need a malformed or unusual stream set these.
	lens          func(main, length []byte) // edits a block's code lengths before they are sent
	wholeWordPads int                       // uncompressed blocks padded by a whole word
	partPads      int                       // and by the rest of a word
}

func newEncoder(t testing.TB, opt encOptions) *encoder {
	if opt.windowBits == 0 {
		opt.windowBits = 16
	}
	e := &encoder{
		t:      t,
		opt:    opt,
		window: 1 << opt.windowBits,
		slots:  positionSlots[opt.windowBits-minWindowBits],
		frames: []int64{0},
	}
	e.mainLen = make([]byte, numChars+e.slots*8)
	e.lengthLen = make([]byte, numSecondaryLengths)
	return e
}

// encodeLZX compresses whole frames of data. It returns the stream and the
// compressed offset of every frame, which is what a reset table lists.
func encodeLZX(t testing.TB, data []byte, opt encOptions) ([]byte, []int64) {
	t.Helper()
	e := newEncoder(t, opt)
	return e.encode(data), e.frames[:len(data)/frameSize]
}

func (e *encoder) encode(data []byte) []byte {
	e.t.Helper()
	if len(data)%frameSize != 0 {
		e.t.Fatalf("encodeLZX: %d bytes is not whole frames", len(data))
	}
	src := data
	if e.opt.e8Size != 0 {
		src = e8Encode(data, e.opt.e8Size)
	}
	interval := len(src)
	if e.opt.resetFrames > 0 {
		interval = e.opt.resetFrames * frameSize
	}
	for start := 0; start < len(src); start += interval {
		e.encodeInterval(src, start, min(start+interval, len(src)))
	}
	return e.w.out
}

// reset does what a reset does in the decoder, then sends the E8 header. The
// decoder also forgets an odd uncompressed block's pad byte, so none is sent.
func (e *encoder) reset() {
	e.r = [3]uint32{1, 1, 1}
	clear(e.mainLen)
	clear(e.lengthLen)
	e.padPending = false
	if e.opt.e8Size != 0 {
		e.w.bits(1, 1)
		e.w.bits(uint32(e.opt.e8Size)>>16, 16)
		e.w.bits(uint32(e.opt.e8Size)&0xFFFF, 16)
	} else {
		e.w.bits(0, 1)
	}
}

// encodeInterval encodes one reset interval, which refers to nothing before it.
func (e *encoder) encodeInterval(src []byte, start, end int) {
	e.reset()
	m := newMatcher(src, start)
	for pos := start; pos < end; {
		be := min(end, pos+e.blockSize())
		typ := e.blockType()
		if typ == blockUncompressed && be == end && (be-pos)&1 == 1 && !e.opt.oddTail {
			typ = blockVerbatim
		}
		if typ == blockUncompressed {
			e.writeUncompressed(src[pos:be], pos)
		} else {
			e.writeCompressed(typ, e.tokenize(m, pos, be), pos)
		}
		pos = be
	}
}

func (e *encoder) blockSize() int {
	if e.opt.blockSize > 0 {
		return e.opt.blockSize
	}
	sizes := []int{20000, 7001, 45000, 999, 32768, 3, 70001}
	return sizes[e.blocks%len(sizes)]
}

func (e *encoder) blockType() int {
	e.blocks++
	switch e.opt.mode {
	case modeUncompressed:
		return blockUncompressed
	case modeVerbatim:
		return blockVerbatim
	case modeAligned:
		return blockAligned
	}
	return []int{blockVerbatim, blockAligned, blockUncompressed}[(e.blocks-1)%3]
}

// frameEnd is called as the output reaches pos. At a frame's end the decoder
// realigns to a 16-bit word, and the next frame's compressed data begins.
func (e *encoder) frameEnd(pos int) {
	if pos%frameSize == 0 {
		e.w.align()
		e.frames = append(e.frames, int64(len(e.w.out)))
	}
}

func (e *encoder) startBlock(typ, length int) {
	if e.padPending {
		e.w.out = append(e.w.out, 0)
		e.padPending = false
	}
	e.w.bits(uint32(typ), 3)
	e.w.bits(uint32(length), 24)
}

func (e *encoder) writeUncompressed(data []byte, pos int) {
	e.startBlock(blockUncompressed, len(data))
	if e.w.n == 0 { // 1 to 16 bits of padding: a whole word when already aligned
		e.w.bits(0, 16)
		e.wholeWordPads++
	} else {
		e.w.align()
		e.partPads++
	}
	for _, r := range e.r {
		e.w.out = binary.LittleEndian.AppendUint32(e.w.out, r)
	}
	e.padPending = len(data)&1 == 1
	for len(data) > 0 {
		take := min(len(data), frameSize-pos%frameSize)
		e.w.out = append(e.w.out, data[:take]...)
		data = data[take:]
		pos += take
		e.frameEnd(pos)
	}
}

// token is a literal (length 0) or a match.
type token struct {
	length int
	lit    byte
	slot   int    // 0 to 2 repeat R0 to R2
	footer uint32 // the offset's footer bits, for slots past 2
	width  uint
}

// matcher finds matches through hash chains of 3-byte prefixes, within one
// reset interval.
type matcher struct {
	src   []byte
	start int
	head  []int32
	prev  []int32
	next  int
}

func newMatcher(src []byte, start int) *matcher {
	m := &matcher{src: src, start: start, head: make([]int32, 1<<15), prev: make([]int32, len(src)-start), next: start}
	for i := range m.head {
		m.head[i] = -1
	}
	return m
}

func hash3(b []byte) int {
	return int((uint32(b[0])<<16|uint32(b[1])<<8|uint32(b[2]))*2654435761>>17) & (1<<15 - 1)
}

func (m *matcher) insertUpTo(pos int) {
	for ; m.next < pos; m.next++ {
		if m.next+3 > len(m.src) {
			continue
		}
		h := hash3(m.src[m.next:])
		m.prev[m.next-m.start] = m.head[h]
		m.head[h] = int32(m.next)
	}
}

func matchLen(src []byte, pos, off, limit int) int {
	n := 0
	for n < limit && src[pos+n] == src[pos+n-off] {
		n++
	}
	return n
}

// tokenize parses src[bs:be] greedily. A match never crosses a frame's end,
// and a repeated offset wins a tie, since it costs no footer.
func (e *encoder) tokenize(m *matcher, bs, be int) []token {
	var toks []token
	for pos := bs; pos < be; {
		m.insertUpTo(pos)
		limit := min(257, be-pos, frameSize-pos%frameSize)
		maxOff := min(e.window-3, pos-m.start)
		bestLen, bestOff, bestRep := 0, 0, -1
		for k, r := range e.r {
			if off := int(r); off >= 1 && off <= maxOff {
				if l := matchLen(m.src, pos, off, limit); l >= 2 && l > bestLen {
					bestLen, bestOff, bestRep = l, off, k
				}
			}
		}
		if limit >= 3 {
			steps := 0
			for cand := m.head[hash3(m.src[pos:])]; cand >= 0 && steps < 48; cand = m.prev[int(cand)-m.start] {
				steps++
				off := pos - int(cand)
				if off > maxOff {
					break
				}
				if l := matchLen(m.src, pos, off, limit); l > bestLen {
					bestLen, bestOff, bestRep = l, off, -1
				}
			}
		}
		if bestLen < 2 || (bestRep < 0 && bestLen < 3) {
			toks = append(toks, token{lit: m.src[pos]})
			pos++
			continue
		}
		t := token{length: bestLen, slot: bestRep}
		switch bestRep {
		case 0:
		case 1:
			e.r[0], e.r[1] = e.r[1], e.r[0]
		case 2:
			e.r[0], e.r[2] = e.r[2], e.r[0]
		default:
			t = explicit(bestLen, bestOff)
			e.r[2], e.r[1], e.r[0] = e.r[1], e.r[0], uint32(bestOff)
		}
		toks = append(toks, t)
		pos += bestLen
	}
	return toks
}

// explicit is a match at an explicit offset: the offset plus 2, sent as a
// position slot and that slot's footer bits.
func explicit(length, off int) token {
	formatted := uint32(off + 2)
	slot := 3
	for positionBase[slot+1] <= formatted {
		slot++
	}
	return token{length: length, slot: slot, footer: formatted - positionBase[slot], width: uint(extraBits[slot])}
}

func (e *encoder) writeCompressed(typ int, toks []token, pos int) {
	mainFreq := make([]int, numChars+e.slots*8)
	lengthFreq := make([]int, numSecondaryLengths)
	alignedFreq := make([]int, alignedSyms)
	length := 0
	for _, t := range toks {
		if t.length == 0 {
			mainFreq[t.lit]++
			length++
			continue
		}
		h := min(t.length-minMatch, numPrimaryLengths)
		mainFreq[numChars+t.slot*8+h]++
		if h == numPrimaryLengths {
			lengthFreq[t.length-minMatch-numPrimaryLengths]++
		}
		if typ == blockAligned && t.slot >= 3 && t.width >= 3 {
			alignedFreq[t.footer&7]++
		}
		length += t.length
	}
	mainLens := huffLengths(complete(mainFreq), maxCodeLen)
	lengthLens := huffLengths(lengthFreq, maxCodeLen) // empty when no match needs it
	if used(lengthFreq) == 1 {
		lengthLens = huffLengths(complete(lengthFreq), maxCodeLen)
	}
	if e.lens != nil {
		e.lens(mainLens, lengthLens)
	}

	e.startBlock(typ, length)
	var alignedLens []byte
	if typ == blockAligned {
		for i := range alignedFreq {
			alignedFreq[i]++ // every footer value gets a code, so the tree is complete
		}
		alignedLens = huffLengths(alignedFreq, 7)
		for _, l := range alignedLens {
			e.w.bits(uint32(l), 3)
		}
	}
	e.writeLengths(e.mainLen[:numChars], mainLens[:numChars])
	e.writeLengths(e.mainLen[numChars:], mainLens[numChars:])
	e.writeLengths(e.lengthLen, lengthLens)
	copy(e.mainLen, mainLens)
	copy(e.lengthLen, lengthLens)

	mainCodes, lengthCodes, alignedCodes := canonical(mainLens), canonical(lengthLens), canonical(alignedLens)
	for _, t := range toks {
		if t.length == 0 {
			e.w.bits(mainCodes[t.lit], uint(mainLens[t.lit]))
			pos++
			e.frameEnd(pos)
			continue
		}
		h := min(t.length-minMatch, numPrimaryLengths)
		sym := numChars + t.slot*8 + h
		e.w.bits(mainCodes[sym], uint(mainLens[sym]))
		if h == numPrimaryLengths {
			s := t.length - minMatch - numPrimaryLengths
			e.w.bits(lengthCodes[s], uint(lengthLens[s]))
		}
		if t.slot >= 3 {
			switch {
			case typ == blockAligned && t.width >= 3:
				if t.width > 3 {
					e.w.bits(t.footer>>3, t.width-3)
				}
				a := t.footer & 7
				e.w.bits(alignedCodes[a], uint(alignedLens[a]))
			case t.width > 0:
				e.w.bits(t.footer, t.width)
			}
		}
		pos += t.length
		e.frameEnd(pos)
	}
}

// writeLengths sends code lengths as pretree operations against the previous
// block's: runs of zeros as 17 or 18, runs of one length as 19, the rest as
// single deltas.
func (e *encoder) writeLengths(prev, cur []byte) {
	type op struct {
		sym, extra int
		width      uint
		sym2       int
	}
	deltaOf := func(x int) int { return (int(prev[x]) - int(cur[x]) + 17) % 17 }
	var ops []op
	freq := make([]int, pretreeSyms)
	for x := 0; x < len(cur); {
		run := 1
		for x+run < len(cur) && cur[x+run] == cur[x] {
			run++
		}
		var o op
		switch {
		case cur[x] == 0 && run >= 20:
			n := min(run, 51)
			o = op{sym: 18, extra: n - 20, width: 5}
			x += n
		case cur[x] == 0 && run >= 4:
			n := min(run, 19)
			o = op{sym: 17, extra: n - 4, width: 4}
			x += n
		case run >= 4:
			n := min(run, 5)
			o = op{sym: 19, extra: n - 4, width: 1, sym2: deltaOf(x)}
			freq[o.sym2]++
			x += n
		default:
			o = op{sym: deltaOf(x)}
			x++
		}
		freq[o.sym]++
		ops = append(ops, o)
	}
	lens := huffLengths(complete(freq), 15)
	for _, l := range lens {
		e.w.bits(uint32(l), 4)
	}
	codes := canonical(lens)
	for _, o := range ops {
		e.w.bits(codes[o.sym], uint(lens[o.sym]))
		if o.width > 0 {
			e.w.bits(uint32(o.extra), o.width)
		}
		if o.sym == 19 {
			e.w.bits(codes[o.sym2], uint(lens[o.sym2]))
		}
	}
}

func used(freq []int) int {
	n := 0
	for _, f := range freq {
		if f > 0 {
			n++
		}
	}
	return n
}

// complete gives a second symbol a count when only one has any, since LZX
// accepts only complete codes and a lone symbol's code would not be one.
func complete(freq []int) []int {
	freq = slices.Clone(freq)
	for i := 0; used(freq) < 2 && i < len(freq); i++ {
		if freq[i] == 0 {
			freq[i] = 1
		}
	}
	return freq
}

// huffLengths computes Huffman code lengths no longer than limit, halving the
// counts until the tree fits.
func huffLengths(freq []int, limit int) []byte {
	lens := make([]byte, len(freq))
	f := slices.Clone(freq)
	for {
		var leaves []int
		for s, w := range f {
			if w > 0 {
				leaves = append(leaves, s)
			}
		}
		switch len(leaves) {
		case 0:
			return lens
		case 1:
			lens[leaves[0]] = 1
			return lens
		}
		slices.SortStableFunc(leaves, func(a, b int) int { return f[a] - f[b] })
		n := len(leaves)
		weights := make([]int, n, 2*n-1)
		for i, s := range leaves {
			weights[i] = f[s]
		}
		parent := make([]int, 2*n-1)
		nextLeaf, nextNode := 0, n
		pick := func() int {
			if nextLeaf < n && (nextNode >= len(weights) || weights[nextLeaf] <= weights[nextNode]) {
				nextLeaf++
				return nextLeaf - 1
			}
			nextNode++
			return nextNode - 1
		}
		for range n - 1 {
			a, b := pick(), pick()
			weights = append(weights, weights[a]+weights[b])
			parent[a], parent[b] = len(weights)-1, len(weights)-1
		}
		depth := make([]int, len(weights))
		deepest := 0
		for k := len(weights) - 2; k >= 0; k-- {
			depth[k] = depth[parent[k]] + 1
			if k < n {
				deepest = max(deepest, depth[k])
			}
		}
		if deepest <= limit {
			for k, s := range leaves {
				lens[s] = byte(depth[k])
			}
			return lens
		}
		for s := range f {
			if f[s] > 0 {
				f[s] = (f[s] + 1) / 2
			}
		}
	}
}

// canonical assigns the canonical codes the decoder expects: shorter codes
// first, and within a length, lower symbols first.
func canonical(lens []byte) []uint32 {
	var count [maxCodeLen + 1]uint32
	for _, l := range lens {
		if l > 0 {
			count[l]++
		}
	}
	var next [maxCodeLen + 1]uint32
	var code uint32
	for l := 1; l <= maxCodeLen; l++ {
		code = (code + count[l-1]) << 1
		next[l] = code
	}
	codes := make([]uint32, len(lens))
	for s, l := range lens {
		if l > 0 {
			codes[s] = next[l]
			next[l]++
		}
	}
	return codes
}

// e8Encode applies the compressor's x86 preprocessing, which translateE8
// undoes: each CALL's relative target becomes an absolute one. Positions count
// from the start of the stream, where the test decoders begin.
func e8Encode(data []byte, size int32) []byte {
	out := slices.Clone(data)
	for f := 0; f*frameSize < len(out) && f < e8Frames; f++ {
		b := out[f*frameSize : (f+1)*frameSize]
		pos := int32(f * frameSize)
		for i := 0; i < len(b)-10; {
			if b[i] != 0xE8 {
				i++
				pos++
				continue
			}
			rel := int32(binary.LittleEndian.Uint32(b[i+1:]))
			if rel >= -pos && rel < size {
				abs := rel - size
				if rel < size-pos {
					abs = rel + pos
				}
				binary.LittleEndian.PutUint32(b[i+1:], uint32(abs))
			}
			i += 5
			pos += 5
		}
	}
	return out
}
