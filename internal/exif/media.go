package exif

import (
	"encoding/binary"
	"errors"
	"io"
	"time"
)

// epoch1904 is where QuickTime and MP4 count time from.
var epoch1904 = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)

// Video reads an MP4, MOV, M4V or 3GP file's creation time from its movie header
// ('mvhd'). The movie box can sit after gigabytes of media data; boxes are seeked
// past, never read. Cameras that never set their clock write zero, which is no time.
func Video(r io.ReadSeeker) (Info, error) {
	info := Info{Orientation: 1}
	size, err := findBox(r, "moov", -1)
	if err != nil {
		return info, err
	}
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return info, err
	}
	if _, err := findBox(r, "mvhd", start+int64(min(size, 1<<40))); err != nil {
		return info, err
	}
	var b [12]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return info, err
	}
	var secs uint64
	if b[0] == 1 { // version 1: 64-bit times
		secs = binary.BigEndian.Uint64(b[4:12])
	} else {
		secs = uint64(binary.BigEndian.Uint32(b[4:8]))
	}
	if secs == 0 || secs > 1<<40 {
		return info, nil
	}
	t := epoch1904.Add(time.Duration(secs) * time.Second)
	if t.Year() >= 1970 {
		info.Taken = t.Local()
	}
	return info, nil
}

// PNG reads the EXIF a PNG carries in an 'eXIf' chunk, when it has one.
func PNG(r io.ReadSeeker) (Info, error) {
	info := Info{Orientation: 1}
	var sig [8]byte
	if _, err := io.ReadFull(r, sig[:]); err != nil {
		return info, err
	}
	if string(sig[:]) != "\x89PNG\r\n\x1a\n" {
		return info, errors.New("png: bad signature")
	}
	for range 10000 {
		var h [8]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return info, nil // ran out of chunks without one
		}
		n := int64(binary.BigEndian.Uint32(h[:4]))
		switch string(h[4:8]) {
		case "eXIf":
			if n > 1<<20 {
				return info, nil
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil {
				return info, err
			}
			e := Parse(b)
			info.Orientation, info.Taken = e.Orientation, e.Taken
			return info, nil
		case "IEND":
			return info, nil
		}
		if _, err := r.Seek(n+4, io.SeekCurrent); err != nil { // data and CRC
			return info, err
		}
	}
	return info, nil
}

// WebP reads the EXIF a WebP carries in an 'EXIF' chunk, when it has one.
func WebP(r io.ReadSeeker) (Info, error) {
	info := Info{Orientation: 1}
	var h [12]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return info, err
	}
	if string(h[:4]) != "RIFF" || string(h[8:12]) != "WEBP" {
		return info, errors.New("webp: bad header")
	}
	for range 10000 {
		var c [8]byte
		if _, err := io.ReadFull(r, c[:]); err != nil {
			return info, nil
		}
		n := int64(binary.LittleEndian.Uint32(c[4:8]))
		if string(c[:4]) == "EXIF" {
			if n > 1<<20 {
				return info, nil
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil {
				return info, err
			}
			if len(b) > 6 && string(b[:6]) == "Exif\x00\x00" {
				b = b[6:]
			}
			e := Parse(b)
			info.Orientation, info.Taken = e.Orientation, e.Taken
			return info, nil
		}
		if _, err := r.Seek(n+n%2, io.SeekCurrent); err != nil { // chunks pad to even
			return info, err
		}
	}
	return info, nil
}
