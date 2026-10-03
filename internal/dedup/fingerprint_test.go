package dedup

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand"
	"testing"

	xdraw "golang.org/x/image/draw"
)

// scene renders a photo-like picture: gradients, soft blobs, a few hard edges, noise.
// shift moves every blob, as camera motion between burst frames would.
func scene(w, h int, seed int64, shift float64) *image.RGBA {
	r := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	type blob struct{ x, y, rad, cr, cg, cb float64 }
	var blobs []blob
	for i := 0; i < 14; i++ {
		blobs = append(blobs, blob{r.Float64() + shift, r.Float64(), 0.04 + r.Float64()*0.25, r.Float64() * 255, r.Float64() * 255, r.Float64() * 255})
	}
	type bar struct{ x0, x1, y0, y1, v float64 }
	var bars []bar
	for i := 0; i < 5; i++ {
		x, y := r.Float64()+shift, r.Float64()
		bars = append(bars, bar{x, x + 0.05 + r.Float64()*0.2, y, y + 0.02 + r.Float64()*0.1, r.Float64() * 255})
	}
	noise := rand.New(rand.NewSource(seed * 7))
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			x, y := float64(px)/float64(w), float64(py)/float64(h)
			fr := 60 + 120*x
			fg := 80 + 100*y
			fb := 140 + 60*math.Sin((x+y)*6)
			for _, b := range blobs {
				d := math.Hypot(x-b.x, (y-b.y)*float64(h)/float64(w))
				if d < b.rad {
					t := 1 - d/b.rad
					fr, fg, fb = fr*(1-t)+b.cr*t, fg*(1-t)+b.cg*t, fb*(1-t)+b.cb*t
				}
			}
			for _, b := range bars {
				if x >= b.x0 && x < b.x1 && y >= b.y0 && y < b.y1 {
					fr, fg, fb = b.v, b.v*0.8, b.v*0.6
				}
			}
			n := noise.NormFloat64() * 5
			img.SetRGBA(px, py, color.RGBA{c8(fr + n), c8(fg + n), c8(fb + n), 255})
		}
	}
	return img
}

func c8(v float64) uint8 { return uint8(math.Max(0, math.Min(255, v))) }

func resized(src image.Image, w, h int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return dst
}

func reencoded(t *testing.T, img image.Image, q int) image.Image {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	out, err := jpeg.Decode(&b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func printOf(img image.Image) *print { g := shrink(img); return newPrint(&g) }

// TestFingerprintSeparatesCopiesFromOtherPictures pins the thresholds to measured
// behavior. Calibrated on 24 scenes at 1500×1000, copies sat at a hash distance of
// 4 or less and a grid distance of 0.05 or less (0.11 at 75×50, below the size
// floor); different scenes at 18+ and 0.56+, crops at 10+ and 0.23+.
//
// Burst frames are left out on purpose. A frame nudged 1% is indistinguishable from
// a resized copy by any measure, which is why FindSimilar only ever skips a copy
// at a smaller pixel size: frames from one burst share their size, and can never
// match each other.
func TestFingerprintSeparatesCopiesFromOtherPictures(t *testing.T) {
	const W, H = 900, 600
	match := func(a, b *print) bool { return similar(a, b) }

	var prints []*print
	for seed := int64(1); seed <= 6; seed++ {
		orig := scene(W, H, seed, 0)
		po := printOf(orig)
		prints = append(prints, po)

		copies := map[string]image.Image{
			"half size":             resized(orig, W/2, H/2),
			"quarter size":          resized(orig, W/4, H/4),
			"90×60":                 resized(orig, 90, 60),
			"jpeg q50":              reencoded(t, orig, 50),
			"third size, jpeg q60":  reencoded(t, resized(orig, W/3, H/3), 60),
			"160×107 thumbnail q75": reencoded(t, resized(orig, 160, 107), 75),
		}
		for name, c := range copies {
			if pc := printOf(c); !match(po, pc) {
				t.Errorf("seed %d, %s: not matched (hash %d, grid %.3f)", seed, name, hamming(po.hash, pc.hash), distance(po, pc))
			}
		}

		crop := image.NewRGBA(image.Rect(0, 0, W*8/10, H*8/10))
		xdraw.Copy(crop, image.Point{}, orig, image.Rect(W/10, H/10, W*9/10, H*9/10), xdraw.Src, nil)
		others := map[string]image.Image{
			"80% crop":       crop,
			"8% burst shift": scene(W, H, seed, 0.08),
		}
		for name, o := range others {
			if po2 := printOf(o); match(po, po2) {
				t.Errorf("seed %d, %s: matched (hash %d, grid %.3f)", seed, name, hamming(po.hash, po2.hash), distance(po, po2))
			}
		}
	}
	for i := range prints {
		for j := i + 1; j < len(prints); j++ {
			if match(prints[i], prints[j]) {
				t.Errorf("different scenes %d and %d matched", i+1, j+1)
			}
		}
	}
}

func TestFlatPicturesAreNotFingerprinted(t *testing.T) {
	blank := image.NewGray(image.Rect(0, 0, 400, 300))
	for i := range blank.Pix {
		blank.Pix[i] = 250
	}
	if p := printOf(blank); !p.flat {
		t.Error("a blank page should be too flat to compare")
	}
	if p := printOf(scene(400, 300, 1, 0)); p.flat {
		t.Error("a photo-like scene was called flat")
	}
}

// TestOrientTurnsTheGridUpright: a portrait photo stored sideways (orientation 6)
// must fingerprint like the same photo turned upright for real.
func TestOrientTurnsTheGridUpright(t *testing.T) {
	upright := scene(400, 600, 3, 0)
	// Store it the way a camera does for orientation 6: rotated 90° counter-clockwise.
	stored := image.NewRGBA(image.Rect(0, 0, 600, 400))
	for y := 0; y < 600; y++ {
		for x := 0; x < 400; x++ {
			stored.Set(y, 399-x, upright.At(x, y))
		}
	}
	g := shrink(stored).orient(6)
	turned := newPrint(&g)
	if want := printOf(upright); !similar(turned, want) {
		t.Errorf("orientation 6 not undone: hash %d, grid %.3f", hamming(turned.hash, want.hash), distance(turned, want))
	}
	for o := 1; o <= 8; o++ {
		g := shrink(upright)
		if back := g.orient(o).orient(inverse[o]); back != g {
			t.Errorf("orientation %d then its inverse %d is not the identity", o, inverse[o])
		}
	}
}

// inverse[o] undoes orientation o.
var inverse = [9]int{0, 1, 2, 3, 4, 5, 8, 7, 6}
