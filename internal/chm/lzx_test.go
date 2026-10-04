package chm

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"testing"
)

// decodeAll decodes frames of a stream from its start.
func decodeAll(comp []byte, windowBits uint, resetFrames, frames int) ([]byte, error) {
	in := &stream{r: bytes.NewReader(comp), end: int64(len(comp))}
	d, err := newLZX(in, windowBits, resetFrames, nil)
	if err != nil {
		return nil, err
	}
	var out []byte
	for range frames {
		f, err := d.next()
		if err != nil {
			return out, err
		}
		out = append(out, f...)
	}
	return out, nil
}

// textData makes HTML-like text: words from a small vocabulary, table rows of
// fixed width, runs and bursts of noise. A compressor finds literals, short
// and long matches, and the same offsets again and again in it.
func textData(n int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	words := []string{"the", "device", "configuration", "<p>", "</p>", `<td class="cell">`, "</td>",
		"register", "0x1F", "interrupt", "\r\n", "setup", "page", "a", "of", "\xE8\x10\x00\x00\x00"}
	var b []byte
	for len(b) < n {
		switch rng.IntN(12) {
		case 0:
			b = fmt.Appendf(b, "<tr><td>%04d</td><td>%-12s</td></tr>\n", rng.IntN(10000), words[rng.IntN(len(words))])
		case 1:
			b = append(b, bytes.Repeat([]byte{byte('a' + rng.IntN(26))}, 1+rng.IntN(600))...)
		case 2:
			for range 1 + rng.IntN(40) {
				b = append(b, byte(rng.IntN(256)))
			}
		default:
			b = append(b, words[rng.IntN(len(words))]...)
			b = append(b, ' ')
		}
	}
	return b[:n]
}

func noise(n int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, 1))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

func firstDiff(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

func TestLZXRoundTrip(t *testing.T) {
	modes := []struct {
		name string
		mode blockMode
	}{
		{"uncompressed", modeUncompressed},
		{"verbatim", modeVerbatim},
		{"aligned", modeAligned},
		{"mixed", modeMixed},
	}
	for _, bits := range []uint{15, 16, 17, 18, 19, 20, 21} {
		for _, resets := range []int{0, 1, 2, 3} {
			for _, m := range modes {
				t.Run(fmt.Sprintf("2^%d/reset%d/%s", bits, resets, m.name), func(t *testing.T) {
					data := textData(5*frameSize, uint64(bits)*31+uint64(resets))
					comp, _ := encodeLZX(t, data, encOptions{windowBits: bits, resetFrames: resets, mode: m.mode})
					got, err := decodeAll(comp, bits, resets, 5)
					if err != nil {
						t.Fatalf("decode: %v", err)
					}
					if !bytes.Equal(got, data) {
						t.Fatalf("round trip differs at byte %d of %d", firstDiff(got, data), len(data))
					}
				})
			}
		}
	}
}

// TestLZXDecodeFromResetPoint starts decoding where the compressor reset, as
// a reader does to reach an entry in the middle of the stream.
func TestLZXDecodeFromResetPoint(t *testing.T) {
	data := textData(8*frameSize, 5)
	comp, frames := encodeLZX(t, data, encOptions{resetFrames: 2, mode: modeMixed})
	for k := 0; k < 8; k += 2 {
		in := &stream{r: bytes.NewReader(comp), next: frames[k], end: int64(len(comp))}
		d, err := newLZX(in, 16, 2, nil)
		if err != nil {
			t.Fatal(err)
		}
		for j := k; j < 8; j++ {
			f, err := d.next()
			if err != nil {
				t.Fatalf("from frame %d, frame %d: %v", k, j, err)
			}
			if !bytes.Equal(f, data[j*frameSize:(j+1)*frameSize]) {
				t.Fatalf("from frame %d, frame %d differs", k, j)
			}
		}
	}
}

// op is one step of a hand-made stream: a literal, or a copy of length bytes
// from off back. A repeat names which of R0 to R2 it uses; off is what the test
// expects that register to hold, so the expected output does not depend on the
// decoder's bookkeeping.
type op struct {
	lit    byte
	length int
	off    int
	repeat int // -1 for an explicit offset
}

func lit(s string) []op {
	var ops []op
	for i := range len(s) {
		ops = append(ops, op{lit: s[i]})
	}
	return ops
}

func cp(length, off int) op     { return op{length: length, off: off, repeat: -1} }
func rep(length, k, off int) op { return op{length: length, off: off, repeat: k} }

func tokenFor(o op) token {
	switch {
	case o.length == 0:
		return token{lit: o.lit}
	case o.repeat >= 0:
		return token{length: o.length, slot: o.repeat}
	}
	return explicit(o.length, o.off)
}

// layout turns ops into tokens and the bytes they should decode to. An op
// that would cross a frame's end is preceded by literal zeros up to it, and
// the last frame is filled out the same way.
func layout(ops []op) ([]token, []byte) {
	return layoutAfter(nil, ops)
}

// layoutAfter is layout for ops that follow history, already sent.
func layoutAfter(history []byte, ops []op) ([]token, []byte) {
	var toks []token
	out := bytes.Clone(history)
	pad := func(to int) {
		for len(out) < to {
			toks = append(toks, token{})
			out = append(out, 0)
		}
	}
	for _, o := range ops {
		if o.length == 0 {
			toks = append(toks, token{lit: o.lit})
			out = append(out, o.lit)
			continue
		}
		if end := (len(out)/frameSize + 1) * frameSize; len(out)+o.length > end {
			pad(end)
		}
		toks = append(toks, tokenFor(o))
		for range o.length {
			out = append(out, out[len(out)-o.off])
		}
	}
	pad((len(out) + frameSize - 1) / frameSize * frameSize)
	return toks, out
}

// craft encodes hand-made blocks after one reset, with a window of 2^bits.
func craft(t *testing.T, bits uint, typ int, blocks ...[]token) []byte {
	e := newEncoder(t, encOptions{windowBits: bits})
	e.reset()
	pos := 0
	for _, toks := range blocks {
		e.writeCompressed(typ, toks, pos)
		for _, tk := range toks {
			pos += max(tk.length, 1)
		}
	}
	return e.w.out
}

func checkCrafted(t *testing.T, bits uint, typ int, toks []token, want []byte) {
	t.Helper()
	comp := craft(t, bits, typ, toks)
	got, err := decodeAll(comp, bits, 0, len(want)/frameSize)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("differs at byte %d of %d", firstDiff(got, want), len(want))
	}
}

