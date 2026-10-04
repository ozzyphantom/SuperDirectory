package exif

import (
	"encoding/binary"
	"errors"
	"io"
)

// TIFF reads a TIFF-structured file — TIFF itself and the RAW formats built on it:
// NEF, NRW, CR2, DNG, ARW, SR2, ORF, PEF, SRW, RW2 — for its orientation and
// capture time. Their EXIF lives in the first megabyte, before the image data, so
// only that much is read. Sizes are not reported: a RAW's first IFD usually
// describes a preview, not the sensor image.
func TIFF(r io.ReaderAt, size int64) (Info, error) {
	n := min(size, 1<<20)
	if n < 8 {
		return Info{Orientation: 1}, errors.New("tiff: too short")
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return Info{Orientation: 1}, err
	}
	// Panasonic RW2 and Olympus ORF use their own magic numbers in place of 42.
	if (string(buf[:4]) == "IIU\x00" || string(buf[:4]) == "IIRO" || string(buf[:4]) == "IIRS") && len(buf) > 4 {
		buf[2], buf[3] = 42, 0
	}
	info := Parse(buf)
	info.Thumb = nil // a RAW's IFD1 is not reliably a thumbnail of the photo
	return info, nil
}

// RAF reads a Fujifilm RAW file through the JPEG preview it embeds, whose EXIF
// carries the capture time. The header records where the preview starts.
func RAF(r io.ReaderAt, size int64) (Info, error) {
	var head [92]byte
	if _, err := r.ReadAt(head[:], 0); err != nil {
		return Info{Orientation: 1}, err
	}
	if string(head[:16]) != "FUJIFILMCCD-RAW " {
		return Info{Orientation: 1}, errors.New("raf: not a Fujifilm RAW file")
	}
	off := int64(binary.BigEndian.Uint32(head[84:88]))
	n := int64(binary.BigEndian.Uint32(head[88:92]))
	if off <= 0 || n <= 0 || off+n > size {
		return Info{Orientation: 1}, errors.New("raf: bad preview offset")
	}
	info, err := JPEG(io.NewSectionReader(r, off, n))
	info.Width, info.Height, info.Thumb = 0, 0, nil // the preview's, not the sensor's
	return info, err
}

// cr3UUID marks the box in a Canon CR3 that holds its TIFF-structured metadata.
const cr3UUID = "\x85\xc0\xb6\x87\x82\x0f\x11\xe0\x81\x11\xf4\xce\x46\x2b\x6a\x48"

// CR3 reads a Canon CR3, an ISO base media file: moov holds a box with Canon's
// UUID, which holds CMT1 (the TIFF IFD0, with the orientation) and CMT2 (the EXIF
// IFD, with the capture time), each a small TIFF file of its own.
func CR3(r io.ReadSeeker) (Info, error) {
	info := Info{Orientation: 1}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return info, err
	}
	size, err := findBox(r, "moov", -1)
	if err != nil {
		return info, err
	}
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return info, err
	}
	end := start + int64(min(size, 1<<40))
	for range 256 {
		size, err := findBox(r, "uuid", end)
		if err != nil || size < 16 || size > 8<<20 {
			return info, errors.New("cr3: no metadata box")
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			return info, err
		}
		if string(body[:16]) != cr3UUID {
			continue
		}
		eachBox(body[16:], func(typ string, b []byte) {
			switch typ {
			case "CMT1":
				info.Orientation = Parse(b).Orientation
			case "CMT2":
				info.Taken = Parse(b).Taken
			}
		})
		return info, nil
	}
	return info, errors.New("cr3: no metadata box")
}
