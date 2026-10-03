package exif

import (
	"encoding/binary"
	"errors"
	"io"
)

var errNoFrame = errors.New("jpeg: no frame header before the image data")

// JPEG walks a JPEG's segments up to its frame header, collecting the dimensions,
// the EXIF orientation, thumbnail and capture time. Segments it does not need —
// ICC profiles, Photoshop blocks, XMP — are seeked past, not read, so the cost is
// the EXIF block (64 KiB at most) and a few segment headers.
func JPEG(r io.ReadSeeker) (Info, error) {
	info := Info{Orientation: 1}
	exifSeen := false
	var b [2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return info, err
	}
	if b[0] != 0xFF || b[1] != 0xD8 {
		return info, errors.New("jpeg: missing start-of-image marker")
	}
	for range 512 { // a real header has a dozen segments; this bounds a hostile file
		if _, err := io.ReadFull(r, b[:1]); err != nil {
			return info, err
		}
		if b[0] != 0xFF {
			return info, errors.New("jpeg: expected a marker")
		}
		marker := byte(0xFF)
		for marker == 0xFF { // fill bytes may pad between segments
			if _, err := io.ReadFull(r, b[:1]); err != nil {
				return info, err
			}
			marker = b[0]
		}
		switch {
		case marker == 0x01 || marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7):
			continue // standalone markers carry no length
		case marker == 0xD9 || marker == 0xDA:
			return info, errNoFrame // end of image, or scan data, before any frame
		}
		if _, err := io.ReadFull(r, b[:2]); err != nil {
			return info, err
		}
		n := int(binary.BigEndian.Uint16(b[:])) - 2
		if n < 0 {
			return info, errors.New("jpeg: bad segment length")
		}
		switch {
		case isSOF(marker):
			if n < 5 {
				return info, errors.New("jpeg: short frame header")
			}
			var f [5]byte
			if _, err := io.ReadFull(r, f[:]); err != nil {
				return info, err
			}
			info.Height = int(binary.BigEndian.Uint16(f[1:3]))
			info.Width = int(binary.BigEndian.Uint16(f[3:5]))
			return info, nil
		case marker == 0xE1 && !exifSeen: // APP1: EXIF, or XMP
			seg := make([]byte, n)
			if _, err := io.ReadFull(r, seg); err != nil {
				return info, err
			}
			if len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
				exifSeen = true
				e := Parse(seg[6:])
				info.Orientation, info.Thumb, info.Taken = e.Orientation, e.Thumb, e.Taken
			}
		default:
			if _, err := r.Seek(int64(n), io.SeekCurrent); err != nil {
				return info, err
			}
		}
	}
	return info, errNoFrame
}

// isSOF reports whether a marker starts a frame: SOF0 through SOF15, except the
// three in that range that mean something else.
func isSOF(m byte) bool {
	return m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC
}