// TestLZXRepeatedOffsets walks the repeated-offset registers through every
// move: an explicit offset pushes R0 down, a repeat of R1 or R2 swaps it with
// R0, and a repeat of R0 changes nothing.
func TestLZXRepeatedOffsets(t *testing.T) {
	ops := lit("0123456789")
	ops = append(ops,
		cp(5, 10),     // R = 10 1 1
		cp(4, 3),      // R = 3 10 1, and the copy overlaps itself
		rep(3, 1, 10), // R = 10 3 1
		rep(2, 2, 1),  // R = 1 3 10
		rep(4, 0, 1),  // unchanged
		rep(3, 2, 10), // R = 10 3 1
		rep(5, 1, 3),  // R = 3 10 1
		rep(6, 0, 3),
	)
	ops = append(ops, lit("end")...)
	toks, want := layout(ops)
	for _, typ := range []int{blockVerbatim, blockAligned} {
		checkCrafted(t, 16, typ, toks, want)
	}
}

// TestLZXUncompressedBlockSetsRepeats: an uncompressed block's header carries
// R0 to R2, and the blocks after it repeat those offsets.
func TestLZXUncompressedBlockSetsRepeats(t *testing.T) {
	head := []byte("abcdefghijklmnop")
	e := newEncoder(t, encOptions{})
	e.reset()
	e.r = [3]uint32{7, 9, 11} // written into the header; no match has used them
	e.writeUncompressed(head, 0)
	ops := []op{rep(4, 0, 7), rep(5, 1, 9), rep(6, 2, 11)}
	toks, tail := layout(append(lit(string(head)), ops...))
	e.writeCompressed(blockVerbatim, toks[len(head):], len(head))
	got, err := decodeAll(e.w.out, 16, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, tail) {
		t.Fatalf("differs at byte %d", firstDiff(got, tail))
	}
}

