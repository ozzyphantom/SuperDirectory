package chm

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestExternalDecoders checks the test compressor against decoders written by
// others: 7-Zip (7zz), for cabinets and CHM archives, and gcab, for cabinets,
// whose LZX decoder descends from cabextract and libmspack. The round trips
// prove this package's decoder agrees with the test compressor; this proves the
// compressor writes what the format means. It runs only with CHM_EXTERNAL=1,
// and skips a tool that is not installed.
func TestExternalDecoders(t *testing.T) {
	if os.Getenv("CHM_EXTERNAL") == "" {
		t.Skip("set CHM_EXTERNAL=1 to compare with 7zz and gcab")
	}
	sevenZip, _ := exec.LookPath("7zz")
	gcab, _ := exec.LookPath("gcab")
	if sevenZip == "" && gcab == "" {
		t.Skip("neither 7zz nor gcab is installed")
	}
	dir := t.TempDir()

	// Cabinets: one LZX stream per folder, no resets, a frame per data block.
	for _, bits := range []uint{15, 16, 17, 21} {
		for _, mode := range []blockMode{modeUncompressed, modeVerbatim, modeAligned, modeMixed} {
			for _, e8 := range []int32{0, 12_000_000} {
				name := fmt.Sprintf("w%d-m%d-e8%v", bits, mode, e8 != 0)
				data := append(textData(6*frameSize, uint64(bits)), noise(2*frameSize, 7)...)
				data = append(data, e8Data(4*frameSize, e8, 99)...)
				comp, frames := encodeLZX(t, data, encOptions{windowBits: bits, mode: mode, e8Size: e8})
				cab := filepath.Join(dir, name+".cab")
				if err := os.WriteFile(cab, cabinet(comp, frames, bits, len(data)), 0o644); err != nil {
					t.Fatal(err)
				}
				for tool, args := range map[string]func(out string) []string{
					sevenZip: func(out string) []string { return []string{"x", "-y", "-o" + out, cab} },
					gcab:     func(out string) []string { return []string{"-x", "-C", out, cab} },
				} {
					if tool == "" {
						continue
					}
					out := filepath.Join(dir, name+"-"+filepath.Base(tool))
					got := extractWith(t, tool, args(out), out, "data.bin")
					if !bytes.Equal(got, data) {
						t.Errorf("%s: %s decodes it differently, from byte %d", name, filepath.Base(tool), firstDiff(got, data))
					}
				}
			}
		}
	}

	// Archives: 7-Zip reads ITSF version 3 only.
	if sevenZip == "" {
		return
	}
	entries := sample()
	for i := range 60 {
		entries = append(entries, testEntry{name: fmt.Sprintf("/pages/p%03d.htm", i), section: 1, data: textData(1000+i*37, uint64(100+i))})
	}
	for name, spec := range map[string]chmSpec{
		"default":          {},
		"small-chunks":     {chunkSize: 512, scramble: true},
		"index":            {chunkSize: 1024, index: true},
		"w15-reset1-verb":  {enc: encOptions{windowBits: 15, resetFrames: 1, mode: modeVerbatim}},
		"w16-reset2-align": {enc: encOptions{resetFrames: 2, mode: modeAligned}},
		"w16-reset2-raw":   {enc: encOptions{resetFrames: 2, mode: modeUncompressed}},
		"w17-reset4-mixed": {enc: encOptions{windowBits: 17, resetFrames: 4, mode: modeMixed}},
		"w21-reset8-mixed": {enc: encOptions{windowBits: 21, resetFrames: 8, mode: modeMixed}},
	} {
		path := filepath.Join(dir, name+".chm")
		if err := os.WriteFile(path, buildCHM(t, spec, entries).data, 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, name)
		for _, e := range userFiles(entries) {
			got := extractWith(t, sevenZip, []string{"x", "-y", "-o" + out, path}, out, e.name)
			if !bytes.Equal(got, e.data) {
				t.Errorf("%s: 7zz reads %s differently, from byte %d", name, e.name, firstDiff(got, e.data))
			}
		}
	}
}

// extractWith runs an extractor once per output folder, then reads one file it
// wrote.
func extractWith(t *testing.T, tool string, args []string, out, name string) []byte {
	t.Helper()
	if _, err := os.Stat(out); err != nil {
		if b, err := exec.Command(tool, args...).CombinedOutput(); err != nil {
			t.Logf("%s: %v\n%s", filepath.Base(tool), err, strings.TrimSpace(string(b)))
		}
	}
	got, _ := os.ReadFile(filepath.Join(out, filepath.FromSlash(name)))
	return got
}

// cabinet wraps an LZX stream as a one-folder, one-file cabinet, a data block
// per frame, each with its checksum.
func cabinet(comp []byte, frames []int64, windowBits uint, size int) []byte {
	le16 := binary.LittleEndian.AppendUint16
	le32 := binary.LittleEndian.AppendUint32
	file := le32(le32(nil, uint32(size)), 0)
	file = le16(le16(le16(le16(file, 0), 0x5A21), 0x6000), 0x20)
	file = append(file, "data.bin\x00"...)
	const filesAt = 36 + 8
	dataAt := filesAt + len(file)
	var blocks []byte
	for k, start := range frames {
		end := int64(len(comp))
		if k+1 < len(frames) {
			end = frames[k+1]
		}
		sizes := le16(le16(nil, uint16(end-start)), frameSize)
		blocks = le32(blocks, cabChecksum(sizes, cabChecksum(comp[start:end], 0)))
		blocks = append(blocks, sizes...)
		blocks = append(blocks, comp[start:end]...)
	}
	h := []byte("MSCF")
	h = le32(le32(le32(le32(le32(h, 0), uint32(dataAt+len(blocks))), 0), filesAt), 0)
	h = append(h, 3, 1)
	h = le16(le16(le16(le16(le16(h, 1), 1), 0), 0), 0)
	h = le32(h, uint32(dataAt))
	h = le16(le16(h, uint16(len(frames))), uint16(3|windowBits<<8))
	return append(append(h, file...), blocks...)
}

// cabChecksum is the cabinet format's: little-endian words XORed together, and
// the last one to three bytes taken big-end first.
func cabChecksum(data []byte, sum uint32) uint32 {
	for len(data) >= 4 {
		sum ^= binary.LittleEndian.Uint32(data)
		data = data[4:]
	}
	var tail uint32
	for _, c := range data {
		tail = tail<<8 | uint32(c)
	}
	return sum ^ tail
}
