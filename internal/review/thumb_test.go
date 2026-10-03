package review

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var (
	red    = color.RGBA{R: 255, A: 255}
	blue   = color.RGBA{B: 255, A: 255}
	green  = color.RGBA{G: 255, A: 255}
	orange = color.RGBA{R: 255, G: 128, A: 255}
)

func solid(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	return img
}

// stripes is a picture too large to allocate: one-pixel columns, black and white
// in turn, made on demand. It has no RGBA64At, so it also takes At's path.
type stripes struct{ w, h int }

func (s stripes) ColorModel() color.Model { return color.GrayModel }
func (s stripes) Bounds() image.Rectangle { return image.Rect(0, 0, s.w, s.h) }
func (s stripes) At(x, y int) color.Color {
	if x%2 == 0 {
		return color.Gray{}
	}
	return color.Gray{Y: 255}
}

// is reports whether d is c, give or take one step of rounding per channel.
func is(d dot, c color.RGBA) bool {
	near := func(a, b uint8) bool { return int(a)-int(b) >= -1 && int(a)-int(b) <= 1 }
	return d.on && near(d.r, c.R) && near(d.g, c.G) && near(d.b, c.B)
}

func TestThumbIsExactlyTheBoxAsked(t *testing.T) {
	pictures := map[string]image.Image{
		"4:3":      solid(40, 30, red),
		"odd":      solid(7, 5, red),
		"one":      solid(1, 1, red),
		"tall":     solid(3, 200, red),
		"stripes":  stripes{w: 64, h: 64},
		"infinite": image.NewUniform(red),
	}
	for name, img := range pictures {
		for _, box := range [][2]int{{1, 1}, {4, 2}, {5, 3}, {17, 8}, {28, 14}} {
			cols, rows := box[0], box[1]
			lines := strings.Split(Thumb(img, cols, rows), "\n")
			if len(lines) != rows {
				t.Errorf("%s in %d×%d: %d lines", name, cols, rows, len(lines))
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w != cols {
					t.Errorf("%s in %d×%d: a line is %d columns: %q", name, cols, rows, w, l)
				}
			}
		}
	}
}

func TestThumbOfASolidPicture(t *testing.T) {
	for i, d := range pixels(solid(8, 8, orange), 4, 4) {
		if !is(d, orange) {
			t.Fatalf("pixel %d = %+v, want orange", i, d)
		}
	}
	if got := Thumb(solid(8, 8, orange), 4, 2); got != "▀▀▀▀\n▀▀▀▀" {
		t.Errorf("Thumb = %q, want two full rows of half blocks", got)
	}
}

func TestThumbAveragesThePixelsUnderEachDot(t *testing.T) {
	// Red and blue in a checkerboard average to purple.
	board := image.NewRGBA(image.Rect(0, 0, 2, 2))
	board.Set(0, 0, red)
	board.Set(1, 1, red)
	board.Set(1, 0, blue)
	board.Set(0, 1, blue)
	if d := pixels(board, 1, 1)[0]; !is(d, color.RGBA{R: 128, B: 128}) {
		t.Errorf("a red and blue checkerboard came out %+v, want purple", d)
	}

	// One-pixel stripes average to grey. Taking the pixel under each dot's center
	// instead would draw them solid black or solid white.
	for i, d := range pixels(stripes{w: 64, h: 64}, 4, 4) {
		if !is(d, color.RGBA{R: 128, G: 128, B: 128}) {
			t.Fatalf("stripes, dot %d = %+v, want grey", i, d)
		}
	}

	// A ramp from black to red, shrunk to four dots, gives each dot the mean of
	// its quarter.
	ramp := image.NewRGBA(image.Rect(0, 0, 256, 2))
	for x := range 256 {
		ramp.Set(x, 0, color.RGBA{R: uint8(x), A: 255})
		ramp.Set(x, 1, color.RGBA{R: uint8(x), A: 255})
	}
	dots := pixels(ramp, 4, 2)
	for i, want := range []uint8{32, 96, 160, 224} {
		if !is(dots[i], color.RGBA{R: want}) {
			t.Errorf("ramp, dot %d = %+v, want red %d", i, dots[i], want)
		}
	}
}

func TestThumbKeepsTheShapeAndCenters(t *testing.T) {
	if got := Thumb(solid(6, 2, red), 6, 3); got != "      \n▀▀▀▀▀▀\n      " {
		t.Errorf("a wide picture should sit centered across the box:\n%q", got)
	}
	if got := Thumb(solid(2, 6, red), 6, 3); got != "  ▀▀  \n  ▀▀  \n  ▀▀  " {
		t.Errorf("a tall picture should stand centered in the box:\n%q", got)
	}
	// Centered exactly, this square would start on an odd pixel row and split its
	// top cell. It starts a pixel higher instead.
	if got := Thumb(solid(4, 4, red), 4, 5); got != "    \n▀▀▀▀\n▀▀▀▀\n    \n    " {
		t.Errorf("a picture should start on a character row:\n%q", got)
	}
}

