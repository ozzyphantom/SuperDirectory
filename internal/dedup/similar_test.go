package dedup

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ozzyphantom/SuperDirectory/internal/exif"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

// exifBlock builds the TIFF structure of an EXIF segment: an orientation in IFD0,
// and, when thumb is set, a JPEG thumbnail referenced from IFD1.
func exifBlock(orientation int, thumb []byte) []byte {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	entry := func(tag, typ uint16, count uint32) { w(tag); w(typ); w(count) }

	b.WriteString("II")
	w(uint16(42))
	w(uint32(8)) // IFD0 starts at offset 8

	// IFD0: a count, one 12-byte entry, the next-IFD offset. 18 bytes, so IFD1 is at 26.
	w(uint16(1))
	entry(0x0112, 3, 1) // Orientation, SHORT
	w(uint16(orientation))
	w(uint16(0))
	if thumb == nil {
		w(uint32(0))
		return b.Bytes()
	}
	const ifd1 = 26
	w(uint32(ifd1))

	// IFD1: a count, two entries, no next IFD. 30 bytes, so the thumbnail is at 56.
	const data = ifd1 + 30
	w(uint16(2))
	entry(0x0201, 4, 1) // JPEGInterchangeFormat, LONG
	w(uint32(data))
	entry(0x0202, 4, 1) // JPEGInterchangeFormatLength, LONG
	w(uint32(len(thumb)))
	w(uint32(0))
	b.Write(thumb)
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

func encodeJPEG(t *testing.T, img image.Image, q int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// cameraJPEG encodes img the way a camera does: with an EXIF orientation and a
// 160-pixel embedded thumbnail of thumbOf (normally img itself).
func cameraJPEG(t *testing.T, img, thumbOf image.Image, orientation int) []byte {
	t.Helper()
	b := thumbOf.Bounds()
	tw := 160
	th := tw * b.Dy() / b.Dx()
	thumb := encodeJPEG(t, resized(thumbOf, tw, th), 75)
	return withSegment(encodeJPEG(t, img, 90), 0xE1, append([]byte("Exif\x00\x00"), exifBlock(orientation, thumb)...))
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestThumbFitsRejectsALetterboxedThumbnail: a 4:3 thumbnail on a 3:2 photo carries
// black bars, and would fingerprint a different picture.
func TestThumbFitsRejectsALetterboxedThumbnail(t *testing.T) {
	thumb := encodeJPEG(t, scene(160, 120, 1, 0), 75)
	if thumbFits(exif.Info{Width: 6000, Height: 4000, Thumb: thumb}) {
		t.Error("a 4:3 thumbnail was accepted for a 3:2 picture")
	}
	img := scene(600, 400, 1, 0)
	h, err := exif.JPEG(bytes.NewReader(cameraJPEG(t, img, img, 1)))
	if err != nil || !thumbFits(h) {
		t.Errorf("a same-shape thumbnail was rejected: %v", err)
	}
}

// similarFixture writes a picture library and returns its plan. Names say what each
// file is; the plan is in name order, as a walk would give it.
func similarFixture(t *testing.T, files map[string][]byte) []flatten.Item {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, data := range files {
		paths = append(paths, write(t, dir, name, data))
	}
	sortStrings(paths)
	return plan(paths...)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func names(items []flatten.Item, idx []int) []string {
	var out []string
	for _, i := range idx {
		out = append(out, filepath.Base(items[i].Src))
	}
	return out
}

func TestFindSimilarKeepsTheLargest(t *testing.T) {
	photo := scene(1200, 800, 1, 0)
	other := scene(1200, 800, 2, 0)
	items := similarFixture(t, map[string][]byte{
		"a-photo.jpg":        cameraJPEG(t, photo, photo, 1),
		"b-photo-small.jpg":  encodeJPEG(t, resized(photo, 600, 400), 80),
		"c-photo-tiny.png":   encodePNG(t, resized(photo, 300, 200)),
		"d-other.jpg":        cameraJPEG(t, other, other, 1),
		"e-other-crop.jpg":   encodeJPEG(t, resized(other.SubImage(image.Rect(100, 100, 1100, 700)), 500, 300), 85),
		"f-photo-resave.jpg": encodeJPEG(t, photo, 50), // same size: never a copy
	})
	res := FindSimilar(items, Options{})
	if len(res.Sets) != 1 {
		t.Fatalf("sets = %+v, want exactly one", res.Sets)
	}
	set := res.Sets[0]
	keep := filepath.Base(items[set.Keep].Src)
	if keep != "a-photo.jpg" && keep != "f-photo-resave.jpg" {
		t.Errorf("kept %s; want a full-size copy", keep)
	}
	got := names(items, set.Skip)
	if len(got) != 2 || got[0] != "b-photo-small.jpg" || got[1] != "c-photo-tiny.png" {
		t.Errorf("skipped %v, want the two smaller copies", got)
	}
	if res.Files != 2 || res.Bytes <= 0 {
		t.Errorf("Files = %d, Bytes = %d", res.Files, res.Bytes)
	}
	if d := res.Dims[set.Skip[1]]; d != (Dims{300, 200}) {
		t.Errorf("Dims of the tiny copy = %+v", d)
	}
	for _, i := range res.Skipped() {
		switch filepath.Base(items[i].Src) {
		case "a-photo.jpg", "f-photo-resave.jpg", "d-other.jpg", "e-other-crop.jpg":
			t.Errorf("skipped %s", items[i].Src)
		}
	}
}

// TestFindSimilarDistrustsAStaleThumbnail is the case the confirmation stage exists
// for. Some editors crop a photo and leave the original's thumbnail in its EXIF.
// That thumbnail matches the original, but the crop is a different picture and must
// survive.
func TestFindSimilarDistrustsAStaleThumbnail(t *testing.T) {
	photo := scene(1200, 800, 3, 0)
	crop := resized(photo.SubImage(image.Rect(300, 200, 900, 600)), 600, 400) // same shape, different picture
	items := similarFixture(t, map[string][]byte{
		"a-photo.jpg": cameraJPEG(t, photo, photo, 1),
		"b-crop.jpg":  cameraJPEG(t, crop, photo, 1), // the original's thumbnail, stale
	})
	confirmed := false
	res := FindSimilar(items, Options{OnProgress: func(p Progress) {
		confirmed = confirmed || p.Phase == Confirming
	}})
	if !confirmed {
		t.Fatal("the thumbnails never matched, so this test proves nothing; it needs a stale-thumbnail match to reject")
	}
	if len(res.Sets) != 0 {
		t.Errorf("a crop with a stale thumbnail was skipped: %+v", res.Sets)
	}
}

// TestFindSimilarUndoesOrientation: the camera stored the portrait sideways with
// orientation 6; the export was turned for real.
func TestFindSimilarUndoesOrientation(t *testing.T) {
	portrait := scene(800, 1200, 4, 0)
	stored := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 800; x++ {
			stored.Set(y, 799-x, portrait.At(x, y))
		}
	}
	items := similarFixture(t, map[string][]byte{
		"a-camera.jpg": cameraJPEG(t, stored, stored, 6),
		"b-export.jpg": encodeJPEG(t, resized(portrait, 400, 600), 85),
	})
	res := FindSimilar(items, Options{})
	if len(res.Sets) != 1 || filepath.Base(items[res.Sets[0].Keep].Src) != "a-camera.jpg" {
		t.Fatalf("sets = %+v, want the camera original kept over the turned export", res.Sets)
	}
	if d := res.Dims[res.Sets[0].Keep]; d != (Dims{800, 1200}) {
		t.Errorf("displayed size = %+v, want 800x1200", d)
	}
}

// TestFindSimilarLeavesRAWAlone: a RAW never competes with the JPEG made from it.
// The ".nef" here is really a JPEG, which proves the extension alone excludes it.
func TestFindSimilarLeavesRAWAlone(t *testing.T) {
	photo := scene(1200, 800, 5, 0)
	items := similarFixture(t, map[string][]byte{
		"a-DSC_0042.nef": encodeJPEG(t, photo, 90),
		"b-DSC_0042.jpg": encodeJPEG(t, resized(photo, 600, 400), 85),
	})
	if res := FindSimilar(items, Options{}); res.Pictures != 1 || len(res.Sets) != 0 {
		t.Errorf("Pictures = %d, sets = %+v; the RAW must not be compared", res.Pictures, res.Sets)
	}
}

// TestFindSimilarReadsOnlyHeadersWithoutResizedCopies proves the gate. Every picture
// is the same size, as off one camera: nothing can be a smaller copy, so nothing
// beyond the headers is read.
func TestFindSimilarReadsOnlyHeadersWithoutResizedCopies(t *testing.T) {
	files := map[string][]byte{}
	for i := int64(1); i <= 4; i++ {
		files[string(rune('a'+i))+".png"] = encodePNG(t, scene(300, 200, i, 0))
	}
	items := similarFixture(t, files)
	var phases []Phase
	res := FindSimilar(items, Options{OnProgress: func(p Progress) { phases = append(phases, p.Phase) }})
	for _, p := range phases {
		if p != ReadingHeaders {
			t.Fatalf("reached phase %d; the gate should have stopped the scan at the headers", p)
		}
	}
	if len(res.Sets) != 0 {
		t.Errorf("sets = %+v", res.Sets)
	}
}

func TestFindSimilarIgnoresTinyPictures(t *testing.T) {
	icon := scene(48, 48, 6, 0)
	items := similarFixture(t, map[string][]byte{
		"a-icon.png":  encodePNG(t, icon),
		"b-icon2.png": encodePNG(t, resized(icon, 24, 24)),
	})
	if res := FindSimilar(items, Options{}); len(res.Sets) != 0 {
		t.Errorf("icons below %dpx were compared: %+v", minSide, res.Sets)
	}
}

func TestFindSimilarCanceled(t *testing.T) {
	photo := scene(600, 400, 7, 0)
	items := similarFixture(t, map[string][]byte{
		"a.png": encodePNG(t, photo),
		"b.png": encodePNG(t, resized(photo, 300, 200)),
	})
	cancel := make(chan struct{})
	close(cancel)
	if res := FindSimilar(items, Options{Cancel: cancel}); !res.Canceled || len(res.Sets) != 0 {
		t.Errorf("Canceled = %v, sets = %+v", res.Canceled, res.Sets)
	}
}

// TestFindSimilarHEIC runs only where sips is: macOS. A phone's HEIC and the smaller
// JPEG made from it are one picture; the HEIC is larger, so it is kept.
func TestFindSimilarHEIC(t *testing.T) {
	if sipsPath == "" {
		t.Skip("sips is macOS only")
	}
	dir := t.TempDir()
	photo := scene(1200, 800, 8, 0)
	src := write(t, dir, "src.png", encodePNG(t, photo))
	heic := filepath.Join(dir, "a-IMG_0042.heic")
	if out, err := exec.Command(sipsPath, "-s", "format", "heic", src, "--out", heic).CombinedOutput(); err != nil {
		t.Skipf("sips cannot write HEIC here: %v %s", err, out)
	}
	os.Remove(src)

	if h, err := guarded(reader{}, heic, func(f exif.File) (exif.Info, error) { return exif.HEIC(f) }); err != nil || h.Width != 1200 || h.Height != 800 {
		t.Fatalf("HEIC header = %+v, %v; want 1200x800", h, err)
	}

	small := write(t, dir, "b-IMG_0042-small.jpg", encodeJPEG(t, resized(photo, 600, 400), 85))
	items := plan(heic, small)
	res := FindSimilar(items, Options{})
	if len(res.Sets) != 1 || res.Sets[0].Keep != 0 || len(res.Sets[0].Skip) != 1 || res.Sets[0].Skip[0] != 1 {
		t.Errorf("sets = %+v, want the HEIC kept and the small JPEG skipped", res.Sets)
	}
}