// TestLZXMatchLengths sends every match length, 2 to 257, each followed by a
// marker literal, so a length decoded wrong shows in the output.
func TestLZXMatchLengths(t *testing.T) {
	ops := lit("abcdefghi")
	for l := 2; l <= 257; l++ {
		if l == 2 {
			ops = append(ops, cp(l, 9))
		} else {
			ops = append(ops, rep(l, 0, 9))
		}
		ops = append(ops, lit("X")...)
	}
	toks, want := layout(ops)
	checkCrafted(t, 16, blockVerbatim, toks, want)
	checkCrafted(t, 16, blockAligned, toks, want)
}

// TestLZXFarOffsets fills a 2 MiB window, then copies from the smallest and
// the largest offset of every position slot: up to the whole window back,
// which takes the last slot and all 17 of its footer bits.
func TestLZXFarOffsets(t *testing.T) {
	const window = 1 << 21
	history := noise(window, 9)
	var ops []op
	for slot := 3; slot < positionSlots[len(positionSlots)-1]; slot++ {
		lo := int(positionBase[slot]) - 2
		hi := int(positionBase[slot+1]) - 3
		ops = append(ops, cp(3+slot, lo), cp(4+slot%7, hi))
	}
	toks, want := layoutAfter(history, ops)
	for _, typ := range []int{blockVerbatim, blockAligned} {
		e := newEncoder(t, encOptions{windowBits: 21})
		e.reset()
		e.writeUncompressed(history, 0)
		e.writeCompressed(typ, toks, window)
		got, err := decodeAll(e.w.out, 21, 0, len(want)/frameSize)
		if err != nil {
			t.Fatalf("block type %d: %v", typ, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("block type %d: differs at byte %d of %d", typ, firstDiff(got, want), len(want))
		}
	}
}

// TestLZXUncompressedPadding: an uncompressed block's bytes start after 1 to
// 16 bits of padding, a whole word when the header ended on a word boundary.
// Both cases must decode.
func TestLZXUncompressedPadding(t *testing.T) {
	var whole, part int
	for size := 300; size < 340; size++ {
		data := textData(2*frameSize, uint64(size))
		e := newEncoder(t, encOptions{mode: modeMixed, blockSize: size})
		comp := e.encode(data)
		whole += e.wholeWordPads
		part += e.partPads
		got, err := decodeAll(comp, 16, 0, 2)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("block size %d: %v", size, err)
		}
	}
	if whole == 0 || part == 0 {
		t.Fatalf("padding by a whole word %d times and by part of one %d times; the test needs both", whole, part)
	}
}

// TestLZXOddBlockAtReset: an uncompressed block of odd length owes a pad byte,
// but a reset right after it forgets the debt, as lzxd.c does.
func TestLZXOddBlockAtReset(t *testing.T) {
	data := textData(4*frameSize, 3)
	comp, _ := encodeLZX(t, data, encOptions{resetFrames: 1, mode: modeUncompressed, blockSize: frameSize - 1, oddTail: true})
	got, err := decodeAll(comp, 16, 1, 4)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("decode: %v", err)
	}
}

func TestTranslateE8(t *testing.T) {
	b := make([]byte, 40)
	put := func(i int, v int32) {
		b[i] = 0xE8
		binary.LittleEndian.PutUint32(b[i+1:], uint32(v))
	}
	put(0, 500)   // at position 100: absolute 500 becomes relative 400
	put(5, -50)   // negative and within reach: -50 + size
	put(10, 2000) // at or past size: left alone
	put(15, -200) // further back than the start: left alone
	put(31, 7)    // in the last ten bytes: never looked at
	translateE8(b, 100, 1000)
	want := []int32{400, 950, 2000, -200}
	for i, at := range []int{0, 5, 10, 15} {
		if got := int32(binary.LittleEndian.Uint32(b[at+1:])); got != want[i] {
			t.Errorf("call at %d: %d, want %d", at, got, want[i])
		}
	}
	if got := int32(binary.LittleEndian.Uint32(b[32:])); got != 7 {
		t.Errorf("a call in the last ten bytes changed to %d", got)
	}
}

// e8Data is noise sprinkled with CALL instructions whose targets land on both
// sides of every bound translateE8 checks.
func e8Data(n int, size int32, seed uint64) []byte {
	b := textData(n, seed)
	rng := rand.New(rand.NewPCG(seed, 2))
	for i := 0; i+5 <= n; i += 5 + rng.IntN(60) {
		pos := int32(i % frameSize)
		targets := []int32{0, 1, -1, size - 1, size, -pos, -pos - 1, size - pos, rng.Int32(), -rng.Int32N(1 << 20)}
		b[i] = 0xE8
		binary.LittleEndian.PutUint32(b[i+1:], uint32(targets[rng.IntN(len(targets))]))
	}
	return b
}

