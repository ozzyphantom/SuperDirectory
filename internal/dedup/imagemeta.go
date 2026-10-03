package dedup

import (
	"encoding/binary"
	"errors"
	"image"
	"io"

	// Decoders for image.Decode and image.DecodeConfig. JPEG, PNG and GIF come from
	// the standard library; BMP, TIFF and WebP from the Go project's x/image.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// header is what the start of an image file says about it.
type header struct {
	w, h        int    // stored pixel dimensions
	orientation int    // EXIF orientation, 1 when absent
	thumb       []byte // embedded EXIF JPEG thumbnail, nil when absent
}

// displayed returns the dimensions the picture is shown at: an orientation of 5
// through 8 turns it a quarter, swapping width and height.
func (h header) displayed() (w, ht int) {
	if h.orientation >= 5 && h.orientation <= 8 {
		return h.h, h.w
	}
	return h.w, h.h
}

var errNoFrame = errors.New("jpeg: no frame header before the image data")

// readJPEGHeader walks a JPEG's segments up to its frame header, collecting the
// dimensions, the EXIF orientation, and the embedded EXIF thumbnail. Segments it
// does not need — ICC profiles, Photoshop blocks, XMP — are seeked past, not read,
// so the cost is the EXIF block (64 KiB at most) and a few segment headers.
func readJPEGHeader(r io.ReadSeeker) (header, error) {
	h := header{orientation: 1}
	exifSeen := false
	var b [2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return h, err
	}
	if b[0] != 0xFF || b[1] != 0xD8 {
		return h, errors.New("jpeg: missing start-of-image marker")
	}
	for range 512 { // a real header has a dozen segments; this bounds a hostile file
		if _, err := io.ReadFull(r, b[:1]); err != nil {
			return h, err
		}
		if b[0] != 0xFF {
			return h, errors.New("jpeg: expected a marker")
		}
		marker := byte(0xFF)
		for marker == 0xFF { // fill bytes may pad between segments
			if _, err := io.ReadFull(r, b[:1]); err != nil {
				return h, err
			}
			marker = b[0]
		}
		switch {
		case marker == 0x01 || marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7):
			continue // standalone markers carry no length
		case marker == 0xD9 || marker == 0xDA:
			return h, errNoFrame // end of image, or scan data, before any frame
		}
		if _, err := io.ReadFull(r, b[:2]); err != nil {
			return h, err
		}
		n := int(binary.BigEndian.Uint16(b[:])) - 2
		if n < 0 {
			return h, errors.New("jpeg: bad segment length")
		}
		switch {
		case isSOF(marker):
			if n < 5 {
				return h, errors.New("jpeg: short frame header")
			}
			var f [5]byte
			if _, err := io.ReadFull(r, f[:]); err != nil {
				return h, err
			}
			h.h = int(binary.BigEndian.Uint16(f[1:3]))
			h.w = int(binary.BigEndian.Uint16(f[3:5]))
			return h, nil
		case marker == 0xE1 && !exifSeen: // APP1: EXIF, or XMP
			seg := make([]byte, n)
			if _, err := io.ReadFull(r, seg); err != nil {
				return h, err
			}
			if len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
				exifSeen = true
				h.orientation, h.thumb = parseExif(seg[6:])
			}
		default:
			if _, err := r.Seek(int64(n), io.SeekCurrent); err != nil {
				return h, err
			}
		}
	}
	return h, errNoFrame
}

// isSOF reports whether a marker starts a frame: SOF0 through SOF15, except the
// three in that range that mean something else.
func isSOF(m byte) bool {
	return m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC
}

// parseExif reads the orientation from IFD0 and the JPEG thumbnail from IFD1 of
// an EXIF block, which is a small TIFF file. Anything malformed yields the
// defaults: orientation 1 and no thumbnail. Every offset is bounds-checked; this
// is untrusted input.
func parseExif(tiff []byte) (orientation int, thumb []byte) {
	orientation = 1
	if len(tiff) < 8 {
		return orientation, nil
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return orientation, nil
	}
	if bo.Uint16(tiff[2:4]) != 42 {
		return orientation, nil
	}

	// entries visits an IFD's entries and returns the offset of the next IFD.
	entries := func(off uint32, visit func(tag, typ uint16, count uint32, value []byte)) uint32 {
		if off < 8 || int(off)+2 > len(tiff) {
			return 0
		}
		count := int(bo.Uint16(tiff[off:]))
		start := int(off) + 2
		if start+12*count+4 > len(tiff) {
			return 0
		}
		for i := 0; i < count; i++ {
			e := tiff[start+12*i : start+12*i+12]
			visit(bo.Uint16(e[0:2]), bo.Uint16(e[2:4]), bo.Uint32(e[4:8]), e[8:12])
		}
		return bo.Uint32(tiff[start+12*count:])
	}

	next := entries(bo.Uint32(tiff[4:8]), func(tag, typ uint16, count uint32, value []byte) {
		if tag == 0x0112 && typ == 3 && count == 1 { // Orientation, SHORT
			if o := int(bo.Uint16(value)); o >= 1 && o <= 8 {
				orientation = o
			}
		}
	})

	var thumbOff, thumbLen uint32
	entries(next, func(tag, typ uint16, count uint32, value []byte) {
		switch tag {
		case 0x0201: // JPEGInterchangeFormat: where the thumbnail starts
			thumbOff = bo.Uint32(value)
		case 0x0202: // JPEGInterchangeFormatLength
			thumbLen = bo.Uint32(value)
		}
	})
	end := uint64(thumbOff) + uint64(thumbLen)
	if thumbOff == 0 || thumbLen < 4 || end > uint64(len(tiff)) {
		return orientation, nil
	}
	t := tiff[thumbOff:end]
	if t[0] != 0xFF || t[1] != 0xD8 {
		return orientation, nil // an uncompressed thumbnail: rare, and not worth decoding
	}
	return orientation, append([]byte(nil), t...) // own it; the EXIF block is discarded
}

// readOtherHeader reads the dimensions of a PNG, GIF, BMP, TIFF or WebP through the
// registered decoders, which parse only as far as they must.
func readOtherHeader(r io.Reader) (header, error) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return header{}, err
	}
	return header{w: cfg.Width, h: cfg.Height, orientation: 1}, nil
}
