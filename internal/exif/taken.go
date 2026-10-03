package exif

import (
	"path/filepath"
	"strings"
	"time"
)

// Taken reads when a photo or video was taken from its own metadata, choosing the
// reader by name's extension. ok is false when the file does not say.
func Taken(name string, f File, size int64) (time.Time, bool) {
	var info Info
	var err error
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")) {
	case "jpg", "jpeg", "jpe", "jfif":
		info, err = JPEG(f)
	case "heic", "heif", "avif":
		info, err = HEIC(f)
	case "tif", "tiff", "nef", "nrw", "cr2", "dng", "arw", "sr2", "srf", "orf", "pef", "srw", "rw2", "3fr", "erf", "kdc", "mef", "mos", "iiq":
		info, err = TIFF(f, size)
	case "raf":
		info, err = RAF(f, size)
	case "cr3":
		info, err = CR3(f)
	case "mp4", "mov", "m4v", "3gp", "3g2", "qt":
		info, err = Video(f)
	case "png":
		info, err = PNG(f)
	case "webp":
		info, err = WebP(f)
	default:
		return time.Time{}, false
	}
	if err != nil || info.Taken.IsZero() {
		return time.Time{}, false
	}
	return info.Taken, true
}

// Supported reports whether Taken can read name's format.
func Supported(name string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")) {
	case "jpg", "jpeg", "jpe", "jfif", "heic", "heif", "avif",
		"tif", "tiff", "nef", "nrw", "cr2", "dng", "arw", "sr2", "srf", "orf", "pef", "srw", "rw2", "3fr", "erf", "kdc", "mef", "mos", "iiq",
		"raf", "cr3", "mp4", "mov", "m4v", "3gp", "3g2", "qt", "png", "webp":
		return true
	}
	return false
}
