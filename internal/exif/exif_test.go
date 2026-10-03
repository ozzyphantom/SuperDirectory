package exif

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"testing"
	"time"
)

// tiffBlock builds an EXIF block: IFD0 with an orientation and a pointer to the
// EXIF IFD, which holds DateTimeOriginal and, when offset is set, its UTC offset;
// and IFD1 with a JPEG thumbnail when thumb is set.
func tiffBlock(orientation int, original, offset string, thumb []byte) []byte {
	le := binary.LittleEndian
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, le, v) }
	entry := func(tag, typ uint16, count, value uint32) { w(tag); w(typ); w(count); w(value) }

	nExif := 1
	if offset != "" {
		nExif = 2
	}
	const ifd0 = 8
	exifIFD := ifd0 + 2 + 2*12 + 4
	ifd1 := exifIFD + 2 + nExif*12 + 4
	data := ifd1
	if thumb != nil {
		data += 2 + 2*12 + 4
	}
	origAt := data
	offAt := origAt + len(original) + 1
	thumbAt := offAt + len(offset) + 1

	b.WriteString("II")
	w(uint16(42))
	w(uint32(ifd0))

	w(uint16(2))
	b.Write([]byte{0x12, 0x01, 3, 0, 1, 0, 0, 0})
	w(uint16(orientation))
	w(uint16(0))
	entry(tagExifIFD, 4, 1, uint32(exifIFD))
	if thumb != nil {
		w(uint32(ifd1))
	} else {
		w(uint32(0))
	}

	w(uint16(nExif))
	entry(tagDateTimeOriginal, 2, uint32(len(original)+1), uint32(origAt))
	if offset != "" {
		entry(tagOffsetTimeOrig, 2, uint32(len(offset)+1), uint32(offAt))
	}
	w(uint32(0))

	if thumb != nil {
		w(uint16(2))
		entry(tagThumbOffset, 4, 1, uint32(thumbAt))
		entry(tagThumbLength, 4, 1, uint32(len(thumb)))
		w(uint32(0))
	}
	b.WriteString(original + "\x00")
	b.WriteString(offset + "\x00")
	b.Write(thumb)
	return b.Bytes()
}

func gradient(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	return img
}

func encode(img image.Image) []byte {
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	return b.Bytes()
}

// withSegment splices a segment in right after a JPEG's start-of-image marker.
func withSegment(jpg []byte, marker byte, payload []byte) []byte {
	seg := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

func cameraJPEG(orientation int, original, offset string) []byte {
	thumb := encode(gradient(160, 107))
	return withSegment(encode(gradient(600, 400)), 0xE1, append([]byte("Exif\x00\x00"), tiffBlock(orientation, original, offset, thumb)...))
}

var july = time.Date(2019, 7, 14, 10, 32, 5, 0, time.Local)

func TestJPEG(t *testing.T) {
	info, err := JPEG(bytes.NewReader(cameraJPEG(6, "2019:07:14 10:32:05", "")))
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 600 || info.Height != 400 || info.Orientation != 6 {
		t.Errorf("info = %dx%d orientation %d", info.Width, info.Height, info.Orientation)
	}
	if w, h := info.Displayed(); w != 400 || h != 600 {
		t.Errorf("displayed = %dx%d, want 400x600", w, h)
	}
	if !info.Taken.Equal(july) {
		t.Errorf("taken = %v, want %v local", info.Taken, july)
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(info.Thumb)); err != nil {
		t.Errorf("thumbnail does not decode: %v", err)
	}
}

func TestDatesHonorTheRecordedOffset(t *testing.T) {
	info := Parse(tiffBlock(1, "2019:07:14 10:32:05", "+02:00", nil))
	want := time.Date(2019, 7, 14, 8, 32, 5, 0, time.UTC)
	if !info.Taken.Equal(want) {
		t.Errorf("taken = %v, want %v", info.Taken, want)
	}
	if !Parse(tiffBlock(1, "0000:00:00 00:00:00", "", nil)).Taken.IsZero() {
		t.Error("an unset camera clock produced a date")
	}
}

// TestJPEGSeeksPastLargeSegments: an ICC profile or Photoshop block can run to
// megabytes. The header walk must skip it with a seek, not read it.
func TestJPEGSeeksPastLargeSegments(t *testing.T) {
	data := withSegment(encode(gradient(300, 200)), 0xE2, make([]byte, 60000))
	var n int64
	info, err := JPEG(&countingSeeker{r: bytes.NewReader(data), n: &n})
	if err != nil || info.Width != 300 || info.Height != 200 {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if n > 4096 {
		t.Errorf("read %d bytes to find the frame; the 60 KB segment should have been skipped", n)
	}
}

type countingSeeker struct {
	r io.ReadSeeker
	n *int64
}

func (c *countingSeeker) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

func (c *countingSeeker) Seek(o int64, w int) (int64, error) { return c.r.Seek(o, w) }

func box(typ string, body ...[]byte) []byte {
	var b []byte
	for _, p := range body {
		b = append(b, p...)
	}
	h := make([]byte, 8)
	binary.BigEndian.PutUint32(h, uint32(8+len(b)))
	copy(h[4:], typ)
	return append(h, b...)
}

func u16(v int) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, uint16(v)); return b }
func u32(v int) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, uint32(v)); return b }

