package dedup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/exif"
	"github.com/ozzyphantom/SuperDirectory/internal/guard"
)

// sipsPath is macOS's built-in image tool, which decodes HEIC. It is empty on every
// other system, where HEIC pictures are compared byte for byte only. Decoding HEIC
// in Go would mean bundling a codec several megabytes large; sips is already there.
var sipsPath = func() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	p, err := exec.LookPath("sips")
	if err != nil {
		return ""
	}
	return p
}()

// heicPrint fingerprints a HEIC picture by having sips render it as a small JPEG,
// then fingerprinting that. The rendering is upright, or carries an EXIF
// orientation that the JPEG path honors.
func heicPrint(path, tmpDir string, id int, cancel <-chan struct{}, limit time.Duration) (*print, error) {
	if !filepath.IsAbs(path) {
		path = "./" + path // never let a file name read as an option
	}
	out := filepath.Join(tmpDir, fmt.Sprintf("%d.jpg", id))
	defer os.Remove(out)

	ctx, stop := context.WithTimeout(context.Background(), limit)
	defer stop()
	go func() {
		select {
		case <-cancel:
			stop()
		case <-ctx.Done():
		}
	}()
	if err := exec.CommandContext(ctx, sipsPath, "-s", "format", "jpeg", "-Z", "256", path, "--out", out).Run(); err != nil {
		if closed(cancel) {
			return nil, guard.ErrCanceled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("sips gave no answer in %s", limit)
		}
		return nil, fmt.Errorf("sips could not read it: %w", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	orientation := 1
	if h, err := exif.JPEG(bytes.NewReader(data)); err == nil {
		orientation = h.Orientation
	}
	return fingerprintBytes(data, orientation)
}
