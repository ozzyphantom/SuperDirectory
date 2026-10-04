package dedup

import (
	"crypto/sha256"
	"encoding/hex"
	"io"

	"github.com/ozzyphantom/SuperDirectory/internal/exif"
	"github.com/ozzyphantom/SuperDirectory/internal/guard"
)

// hashFile returns the SHA-256 of the file, or of its first limit bytes when limit
// is not negative, read through the guard.
func hashFile(r guard.Reader, path string, limit int64) (string, error) {
	return guard.Read(r, path, func(f exif.File) (string, error) {
		var src io.Reader = f
		if limit >= 0 {
			src = io.LimitReader(f, limit)
		}
		h := sha256.New()
		if _, err := io.CopyBuffer(h, src, make([]byte, 256<<10)); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	})
}