// heicFile builds what HEIC reads: ftyp; meta with item info naming an Exif item,
// its location, and the item properties; then mdat holding the EXIF payload.
func heicFile(quarterTurns int, exifTIFF []byte, sizes ...[2]int) []byte {
	ftyp := box("ftyp", []byte("heic\x00\x00\x00\x00mif1heic"))
	var props [][]byte
	for _, s := range sizes {
		props = append(props, box("ispe", u32(0), u32(s[0]), u32(s[1])))
	}
	props = append(props, box("irot", []byte{byte(quarterTurns)}))
	payload := append(append(u32(6), []byte("Exif\x00\x00")...), exifTIFF...)

	build := func(offset int) []byte {
		infe := box("infe", []byte{2, 0, 0, 0}, u16(7), u16(0), []byte("Exif"), []byte{0})
		iinf := box("iinf", []byte{0, 0, 0, 0}, u16(1), infe)
		iloc := box("iloc", []byte{0, 0, 0, 0}, []byte{0x44, 0x00}, u16(1), u16(7), u16(0), u16(1), u32(offset), u32(len(payload)))
		iprp := box("iprp", box("ipco", props...))
		return box("meta", []byte{0, 0, 0, 0}, box("hdlr", make([]byte, 24)), iinf, iloc, iprp)
	}
	meta := build(0)
	meta = build(len(ftyp) + len(meta) + 8)
	return append(append(ftyp, meta...), box("mdat", payload)...)
}