func TestThumbOfAnOddSizedPicture(t *testing.T) {
	// Five by three fills five by four pixels but for its last row, which leaves
	// the bottom half of the last character row blank.
	dots := pixels(solid(5, 3, red), 5, 4)
	for i, d := range dots {
		if row := i / 5; d.on != (row < 3) {
			t.Errorf("pixel %d (row %d) on=%v", i, row, d.on)
		}
	}
	// Seven by five in a five by six pixel box scales to five by four.
	on := 0
	for _, d := range pixels(solid(7, 5, red), 5, 6) {
		if d.on {
			on++
		}
	}
	if on != 20 {
		t.Errorf("seven by five drew %d pixels, want 20", on)
	}
}

func TestThumbReadsFromTheImageOrigin(t *testing.T) {
	base := solid(20, 20, red)
	draw.Draw(base, image.Rect(10, 10, 20, 20), image.NewUniform(green), image.Point{}, draw.Src)
	for i, d := range pixels(base.SubImage(image.Rect(10, 10, 20, 20)), 4, 4) {
		if !is(d, green) {
			t.Fatalf("a sub-image drew pixel %d as %+v, want its own green", i, d)
		}
	}

	// Bounds may start below zero.
	img := image.NewRGBA(image.Rect(-5, -5, 5, 5))
	draw.Draw(img, image.Rect(-5, -5, 0, 5), image.NewUniform(red), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, -5, 5, 5), image.NewUniform(blue), image.Point{}, draw.Src)
	if dots := pixels(img, 2, 2); !is(dots[0], red) || !is(dots[1], blue) || !is(dots[2], red) || !is(dots[3], blue) {
		t.Errorf("negative bounds drew %+v, want red then blue on each row", dots)
	}
}

func TestThumbOfTinyAndHugePictures(t *testing.T) {
	for i, d := range pixels(solid(1, 1, blue), 4, 4) {
		if !is(d, blue) {
			t.Fatalf("one pixel should fill the box, pixel %d = %+v", i, d)
		}
	}
	// image.Uniform reports a square two billion pixels on a side.
	for i, d := range pixels(image.NewUniform(orange), 8, 8) {
		if !is(d, orange) {
			t.Fatalf("an unbounded picture drew pixel %d as %+v", i, d)
		}
	}
	// Sampled rather than averaged in full, stripes must still come out grey. A
	// run of 64 pixels, an exact multiple of the samples taken, is where a fixed
	// stride would land on one color every time.
	for _, side := range []int{256, 100_000} {
		for i, d := range pixels(stripes{w: side, h: side}, 4, 4) {
			if !d.on || d.r < 112 || d.r > 144 {
				t.Fatalf("stripes %d wide, dot %d = %+v, want about grey", side, i, d)
			}
		}
	}
}

func TestThumbLeavesTransparencyBlank(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	draw.Draw(img, image.Rect(1, 1, 3, 3), image.NewUniform(red), image.Point{}, draw.Src)
	if got := Thumb(img, 4, 2); got != " ▄▄ \n ▀▀ " {
		t.Errorf("transparent pixels should be blank cells, got:\n%q", got)
	}
	// Partly transparent pixels keep their own color, not one darkened toward
	// black.
	faint := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	draw.Draw(faint, faint.Bounds(), image.NewUniform(color.NRGBA{R: 255, A: 160}), image.Point{}, draw.Src)
	if d := pixels(faint, 1, 1)[0]; !is(d, red) {
		t.Errorf("a faint red pixel came out %+v, want red", d)
	}
}

func TestThumbOfNothing(t *testing.T) {
	img := solid(4, 4, red)
	if Thumb(img, 0, 3) != "" || Thumb(img, 3, 0) != "" || Thumb(img, -1, -1) != "" {
		t.Error("a box with no area should draw nothing")
	}
	for _, empty := range []image.Image{nil, image.NewRGBA(image.Rect(0, 0, 0, 5))} {
		if got := Thumb(empty, 3, 2); got != "   \n   " {
			t.Errorf("no picture should draw a blank box, got %q", got)
		}
	}
}

func TestShrinkKeepsTheCacheSmall(t *testing.T) {
	cases := []struct {
		name string
		img  image.Image
		w, h int
	}{
		{"4:3", stripes{w: 4000, h: 3000}, 56, 42},
		{"unbounded", image.NewUniform(red), 56, 56},
		{"small", solid(10, 8, red), 10, 8}, // never enlarged
		{"sub-image", solid(30, 30, red).SubImage(image.Rect(10, 10, 30, 20)), 20, 10},
	}
	for _, c := range cases {
		got := shrink(c.img, keptSide, keptSide)
		if b := got.Bounds(); b != image.Rect(0, 0, c.w, c.h) {
			t.Errorf("%s: shrink made %v, want %d×%d at the origin", c.name, b, c.w, c.h)
		}
	}
	if got := shrink(solid(10, 8, orange), keptSide, keptSide); got.NRGBAAt(5, 5) != (color.NRGBA{R: 255, G: 128, A: 255}) {
		t.Errorf("shrink changed a pixel it kept: %v", got.NRGBAAt(5, 5))
	}
}
