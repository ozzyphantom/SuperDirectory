package review

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// maxTaps caps the samples averaged along each side of one output pixel. A run
// of up to maxTaps source pixels is averaged in full. A longer one is cut into
// maxTaps equal stretches, and one pixel of each is averaged. That bounds the
// work on a huge picture, or an unbounded one such as image.Uniform.
//
// Where in its stretch a sample falls is a hash of its place. Any regular rule,
// the center or a stride, lines up with fine stripes at some size and draws them
// solid; hashed places turn them into their average, give or take a little noise.
const maxTaps = 32

// Thumb renders img as half-block characters: cols wide, rows tall, two pixel rows
// per character row ("▀" with the top pixel as foreground and the bottom as
// background), aspect preserved inside the box. Exported for tests and reuse.
//
// The picture is scaled to fit and centered. Each output pixel is the average of
// the source pixels under it, or of a sample of them on a large picture (see
// maxTaps), so fine detail blends instead of flickering between neighbors. Where
// the box is empty, or the picture is mostly transparent, the cell is left blank
// and the terminal's background shows. Every line is exactly cols wide. Colors go
// through lipgloss, so they follow the terminal's color support like the rest of
// the app.
func Thumb(img image.Image, cols, rows int) string {
	if cols <= 0 || rows <= 0 {
		return ""
	}
	return paint(pixels(img, cols, rows*2), cols, rows)
}

// dot is one output pixel: a color, or nothing.
type dot struct {
	r, g, b uint8
	on      bool
}

// pixels lays img out in a w×h pixel box, scaled to fit and centered, and returns
// the box row by row.
func pixels(img image.Image, w, h int) []dot {
	out := make([]dot, w*h)
	if img == nil || img.Bounds().Empty() {
		return out
	}
	b := img.Bounds()
	ow, oh := fit(int64(b.Dx()), int64(b.Dy()), int64(w), int64(h))
	ox, oy := (w-ow)/2, (h-oh)/2
	oy -= oy % 2 // start on a character row, so the top edge is not split
	for i, c := range average(img, ow, oh) {
		out[(oy+i/ow)*w+ox+i%ow] = toDot(c)
	}
	return out
}

// fit scales a sw×sh picture to fill as much of a w×h box as its shape allows,
// never below one pixel on a side.
func fit(sw, sh, w, h int64) (int, int) {
	if sw*h >= sh*w { // at least as wide as the box, for its height: width decides
		return int(w), int(min(max((2*sh*w+sw)/(2*sw), 1), h))
	}
	return int(min(max((2*sw*h+sh)/(2*sh), 1), w)), int(h)
}

// average resamples the whole of img to a w×h grid of premultiplied colors, row
// by row. Shrinking, each output pixel is the mean of the source pixels under it;
// enlarging, it is the source pixel under its center.
func average(img image.Image, w, h int) []color.RGBA64 {
	b := img.Bounds()
	at := reader(img)
	out := make([]color.RGBA64, 0, w*h)
	for y := range h {
		ylo, yhi := span(int64(y), int64(h), int64(b.Dy()))
		for x := range w {
			xlo, xhi := span(int64(x), int64(w), int64(b.Dx()))
			out = append(out, boxMean(at, b.Min, xlo, xhi, ylo, yhi))
		}
	}
	return out
}

// boxMean is the mean color of the source pixels in [xlo, xhi) × [ylo, yhi),
// counted from origin. Along a side longer than maxTaps it samples; see maxTaps.
func boxMean(at func(x, y int) color.RGBA64, origin image.Point, xlo, xhi, ylo, yhi int64) color.RGBA64 {
	kx, ky := min(xhi-xlo, maxTaps), min(yhi-ylo, maxTaps)
	var r, g, b, a uint64
	for j := range ky {
		y0, y1 := stretch(ylo, yhi, ky, j)
		for i := range kx {
			x0, x1 := stretch(xlo, xhi, kx, i)
			h := mix(x0, y0)
			sx := x0 + int64(h%uint64(x1-x0))
			sy := y0 + int64((h>>32)%uint64(y1-y0))
			c := at(origin.X+int(sx), origin.Y+int(sy))
			r, g, b, a = r+uint64(c.R), g+uint64(c.G), b+uint64(c.B), a+uint64(c.A)
		}
	}
	n := uint64(kx * ky)
	return color.RGBA64{
		R: uint16((r + n/2) / n), G: uint16((g + n/2) / n),
		B: uint16((b + n/2) / n), A: uint16((a + n/2) / n),
	}
}