func TestHEIC(t *testing.T) {
	data := heicFile(3, tiffBlock(1, "2019:07:14 10:32:05", "", nil), [2]int{512, 512}, [2]int{320, 240}, [2]int{4032, 3024})
	info, err := HEIC(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 4032 || info.Height != 3024 {
		t.Errorf("size = %dx%d, want the largest, 4032x3024", info.Width, info.Height)
	}
	if w, h := info.Displayed(); w != 3024 || h != 4032 {
		t.Errorf("displayed = %dx%d: three quarter turns", w, h)
	}
	if !info.Taken.Equal(july) {
		t.Errorf("taken = %v, want %v", info.Taken, july)
	}
}

func TestVideo(t *testing.T) {
	when := time.Date(2021, 5, 2, 18, 0, 0, 0, time.UTC)
	secs := int(when.Sub(epoch1904) / time.Second)
	mvhd := box("mvhd", []byte{0, 0, 0, 0}, u32(secs), u32(secs), u32(600), u32(6000), make([]byte, 80))
	data := append(append(box("ftyp", []byte("qt  \x00\x00\x00\x00qt  ")), box("mdat", make([]byte, 5000))...), box("moov", mvhd)...)
	info, err := Video(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Taken.Equal(when) {
		t.Errorf("taken = %v, want %v", info.Taken, when)
	}
	zero := append(box("ftyp", []byte("isom")), box("moov", box("mvhd", make([]byte, 100)))...)
	if info, _ := Video(bytes.NewReader(zero)); !info.Taken.IsZero() {
		t.Error("a zero creation time produced a date")
	}
}

func TestRAWFormats(t *testing.T) {
	block := tiffBlock(8, "2019:07:14 10:32:05", "", nil)
	if info, err := TIFF(bytes.NewReader(block), int64(len(block))); err != nil || !info.Taken.Equal(july) || info.Orientation != 8 {
		t.Errorf("TIFF/NEF: %+v, %v", info, err)
	}

	jpg := cameraJPEG(1, "2019:07:14 10:32:05", "")
	raf := make([]byte, 100)
	copy(raf, "FUJIFILMCCD-RAW ")
	binary.BigEndian.PutUint32(raf[84:], 100)
	binary.BigEndian.PutUint32(raf[88:], uint32(len(jpg)))
	raf = append(raf, jpg...)
	if info, err := RAF(bytes.NewReader(raf), int64(len(raf))); err != nil || !info.Taken.Equal(july) {
		t.Errorf("RAF: %+v, %v", info, err)
	}

	cmt2 := tiffBlock(1, "2019:07:14 10:32:05", "", nil)
	uuid := box("uuid", []byte(cr3UUID), box("CMT1", tiffBlock(6, "", "", nil)), box("CMT2", cmt2))
	cr3 := append(box("ftyp", []byte("crx \x00\x00\x00\x01crx isom")), box("moov", uuid)...)
	if info, err := CR3(bytes.NewReader(cr3)); err != nil || !info.Taken.Equal(july) || info.Orientation != 6 {
		t.Errorf("CR3: %+v, %v", info, err)
	}
}

func TestPNGAndWebP(t *testing.T) {
	chunk := func(typ string, data []byte) []byte {
		return append(append(append(u32(len(data)), []byte(typ)...), data...), 0, 0, 0, 0)
	}
	block := tiffBlock(1, "2019:07:14 10:32:05", "", nil)
	png := append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", make([]byte, 13))...)
	png = append(append(png, chunk("eXIf", block)...), chunk("IEND", nil)...)
	if info, err := PNG(bytes.NewReader(png)); err != nil || !info.Taken.Equal(july) {
		t.Errorf("PNG: %+v, %v", info, err)
	}

	riff := func(typ string, data []byte) []byte {
		h := []byte(typ)
		h = binary.LittleEndian.AppendUint32(h, uint32(len(data)))
		h = append(h, data...)
		if len(data)%2 == 1 {
			h = append(h, 0)
		}
		return h
	}
	body := append([]byte("WEBP"), riff("VP8X", make([]byte, 10))...)
	body = append(body, riff("EXIF", append([]byte("Exif\x00\x00"), block...))...)
	webp := append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...)
	webp = append(webp, body...)
	if info, err := WebP(bytes.NewReader(webp)); err != nil || !info.Taken.Equal(july) {
		t.Errorf("WebP: %+v, %v", info, err)
	}
}

func TestTakenDispatchesByExtension(t *testing.T) {
	jpg := cameraJPEG(1, "2019:07:14 10:32:05", "")
	if when, ok := Taken("IMG_0001.JPG", bytes.NewReader(jpg), int64(len(jpg))); !ok || !when.Equal(july) {
		t.Errorf("Taken(jpg) = %v, %v", when, ok)
	}
	if _, ok := Taken("notes.txt", bytes.NewReader(jpg), int64(len(jpg))); ok {
		t.Error("a text file has no capture time")
	}
	if !Supported("clip.MOV") || Supported("notes.txt") {
		t.Error("Supported")
	}
}

func FuzzParse(f *testing.F) {
	f.Add(tiffBlock(6, "2019:07:14 10:32:05", "+02:00", []byte{0xFF, 0xD8, 0xFF, 0xD9}))
	f.Add([]byte("MM\x00\x2a\x00\x00\x00\x08"))
	f.Fuzz(func(t *testing.T, b []byte) {
		info := Parse(b)
		if info.Orientation < 1 || info.Orientation > 8 {
			t.Errorf("orientation %d out of range", info.Orientation)
		}
		if info.Thumb != nil && (len(info.Thumb) < 4 || info.Thumb[0] != 0xFF) {
			t.Error("a thumbnail that is not a JPEG")
		}
	})
}

func FuzzJPEG(f *testing.F) {
	f.Add(cameraJPEG(6, "2019:07:14 10:32:05", ""))
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x02, 0xFF, 0xD9})
	f.Fuzz(func(t *testing.T, b []byte) { JPEG(bytes.NewReader(b)) })
}

func FuzzHEIC(f *testing.F) {
	f.Add(heicFile(1, tiffBlock(1, "2019:07:14 10:32:05", "", nil), [2]int{100, 50}))
	f.Fuzz(func(t *testing.T, b []byte) { HEIC(bytes.NewReader(b)) })
}

func FuzzMedia(f *testing.F) {
	mvhd := box("mvhd", []byte{0, 0, 0, 0}, u32(1), u32(1), u32(600), u32(6000), make([]byte, 80))
	f.Add(append(box("ftyp", []byte("isom")), box("moov", mvhd)...))
	f.Add(append([]byte("\x89PNG\r\n\x1a\n"), 0, 0, 0, 4, 'e', 'X', 'I', 'f', 'I', 'I', 42, 0))
	f.Fuzz(func(t *testing.T, b []byte) {
		r := bytes.NewReader(b)
		Video(r)
		r.Seek(0, io.SeekStart)
		PNG(r)
		r.Seek(0, io.SeekStart)
		WebP(r)
		r.Seek(0, io.SeekStart)
		CR3(r)
		RAF(r, int64(len(b)))
		TIFF(r, int64(len(b)))
	})
}
