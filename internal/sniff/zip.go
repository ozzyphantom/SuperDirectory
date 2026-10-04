package sniff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

const (
	zipTail = 64 << 10 // the end record is looked for in the file's last 64 KiB
	zipDir  = 8 << 20  // the most of a central directory read
	zipMime = 1 << 10  // the most of a "mimetype" entry read
)

// zipType names a ZIP archive by its entries. Office Open XML has
// [Content_Types].xml and a folder for its application: word/, xl/ or ppt/.
// OpenDocument and EPUB store a "mimetype" entry first, uncompressed, so a reader
// finds it without inflating anything. A Java archive has META-INF/MANIFEST.MF and
// an Android package AndroidManifest.xml; an APK has both, and the Android one
// wins.
//
// It reads the end record, the central directory and the mimetype entry, never
// compressed data. An archive whose directory cannot be found stays "zip".
func zipType(r io.ReaderAt, size int64) (string, error) {
	tail := make([]byte, min(size, zipTail))
	tailAt := size - int64(len(tail))
	if err := readAt(r, size, tail, tailAt); err != nil {
		return failed("zip", err)
	}
	i := endRecord(tail)
	if i < 0 {
		return "zip", nil
	}
	end := tail[i:]
	endAt := tailAt + int64(i)
	dirLen := int64(binary.LittleEndian.Uint32(end[12:16]))
	dirAt := int64(binary.LittleEndian.Uint32(end[16:20]))
	if dirLen == 0 {
		return "zip", nil // an empty archive
	}
	if dirLen == 0xffffffff || dirAt == 0xffffffff {
		// ZIP64 keeps the directory's place in another record. Only archives past
		// 4 GiB need it, and no document format grows that large.
		return "zip", nil
	}

	dir := make([]byte, min(dirLen, zipDir, size))
	shift := int64(0)
	if err := readAt(r, size, dir, dirAt); err != nil || !hasPrefix(dir, "PK\x01\x02") {
		if err != nil && !errors.Is(err, errShort) {
			return "", err
		}
		// Data in front of an archive shifts every offset in it. Its directory
		// still sits where it must: just before the end record.
		shift = endAt - dirLen - dirAt
		if shift == 0 {
			return "zip", nil
		}
		if err := readAt(r, size, dir, dirAt+shift); err != nil {
			return failed("zip", err)
		}
	}

	var types, word, xl, ppt, manifest, android bool
	mimeAt, mimeLen := int64(-1), int64(0)
	for first := true; len(dir) >= 46 && hasPrefix(dir, "PK\x01\x02"); first = false {
		nameEnd := 46 + int(binary.LittleEndian.Uint16(dir[28:30]))
		if nameEnd > len(dir) {
			break // the directory was cut off at the cap
		}
		name := string(dir[46:nameEnd])
		if first && name == "mimetype" && binary.LittleEndian.Uint16(dir[10:12]) == 0 { // stored
			mimeAt = int64(binary.LittleEndian.Uint32(dir[42:46]))
			mimeLen = int64(binary.LittleEndian.Uint32(dir[24:28]))
		}
		switch {
		case name == "[Content_Types].xml":
			types = true
		case strings.HasPrefix(name, "word/"):
			word = true
		case strings.HasPrefix(name, "xl/"):
			xl = true
		case strings.HasPrefix(name, "ppt/"):
			ppt = true
		case name == "META-INF/MANIFEST.MF":
			manifest = true
		case name == "AndroidManifest.xml":
			android = true
		}
		next := nameEnd + int(binary.LittleEndian.Uint16(dir[30:32])) + int(binary.LittleEndian.Uint16(dir[32:34]))
		if next > len(dir) {
			break
		}
		dir = dir[next:]
	}

	if mimeAt >= 0 {
		t, err := mimetypeType(r, size, mimeAt, shift, mimeLen)
		if err != nil || t != "" {
			return t, err
		}
	}
	switch {
	case types && word:
		return "docx", nil
	case types && xl:
		return "xlsx", nil
	case types && ppt:
		return "pptx", nil
	case android:
		return "apk", nil
	case manifest:
		return "jar", nil
	}
	return "zip", nil
}

// endRecord finds the end-of-central-directory record in the tail of a file: the
// last "PK\x05\x06" with room after it for its 22 bytes and the comment it
// declares.
func endRecord(tail []byte) int {
	for i := len(tail) - 22; i >= 0; i-- {
		if string(tail[i:i+4]) == "PK\x05\x06" && i+22+int(binary.LittleEndian.Uint16(tail[i+20:i+22])) <= len(tail) {
			return i
		}
	}
	return -1
}

// mimetypes are the media types an OpenDocument or EPUB "mimetype" entry holds.
var mimetypes = map[string]string{
	"application/vnd.oasis.opendocument.text":         "odt",
	"application/vnd.oasis.opendocument.spreadsheet":  "ods",
	"application/vnd.oasis.opendocument.presentation": "odp",
	"application/vnd.oasis.opendocument.graphics":     "odg",
	"application/epub+zip":                            "epub",
}

// mimetypeType reads a stored "mimetype" entry through its local header, whose
// name and extra field lengths can differ from the central directory's. The
// entry's offset is tried shifted like the directory, then as written, since a
// directory can be misplaced without the entries moving. It returns "" for a
// media type it does not know.
func mimetypeType(r io.ReaderAt, size, at, shift, n int64) (string, error) {
	var h [30]byte
	err := readAt(r, size, h[:], at+shift)
	if (err != nil || !hasPrefix(h[:], "PK\x03\x04")) && shift != 0 {
		if err != nil && !errors.Is(err, errShort) {
			return "", err
		}
		err = readAt(r, size, h[:], at)
		shift = 0
	}
	if err != nil || !hasPrefix(h[:], "PK\x03\x04") {
		return failed("", err)
	}
	data := at + shift + 30 + int64(binary.LittleEndian.Uint16(h[26:28])) + int64(binary.LittleEndian.Uint16(h[28:30]))
	m := make([]byte, min(n, zipMime))
	if err := readAt(r, size, m, data); err != nil {
		return failed("", err)
	}
	return mimetypes[string(bytes.TrimSpace(m))], nil
}