// span is the run of source pixels [lo, hi) under output pixel i of n, on an
// axis size pixels long. Shrinking, the runs tile the axis. Enlarging, a run would
// be empty, so it is the one pixel under the output pixel's center.
func span(i, n, size int64) (lo, hi int64) {
	lo, hi = i*size/n, (i+1)*size/n
	if hi > lo {
		return lo, hi
	}
	lo = (2*i + 1) * size / (2 * n)
	return lo, lo + 1
}

// stretch is the i-th of k equal stretches of [lo, hi). With k equal to the
// length, each stretch is one pixel.
func stretch(lo, hi, k, i int64) (int64, int64) {
	n := hi - lo
	return lo + i*n/k, lo + (i+1)*n/k
}

// mix hashes a sample's place, to pick where in its stretch it falls. It is
// splitmix64's finalizer: every bit of the place stirs every bit of the result.
func mix(x, y int64) uint64 {
	z := uint64(x)*0x9e3779b97f4a7c15 ^ uint64(y)*0xc2b2ae3d27d4eb4f
	z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
	z = (z ^ z>>27) * 0x94d049bb133111eb
	return z ^ z>>31
}

// reader returns img's pixel accessor. Every image type in the standard library
// has RGBA64At, which returns a plain value; At returns an interface, which
// costs an allocation per pixel.
func reader(img image.Image) func(x, y int) color.RGBA64 {
	if f, ok := img.(image.RGBA64Image); ok {
		return f.RGBA64At
	}
	return func(x, y int) color.RGBA64 {
		r, g, b, a := img.At(x, y).RGBA()
		return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
	}
}

// toDot turns an averaged, premultiplied color into a terminal pixel. Under half
// coverage the pixel is blank, so a transparent background reads as the
// terminal's own on dark and light themes alike. Otherwise it takes the color of
// the visible part.
func toDot(c color.RGBA64) dot {
	if c.A < 0x8000 {
		return dot{}
	}
	straight := func(v uint16) uint8 {
		s := min(uint32(v)*0xffff/uint32(c.A), 0xffff)
		return uint8((s*0xff + 0x7fff) / 0xffff)
	}
	return dot{r: straight(c.R), g: straight(c.G), b: straight(c.B), on: true}
}

// paint draws a w-pixel-wide box of dots as rows lines of half blocks. A run of
// identical cells is styled once.
func paint(dots []dot, w, rows int) string {
	var b strings.Builder
	for r := range rows {
		if r > 0 {
			b.WriteByte('\n')
		}
		top, bottom := dots[2*r*w:(2*r+1)*w], dots[(2*r+1)*w:(2*r+2)*w]
		for x := 0; x < w; {
			n := 1
			for x+n < w && top[x+n] == top[x] && bottom[x+n] == bottom[x] {
				n++
			}
			b.WriteString(cells(top[x], bottom[x], n))
			x += n
		}
	}
	return b.String()
}

// cells draws n identical cells with top pixel t and bottom pixel u. A cell with
// one pixel set draws only that half, and leaves the other to the background.
func cells(t, u dot, n int) string {
	switch {
	case t.on && u.on:
		return lipgloss.NewStyle().Foreground(t.color()).Background(u.color()).Render(strings.Repeat("▀", n))
	case t.on:
		return lipgloss.NewStyle().Foreground(t.color()).Render(strings.Repeat("▀", n))
	case u.on:
		return lipgloss.NewStyle().Foreground(u.color()).Render(strings.Repeat("▄", n))
	default:
		return strings.Repeat(" ", n)
	}
}

func (d dot) color() lipgloss.Color {
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", d.r, d.g, d.b))
}

// shrink returns a copy of img no larger than w×h, its shape kept. The cache holds
// these instead of decoded pictures, which can run to hundreds of megabytes each.
func shrink(img image.Image, w, h int) *image.NRGBA {
	b := img.Bounds()
	ow, oh := b.Dx(), b.Dy()
	if ow > w || oh > h {
		ow, oh = fit(int64(ow), int64(oh), int64(w), int64(h))
	}
	out := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	for i, c := range average(img, ow, oh) {
		out.Set(i%ow, i/ow, c)
	}
	return out
}
