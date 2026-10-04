// Package exif reads what pictures and videos say about themselves: their size,
// which way up they go, a small embedded thumbnail, and when they were taken.
//
// It parses headers only, never pixel data. The bytes come from arbitrary files,
// so every offset is checked, every allocation driven by the file is capped, and
// malformed input yields an error or empty fields, never a crash.
package exif

import (
	"encoding/binary"
	"io"
	"strings"
	"time"
)

// File is how the readers reach a file: in order for headers, by offset for the
// formats that point around inside themselves.
type File interface {
	io.Reader
	io.Seeker
	io.ReaderAt
}

// Info is what a file's metadata says. Fields the format does not carry are zero.
type Info struct {
	Width, Height int       // stored pixel dimensions
	Orientation   int       // EXIF orientation, 1 to 8; 1 when absent
	Thumb         []byte    // embedded JPEG thumbnail, nil when absent
	Taken         time.Time // when it was taken; zero when unknown
}

// Displayed returns the dimensions the picture is shown at: an orientation of 5
// through 8 turns it a quarter, swapping width and height.
func (i Info) Displayed() (w, h int) {
	if i.Orientation >= 5 && i.Orientation <= 8 {
		return i.Height, i.Width
	}
	return i.Width, i.Height
}

// EXIF and TIFF tags this package reads.
const (
	tagOrientation       = 0x0112
	tagDateTime          = 0x0132
	tagExifIFD           = 0x8769
	tagDateTimeOriginal  = 0x9003
	tagDateTimeDigitized = 0x9004
	tagOffsetTime        = 0x9010
	tagOffsetTimeOrig    = 0x9011
	tagThumbOffset       = 0x0201
	tagThumbLength       = 0x0202
)

// Parse reads an EXIF block, which is a small TIFF file: the orientation from IFD0,
// the JPEG thumbnail from IFD1, and the capture time from the EXIF IFD.
//
// The capture time prefers DateTimeOriginal, the moment the shutter fired, over
// DateTimeDigitized and over IFD0's DateTime, which editors rewrite on save. When
// the camera recorded its UTC offset the time is placed in that zone; otherwise it
// is read as local time, which is what the camera's clock showed.
func Parse(tiff []byte) Info {
	info := Info{Orientation: 1}
	if len(tiff) < 8 {
		return info
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return info
	}
	if bo.Uint16(tiff[2:4]) != 42 {
		return info
	}

	ascii := func(typ uint16, count uint32, value []byte) string {
		if typ != 2 || count == 0 || count > 64 {
			return ""
		}
		var raw []byte
		if count <= 4 {
			raw = value[:count]
		} else {
			off := bo.Uint32(value)
			if uint64(off)+uint64(count) > uint64(len(tiff)) {
				return ""
			}
			raw = tiff[off : off+count]
		}
		return strings.TrimRight(string(raw), "\x00 ")
	}

	// entries visits an IFD's entries and returns the offset of the next IFD.
	entries := func(off uint32, visit func(tag, typ uint16, count uint32, value []byte)) uint32 {
		if off < 8 || uint64(off)+2 > uint64(len(tiff)) {
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

	var dateTime, original, digitized, offset, offsetOriginal string
	var exifIFD uint32
	dates := func(tag, typ uint16, count uint32, value []byte) {
		switch tag {
		case tagDateTimeOriginal:
			original = ascii(typ, count, value)
		case tagDateTimeDigitized:
			digitized = ascii(typ, count, value)
		case tagOffsetTime:
			offset = ascii(typ, count, value)
		case tagOffsetTimeOrig:
			offsetOriginal = ascii(typ, count, value)
		}
	}

	ifd0 := bo.Uint32(tiff[4:8])
	next := entries(ifd0, func(tag, typ uint16, count uint32, value []byte) {
		switch tag {
		case tagOrientation:
			if typ == 3 && count == 1 {
				if o := int(bo.Uint16(value)); o >= 1 && o <= 8 {
					info.Orientation = o
				}
			}
		case tagDateTime:
			dateTime = ascii(typ, count, value)
		case tagExifIFD:
			if typ == 4 || typ == 13 { // LONG or IFD
				exifIFD = bo.Uint32(value)
			}
		default:
			dates(tag, typ, count, value) // some formats (Canon CR3) keep EXIF tags in IFD0
		}
	})
	if exifIFD != 0 && exifIFD != ifd0 {
		entries(exifIFD, dates)
	}

	zone := offsetOriginal
	if zone == "" {
		zone = offset
	}
	for _, s := range []string{original, digitized, dateTime} {
		if t, ok := parseDate(s, zone); ok {
			info.Taken = t
			break
		}
	}

	if next != 0 && next != ifd0 {
		var thumbOff, thumbLen uint32
		entries(next, func(tag, typ uint16, count uint32, value []byte) {
			switch tag {
			case tagThumbOffset:
				thumbOff = bo.Uint32(value)
			case tagThumbLength:
				thumbLen = bo.Uint32(value)
			}
		})
		end := uint64(thumbOff) + uint64(thumbLen)
		if thumbOff != 0 && thumbLen >= 4 && end <= uint64(len(tiff)) {
			if t := tiff[thumbOff:end]; t[0] == 0xFF && t[1] == 0xD8 {
				info.Thumb = append([]byte(nil), t...) // own it; the block is discarded
			}
		}
	}
	return info
}

// parseDate reads an EXIF date, "2019:07:14 10:32:05", with an optional UTC offset
// such as "+02:00". All-zero dates, which cameras write when their clock was never
// set, are no date at all.
func parseDate(s, zone string) (time.Time, bool) {
	if len(s) < 19 || strings.HasPrefix(s, "0000") {
		return time.Time{}, false
	}
	loc := time.Local
	if z, err := time.Parse("-07:00", zone); err == nil {
		_, secs := z.Zone()
		loc = time.FixedZone(zone, secs)
	}
	t, err := time.ParseInLocation("2006:01:02 15:04:05", s[:19], loc)
	if err != nil || t.Year() < 1900 || t.Year() > 2200 {
		return time.Time{}, false
	}
	return t, true
}
