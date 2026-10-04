package dedup

import (
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

	"github.com/ozzyphantom/SuperDirectory/internal/exif"
)

// readOtherHeader reads the dimensions of a PNG, GIF, BMP, TIFF or WebP through the
// registered decoders, which parse only as far as they must.
func readOtherHeader(r io.Reader) (exif.Info, error) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return exif.Info{}, err
	}
	return exif.Info{Width: cfg.Width, Height: cfg.Height, Orientation: 1}, nil
}
