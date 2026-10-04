package chm

import (
	"bytes"
	"testing"
)

// fuzzLimits keep each input cheap: a hostile archive can claim a long stream
// and make a read decode all of it, so the fuzzer caps the stream.
var fuzzLimits = limits{
	entries: 1000,
	name:    1024,
	entry:   1 << 20,
	total:   4 << 20,
	stream:  1 << 20,
}

// smallText is a frame of text that compresses to a few hundred bytes. Small
// seeds keep the fuzzer mutating rather than minimizing.
func smallText(seed uint64) []byte {
	b := bytes.Repeat([]byte("<p>the device register 0x1F</p>\r\n"), frameSize/33+1)[:frameSize]
	copy(b[1000:], textData(400, seed))
	return b
}

func FuzzOpen(f *testing.F) {
	small := []testEntry{
		{name: "/", section: 0},
		{name: "/#SYSTEM", section: 0, data: []byte("system")},
		{name: "/a.htm", section: 1, data: smallText(1)[:3000]},
		{name: "/b.htm", section: 1, data: smallText(2)[:9000]},
		{name: "/c.gif", section: 0, data: noise(100, 3)},
		{name: "/d/", section: 0},
		{name: "/d/e.htm", section: 1, data: []byte("<p>e</p>")},
	}
	for _, spec := range []chmSpec{
		{chunkSize: 512},
		{version: 2, chunkSize: 256, index: true},
		{chunkSize: 256, scramble: true},
		{chunkSize: 512, enc: encOptions{windowBits: 15, resetFrames: 1, mode: modeAligned}},
		{chunkSize: 512, enc: encOptions{mode: modeUncompressed}},
		{chunkSize: 512, enc: encOptions{mode: modeMixed, blockSize: 2000, e8Size: 1 << 20}},
		{chunkSize: 512, tableFrames: -1},
	} {
		f.Add(buildCHM(f, spec, small).data)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		ch, err := open(bytes.NewReader(b), int64(len(b)), fuzzLimits)
		if err != nil {
			return
		}
		for i, e := range ch.Entries() {
			if i == 32 {
				break
			}
			data, err := ch.ReadFile(e.Name)
			if err == nil && int64(len(data)) != e.Size {
				t.Fatalf("%s: read %d bytes, the directory says %d", e.Name, len(data), e.Size)
			}
		}
	})
}

// FuzzLZX runs the decoder on arbitrary input with a fixed window and output
// size: two frames, with a reset between them.
func FuzzLZX(f *testing.F) {
	data := append(smallText(1), smallText(2)...)
	for _, opt := range []encOptions{
		{mode: modeUncompressed, resetFrames: 1, blockSize: 1001},
		{mode: modeVerbatim, resetFrames: 1},
		{mode: modeAligned, resetFrames: 1},
		{mode: modeMixed, resetFrames: 1, blockSize: 3000},
		{mode: modeMixed, resetFrames: 1, blockSize: 5000, e8Size: 1 << 20},
	} {
		comp, _ := encodeLZX(f, data, opt)
		if opt.mode == modeUncompressed {
			comp = comp[:2000] // the rest is the text again, raw
		}
		f.Add(comp)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		in := &stream{r: bytes.NewReader(b), end: int64(len(b))}
		d, err := newLZX(in, 16, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			out, err := d.next()
			if err != nil {
				return
			}
			if len(out) != frameSize {
				t.Fatalf("a frame of %d bytes", len(out))
			}
		}
	})
}
