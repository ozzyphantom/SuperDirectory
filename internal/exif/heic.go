package exif

import (
	"encoding/binary"
	"errors"
	"io"
)

// HEIC reads a HEIC or HEIF picture's size and rotation from its item properties,
// and its capture time from its EXIF item, without decoding it.
//
// A phone stores the picture as a grid of tiles plus a thumbnail, each with its own
// size property ('ispe'); the largest is the picture. A rotation property ('irot')
// turns it at display. The EXIF block is an item like any other: 'iinf' names it,
// 'iloc' says where its bytes are.
func HEIC(r io.ReadSeeker) (Info, error) {
	info := Info{Orientation: 1}
	size, err := findBox(r, "meta", -1)
	if err != nil {
		return info, err
	}
	if size > 4<<20 {
		return info, errors.New("heic: implausible meta box")
	}
	meta := make([]byte, size)
	if _, err := io.ReadFull(r, meta); err != nil {
		return info, err
	}
	if len(meta) < 4 {
		return info, errors.New("heic: empty meta box")
	}
	body := meta[4:] // meta is a full box: skip its version and flags

	w, h, turns := heicProperties(body)
	if w == 0 || h == 0 {
		return info, errors.New("heic: no image size")
	}
	info.Width, info.Height = w, h
	// irot counts quarter turns anticlockwise. In EXIF terms, a quarter turn
	// anticlockwise is orientation 8 and three of them are orientation 6. Only the
	// width-and-height swap matters to callers; renderers turn the pixels.
	switch turns {
	case 1:
		info.Orientation = 8
	case 2:
		info.Orientation = 3
	case 3:
		info.Orientation = 6
	}

	if off, n, ok := heicExifLocation(body); ok && n <= 1<<20 {
		buf := make([]byte, n)
		if _, err := r.Seek(int64(off), io.SeekStart); err == nil {
			if _, err := io.ReadFull(r, buf); err == nil {
				info.Taken = parseHEICExif(buf).Taken
			}
		}
	}
	return info, nil
}

// heicProperties searches a meta box's body for the largest 'ispe' size and the
// first 'irot' rotation.
func heicProperties(meta []byte) (w, h, quarterTurns int) {
	var best uint64
	eachBox(meta, func(typ string, body []byte) {
		if typ != "iprp" {
			return
		}
		eachBox(body, func(typ string, body []byte) {
			if typ != "ipco" {
				return
			}
			rotSeen := false
			eachBox(body, func(typ string, body []byte) {
				switch {
				case typ == "ispe" && len(body) >= 12:
					bw := binary.BigEndian.Uint32(body[4:8])
					bh := binary.BigEndian.Uint32(body[8:12])
					if a := uint64(bw) * uint64(bh); a > best && bw < 1<<20 && bh < 1<<20 {
						best, w, h = a, int(bw), int(bh)
					}
				case typ == "irot" && len(body) >= 1 && !rotSeen:
					rotSeen, quarterTurns = true, int(body[0]&3)
				}
			})
		})
	})
	return
}

// heicExifLocation finds the file offset and length of the EXIF item: its ID from
// 'iinf', then its first extent from 'iloc'. Items stored in an 'idat' box rather
// than at a file offset are not followed; phones do not store EXIF that way.
func heicExifLocation(meta []byte) (offset, length uint64, ok bool) {
	var exifID uint32
	var found bool
	var iloc []byte
	eachBox(meta, func(typ string, body []byte) {
		switch typ {
		case "iinf":
			exifID, found = heicExifItemID(body)
		case "iloc":
			iloc = body
		}
	})
	if !found || iloc == nil {
		return 0, 0, false
	}
	return heicItemExtent(iloc, exifID)
}

// heicExifItemID reads 'iinf' for the item whose type is "Exif".
func heicExifItemID(b []byte) (uint32, bool) {
	if len(b) < 6 {
		return 0, false
	}
	version := b[0]
	pos := 4
	if version == 0 {
		pos += 2 // a 16-bit entry count
	} else {
		pos += 4
	}
	if pos > len(b) {
		return 0, false
	}
	var id uint32
	var ok bool
	eachBox(b[pos:], func(typ string, body []byte) {
		if typ != "infe" || ok || len(body) < 4 {
			return
		}
		v := body[0]
		p := 4
		var itemID uint32
		switch {
		case v == 2 && len(body) >= p+8:
			itemID = uint32(binary.BigEndian.Uint16(body[p:]))
			p += 2
		case v >= 3 && len(body) >= p+10:
			itemID = binary.BigEndian.Uint32(body[p:])
			p += 4
		default:
			return
		}
		p += 2 // protection index
		if string(body[p:p+4]) == "Exif" {
			id, ok = itemID, true
		}
	})
	return id, ok
}

// heicItemExtent reads 'iloc' for an item's first extent.
func heicItemExtent(b []byte, want uint32) (offset, length uint64, ok bool) {
	if len(b) < 8 {
		return 0, 0, false
	}
	version := b[0]
	offSize, lenSize := int(b[4]>>4), int(b[4]&15)
	baseSize, indexSize := int(b[5]>>4), int(b[5]&15)
	pos := 6
	var count uint32
	if version < 2 {
		count = uint32(binary.BigEndian.Uint16(b[pos:]))
		pos += 2
	} else {
		if len(b) < pos+4 {
			return 0, 0, false
		}
		count = binary.BigEndian.Uint32(b[pos:])
		pos += 4
	}
	read := func(n int) (uint64, bool) {
		if n == 0 {
			return 0, true
		}
		if (n != 4 && n != 8) || pos+n > len(b) {
			return 0, false
		}
		var v uint64
		if n == 4 {
			v = uint64(binary.BigEndian.Uint32(b[pos:]))
		} else {
			v = binary.BigEndian.Uint64(b[pos:])
		}
		pos += n
		return v, true
	}
	for i := uint32(0); i < count && i < 4096; i++ {
		var id uint32
		if version < 2 {
			if pos+2 > len(b) {
				return 0, 0, false
			}
			id = uint32(binary.BigEndian.Uint16(b[pos:]))
			pos += 2
		} else {
			if pos+4 > len(b) {
				return 0, 0, false
			}
			id = binary.BigEndian.Uint32(b[pos:])
			pos += 4
		}
		method := 0
		if version == 1 || version == 2 {
			if pos+2 > len(b) {
				return 0, 0, false
			}
			method = int(binary.BigEndian.Uint16(b[pos:]) & 15)
			pos += 2
		}
		pos += 2 // data reference index
		base, ok1 := read(baseSize)
		if pos+2 > len(b) || !ok1 {
			return 0, 0, false
		}
		extents := int(binary.BigEndian.Uint16(b[pos:]))
		pos += 2
		for e := 0; e < extents && e < 1024; e++ {
			if (version == 1 || version == 2) && indexSize > 0 {
				if _, ok := read(indexSize); !ok {
					return 0, 0, false
				}
			}
			off, okOff := read(offSize)
			n, okLen := read(lenSize)
			if !okOff || !okLen {
				return 0, 0, false
			}
			if id == want && e == 0 && method == 0 {
				return base + off, n, n > 0
			}
		}
	}
	return 0, 0, false
}

// parseHEICExif reads an EXIF item's payload: a 32-bit offset to the TIFF header,
// usually pointing past an "Exif\0\0" marker, then the TIFF structure.
func parseHEICExif(b []byte) Info {
	if len(b) < 4 {
		return Info{Orientation: 1}
	}
	skip := uint64(binary.BigEndian.Uint32(b)) + 4
	if skip > uint64(len(b)) {
		return Info{Orientation: 1}
	}
	return Parse(b[skip:])
}