func TestLZXE8RoundTrip(t *testing.T) {
	const size = 6_000_000
	for _, mode := range []blockMode{modeUncompressed, modeVerbatim, modeAligned, modeMixed} {
		for _, resets := range []int{0, 2} {
			data := e8Data(4*frameSize, size, uint64(mode))
			comp, _ := encodeLZX(t, data, encOptions{mode: mode, resetFrames: resets, e8Size: size})
			got, err := decodeAll(comp, 16, resets, 4)
			if err != nil {
				t.Fatalf("mode %d, resets %d: %v", mode, resets, err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("mode %d, resets %d: differs at byte %d", mode, resets, firstDiff(got, data))
			}
		}
	}
}

// TestReadLengthsSpill: a run may spill past the range being read, and the
// spilled lengths become the next range's delta base, as in lzxd.c. A delta
// past 16 leaves a length over 16 behind, which gives its symbol no code.
func TestReadLengthsSpill(t *testing.T) {
	// A pretree where symbols 0 to 11 take 4 bits and 12 to 19 take 5.
	var pre [pretreeSyms]byte
	for i := range pre {
		pre[i] = 4
		if i >= 12 {
			pre[i] = 5
		}
	}
	codes := canonical(pre[:])
	var w bitWriter
	sym := func(s int) { w.bits(codes[s], uint(pre[s])) }
	writePretree := func() {
		for _, l := range pre {
			w.bits(uint32(l), 4)
		}
	}
	writePretree()
	sym(18) // 51 zeros from position 250 of the range 0..256: 45 spill past it
	w.bits(31, 5)
	writePretree()
	sym(0) // position 256: delta 0 against the spilled zero
	sym(19)
	w.bits(0, 1) // four copies of
	sym(18)      // delta 18 against zero: -1, which wraps to 255
	w.bits(0, 16)
	w.bits(0, 16)

	d, err := newLZX(&stream{r: bytes.NewReader(w.out), end: int64(len(w.out))}, 16, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	lens := d.mainLen[:]
	for i := range lens {
		lens[i] = 7
	}
	if err := d.readLengths(lens, 250, 256); err != nil {
		t.Fatal(err)
	}
	for i := 250; i < 301; i++ {
		if lens[i] != 0 {
			t.Fatalf("length %d is %d, want the spilled 0", i, lens[i])
		}
	}
	if lens[301] != 7 {
		t.Fatalf("length 301 is %d; the run stops at 51", lens[301])
	}
	if err := d.readLengths(lens, 256, 261); err != nil {
		t.Fatal(err)
	}
	if lens[256] != 0 {
		t.Errorf("length 256 is %d, want 0 (delta 0 against the spill)", lens[256])
	}
	for i := 257; i < 261; i++ {
		if lens[i] != 255 {
			t.Errorf("length %d is %d, want 255", i, lens[i])
		}
	}
	var h huffman
	lens2 := []byte{1, 255, 1, 255} // two real codes; the 255s take none
	if err := h.build(lens2, 6); err != nil {
		t.Fatalf("a length over 16 should be ignored: %v", err)
	}
}

// TestHuffmanLongCodes decodes every symbol of a code whose lengths run from 1
// to 16, longer than any lookup table, so the slow path decodes some.
func TestHuffmanLongCodes(t *testing.T) {
	lens := make([]byte, 17)
	for i := range 15 {
		lens[i] = byte(i + 1)
	}
	lens[15], lens[16] = 16, 16
	codes := canonical(lens)
	var w bitWriter
	order := []int{16, 0, 15, 7, 14, 1, 13, 12, 2, 11, 3, 10, 4, 9, 5, 8, 6}
	for _, s := range order {
		w.bits(codes[s], uint(lens[s]))
	}
	w.bits(0, 16)
	for _, bits := range []uint{6, 10} {
		var h huffman
		if err := h.build(lens, bits); err != nil {
			t.Fatal(err)
		}
		br := bitReader{in: &stream{r: bytes.NewReader(w.out), end: int64(len(w.out))}}
		for _, want := range order {
			got, err := br.decode(&h)
			if err != nil || got != want {
				t.Fatalf("table bits %d: decoded %d, %v; want %d", bits, got, err, want)
			}
		}
	}
}

func TestHuffmanRejectsIncompleteCodes(t *testing.T) {
	var h huffman
	for name, lens := range map[string][]byte{
		"empty":            make([]byte, 8),
		"one code":         {1, 0, 0},
		"oversubscribed":   {1, 1, 1},
		"incomplete":       {1, 2, 0, 0},
		"long and short":   {2, 2, 2, 3},
		"over by a little": {1, 2, 3, 3, 3},
	} {
		if err := h.build(lens, 6); err == nil {
			t.Errorf("%s: built a code from %v", name, lens)
		}
	}
}

// TestLZXMalformed feeds streams that break each rule the decoder checks. Every
// one must end in an error, never a panic or an endless loop.
func TestLZXMalformed(t *testing.T) {
	good := textData(2*frameSize, 11)
	header := func(typ uint32, length uint32) []byte {
		var w bitWriter
		w.bits(0, 1)
		w.bits(typ, 3)
		w.bits(length, 24)
		for range 64 {
			w.bits(0x5A5A, 16)
		}
		return w.out
	}
	pretree := func(lens ...uint32) []byte {
		var w bitWriter
		w.bits(0, 1)
		w.bits(blockVerbatim, 3)
		w.bits(100, 24)
		for i := range pretreeSyms {
			w.bits(lens[i%len(lens)], 4)
		}
		w.bits(0, 16)
		return w.out
	}
	withLens := func(edit func(main, length []byte)) []byte {
		e := newEncoder(t, encOptions{mode: modeVerbatim, blockSize: frameSize})
		e.lens = edit
		return e.encode(good)
	}
	// An uncompressed block's header can set R0 to anything; here, further
	// back than a 32 KiB window reaches, after four frames have wrapped it.
	pastWindow := func() []byte {
		e := newEncoder(t, encOptions{windowBits: 15})
		e.reset()
		e.writeCompressed(blockVerbatim, slicesRepeat(token{lit: 'a'}, 4*frameSize), 0)
		e.r = [3]uint32{100_000, 1, 1}
		e.writeUncompressed([]byte("ab"), 4*frameSize)
		e.writeCompressed(blockVerbatim, []token{{length: 4, slot: 0}}, 4*frameSize+2)
		return e.w.out
	}
	cases := []struct {
		name   string
		comp   []byte
		bits   uint
		frames int
	}{
		{"block type 0", header(0, 100), 16, 2},
		{"block type 4", header(4, 100), 16, 2},
		{"block type 7", header(7, 100), 16, 2},
		{"empty pretree", pretree(0), 16, 2},
		{"oversubscribed pretree", pretree(1), 16, 2},
		{"oversubscribed main tree", withLens(func(main, _ []byte) {
			for i := range main {
				main[i] = 1
			}
		}), 16, 2},
		{"incomplete main tree", withLens(func(main, _ []byte) {
			for i, l := range main {
				if l > 0 {
					main[i] = l + 1
					return
				}
			}
		}), 16, 2},
		{"missing length tree", withLens(func(_, length []byte) { clear(length) }), 16, 2},
		{"match before the start", craft(t, 16, blockVerbatim, []token{explicit(3, 5)}), 16, 1},
		{"match across a frame", craft(t, 16, blockVerbatim,
			append(slicesRepeat(token{lit: 'a'}, frameSize-2), explicit(5, 1))), 16, 2},
		{"match past the window", pastWindow(), 15, 5},
		{"truncated", func() []byte {
			comp, _ := encodeLZX(t, good, encOptions{mode: modeMixed})
			return comp[:len(comp)/2]
		}(), 16, 2},
		{"empty", nil, 16, 1},
		{"all ones", bytes.Repeat([]byte{0xFF}, 5000), 16, 2},
		{"all zeros", make([]byte, 5000), 16, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := decodeAll(c.comp, c.bits, 0, c.frames); err == nil {
				t.Fatal("decoded without an error")
			}
		})
	}
	for _, bits := range []uint{14, 22} {
		if _, err := newLZX(&stream{}, bits, 0, nil); err == nil {
			t.Errorf("accepted a window of 2^%d", bits)
		}
	}
}

func slicesRepeat(t token, n int) []token {
	out := make([]token, n)
	for i := range out {
		out[i] = t
	}
	return out
}
