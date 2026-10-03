package exif

import (
	"encoding/binary"
	"errors"
	"io"
)

// readBoxHeader reads an ISO base media box header (HEIC, MP4, MOV, CR3): the
// box's total size, its type, and how many bytes the header took. A size of 0
// means "to the end of the file".
func readBoxHeader(r io.Reader) (size uint64, typ string, hdr uint64, err error) {
	var b [8]byte
	if _, err = io.ReadFull(r, b[:]); err != nil {
		return
	}
	size, typ, hdr = uint64(binary.BigEndian.Uint32(b[:4])), string(b[4:8]), 8
	if size == 1 {
		if _, err = io.ReadFull(r, b[:]); err != nil {
			return
		}
		size, hdr = binary.BigEndian.Uint64(b[:]), 16
	}
	if size != 0 && size < hdr {
		err = errors.New("bad box size")
	}
	return
}

// findBox scans the boxes from r's position to the end of the file (or to end,
// when non-negative) for one of type want, seeking past the rest, and returns its
// body's size with r positioned at the body.
func findBox(r io.ReadSeeker, want string, end int64) (uint64, error) {
	for range 4096 {
		pos, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}
		if end >= 0 && pos >= end {
			break
		}
		size, typ, hdr, err := readBoxHeader(r)
		if err != nil {
			return 0, err
		}
		if size == 0 {
			if typ == want {
				return 1 << 62, nil // runs to the end of the file
			}
			break
		}
		if typ == want {
			return size - hdr, nil
		}
		if size-hdr > 1<<62 {
			break
		}
		if _, err := r.Seek(int64(size-hdr), io.SeekCurrent); err != nil {
			return 0, err
		}
	}
	return 0, errors.New("no " + want + " box")
}

// eachBox calls fn for every box packed in b, with its type and body. It stops at
// the first malformed box rather than reading past it.
func eachBox(b []byte, fn func(typ string, body []byte)) {
	for len(b) >= 8 {
		size, hdr := uint64(binary.BigEndian.Uint32(b[:4])), uint64(8)
		typ := string(b[4:8])
		if size == 1 {
			if len(b) < 16 {
				return
			}
			size, hdr = binary.BigEndian.Uint64(b[8:16]), 16
		}
		if size == 0 {
			size = uint64(len(b))
		}
		if size < hdr || size > uint64(len(b)) {
			return
		}
		fn(typ, b[hdr:size])
		b = b[size:]
	}
}
