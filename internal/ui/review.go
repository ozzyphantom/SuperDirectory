package ui

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/engine"
	"github.com/ozzyphantom/SuperDirectory/internal/exif"
	"github.com/ozzyphantom/SuperDirectory/internal/guard"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
	"github.com/ozzyphantom/SuperDirectory/internal/review"
)

// ErrReviewBack means the user left the review with esc: back to the prompt that
// opened it, with nothing changed.
var ErrReviewBack = errors.New("back from the review")

// Review shows the duplicate sets one by one, with pictures side by side, and
// returns the sets as the user left them: a keeper changed, or a set kept whole.
func Review(f *engine.Found, sets []engine.DupSet) ([]engine.DupSet, error) {
	in := make([]review.Set, len(sets))
	members := make([][]int, len(sets))
	for k, s := range sets {
		members[k] = append([]int{s.Keep}, s.Skip...)
		rs := review.Set{Kind: kindShort(s.Kind)}
		for _, i := range members[k] {
			it := f.Items[i]
			m := review.Member{Path: it.Src, Size: it.Size, ModTime: it.ModTime}
			if d, ok := f.Dims[i]; ok {
				m.Dims = fmt.Sprintf("%d×%d", d.W, d.H)
			}
			rs.Members = append(rs.Members, m)
		}
		in[k] = rs
	}
	out, err := review.Run(in, review.Options{Thumbnail: thumbnail})
	switch {
	case errors.Is(err, review.ErrBack):
		return nil, ErrReviewBack
	case err != nil:
		return nil, engine.ErrAbandoned
	}
	var chosen []engine.DupSet
	for k, rs := range out {
		if rs.KeepAll || k >= len(members) {
			continue
		}
		keep := members[k][rs.Keep]
		set := engine.DupSet{Kind: sets[k].Kind, Keep: keep, Score: sets[k].Score}
		for _, i := range members[k] {
			if i != keep {
				set.Skip = append(set.Skip, i)
				set.Bytes += f.Items[i].Size
			}
		}
		chosen = append(chosen, set)
	}
	return chosen, nil
}

func kindShort(kind string) string {
	switch kind {
	case job.Pictures:
		return "Smaller copy"
	case job.Documents:
		return "Similar document"
	}
	return "Identical"
}

// thumbnail loads a picture for the review screen through the stall guard, turned
// the way its EXIF says it is shown. Formats without a decoder (HEIC, RAW) show
// no preview.
func thumbnail(path string) (image.Image, error) {
	g := guard.Reader{Stall: 15 * time.Second}
	return guard.Read(g, path, func(f exif.File) (image.Image, error) {
		orientation := 1
		switch strings.ToLower(filepath.Ext(path)) {
		case ".jpg", ".jpeg", ".jpe", ".jfif":
			if info, err := exif.JPEG(f); err == nil {
				orientation = info.Orientation
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
		}
		img, _, err := image.Decode(f)
		if err != nil {
			return nil, err
		}
		return oriented{img, orientation}, nil
	})
}

// oriented shows an image turned by an EXIF orientation without copying its
// pixels: a thumbnail samples a few thousand of them, and rotating all 24 million
// to read those would waste a second and a hundred megabytes.
type oriented struct {
	image.Image
	o int
}

func (r oriented) Bounds() image.Rectangle {
	b := r.Image.Bounds()
	if r.o >= 5 && r.o <= 8 {
		return image.Rect(0, 0, b.Dy(), b.Dx())
	}
	return image.Rect(0, 0, b.Dx(), b.Dy())
}

func (r oriented) At(x, y int) color.Color {
	b := r.Image.Bounds()
	w, h := b.Dx(), b.Dy()
	var sx, sy int
	switch r.o {
	case 2:
		sx, sy = w-1-x, y
	case 3:
		sx, sy = w-1-x, h-1-y
	case 4:
		sx, sy = x, h-1-y
	case 5:
		sx, sy = y, x
	case 6:
		sx, sy = y, h-1-x
	case 7:
		sx, sy = w-1-y, h-1-x
	case 8:
		sx, sy = w-1-y, x
	default:
		sx, sy = x, y
	}
	return r.Image.At(b.Min.X+sx, b.Min.Y+sy)
}
