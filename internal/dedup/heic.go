package dedup

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
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

// readHEICHeader reads a HEIC or HEIF picture's size and rotation from its item
// properties, without decoding it. A HEIC from a phone stores the picture as a grid
// of tiles plus a thumbnail, each with its own size property ('ispe'). The largest
// is the picture. A rotation property ('irot') turns it at display.
func readHEICHeader(r io.ReadSeeker) (header, error) {
	h := header{orientation: 1}
	for range 64 { // top-level boxes; 'meta' comes within the first few
		size, typ, hdr, err := readBoxHeader(r)
		if err != nil {
			return h, err
		}
		if typ != "meta" {
			if size == 0 {
				break // the box runs to the end of the file, and it was not meta
			}
			if _, err := r.Seek(int64(size-hdr), io.SeekCurrent); err != nil {
				return h, err
			}
			continue
		}
		if size == 0 || size-hdr > 4<<20 {
			return h, errors.New("heic: implausible meta box")
		}
		body := make([]byte, size-hdr)
		if _, err := io.ReadFull(r, body); err != nil {
			return h, err
		}
		w, ht, quarterTurns := heicProperties(body)
		if w == 0 || ht == 0 {
			return h, errors.New("heic: no image size")
		}
		h.w, h.h = w, ht
		// irot counts quarter turns anticlockwise. In EXIF terms, a quarter turn
		// anticlockwise is orientation 8 and three of them are orientation 6. Only
		// the width-and-height swap matters here; sips renders the pixels upright.
		switch quarterTurns {
		case 1:
			h.orientation = 8
		case 2:
			h.orientation = 3
		case 3:
			h.orientation = 6
		}
		return h, nil
	}
	return h, errors.New("heic: no meta box")
}

// readBoxHeader reads an ISO base media box header: the box's total size, its type,
// and how many bytes the header itself took. A size of 0 means "to end of file".
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
		err = errors.New("heic: bad box size")
	}
	return
}

// heicProperties searches a meta box's body for the largest 'ispe' size and the
// first 'irot' rotation.
func heicProperties(meta []byte) (w, h, quarterTurns int) {
	if len(meta) < 4 {
		return
	}
	var best uint64
	eachBox(meta[4:], func(typ string, body []byte) { // meta is a full box: skip version and flags
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
			return nil, errCanceled
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
	if h, err := readJPEGHeader(bytes.NewReader(data)); err == nil {
		orientation = h.orientation
	}
	return fingerprintBytes(data, orientation)
}
