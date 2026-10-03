package dedup

import (
	"image"
	"math"
	"math/bits"
	"sort"
)

// grid is the side of the square every picture is shrunk to before it is compared.
const grid = 32

// gray is a picture shrunk to a grid×grid square of luma values, 0–255.
type gray [grid * grid]float32

// shrink averages img's luma into a grid×grid square. Every picture lands on the
// same square whatever its size or shape, so two sizes of one picture land on
// nearly the same values.
//
// It samples rather than visiting every pixel. A 24-megapixel photo still averages
// thousands of samples into each cell, which is plenty, at a millisecond instead
// of tens. Transparent pixels are composited over white, the way icons and
// diagrams are seen, so two exports of one transparent PNG agree.
func shrink(img image.Image) gray {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var g gray
	if w <= 0 || h <= 0 {
		return g
	}
	luma := lumaOf(img)
	step := max(1, min(w, h)/512)

	var sum, count [grid * grid]float64
	for y := 0; y < h; y += step {
		row := (y * grid / h) * grid
		for x := 0; x < w; x += step {
			cell := row + x*grid/w
			sum[cell] += luma(b.Min.X+x, b.Min.Y+y)
			count[cell]++
		}
	}
	for i := range g {
		if count[i] > 0 {
			g[i] = float32(sum[i] / count[i])
		}
	}
	return g
}

// lumaOf returns a fast luma reader for the decoders' native image types, and a
// general one for anything else. Luma uses the BT.601 weights JPEG itself uses, so
// a JPEG's Y plane and an RGB image's computed luma agree.
func lumaOf(img image.Image) func(x, y int) float64 {
	switch m := img.(type) {
	case *image.YCbCr:
		return func(x, y int) float64 { return float64(m.Y[m.YOffset(x, y)]) }
	case *image.Gray:
		return func(x, y int) float64 { return float64(m.Pix[m.PixOffset(x, y)]) }
	case *image.RGBA: // premultiplied: over white is c + (255 - a)
		return func(x, y int) float64 {
			p := m.Pix[m.PixOffset(x, y):]
			bg := 255 - float64(p[3])
			return 0.299*(float64(p[0])+bg) + 0.587*(float64(p[1])+bg) + 0.114*(float64(p[2])+bg)
		}
	case *image.NRGBA: // straight alpha: over white is c·a + 255·(1 - a)
		return func(x, y int) float64 {
			p := m.Pix[m.PixOffset(x, y):]
			a := float64(p[3]) / 255
			bg := 255 * (1 - a)
			return 0.299*(float64(p[0])*a+bg) + 0.587*(float64(p[1])*a+bg) + 0.114*(float64(p[2])*a+bg)
		}
	default:
		return func(x, y int) float64 {
			r, g, b, a := img.At(x, y).RGBA() // premultiplied, 16-bit
			bg := float64(0xffff - a)
			return (0.299*(float64(r)+bg) + 0.587*(float64(g)+bg) + 0.114*(float64(b)+bg)) / 257
		}
	}
}

// orient turns a grid upright according to an EXIF orientation, 1 through 8. A
// camera stores a portrait photo sideways and records how to turn it; a resized
// copy has usually been turned for real. Comparing the stored pixels would call
// them different pictures. Rotating the 32×32 grid is the same as rotating the
// photo and then shrinking it, and costs nothing.
func (g gray) orient(o int) gray {
	if o < 2 || o > 8 {
		return g
	}
	const n = grid
	var d gray
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var sx, sy int
			switch o {
			case 2: // mirrored
				sx, sy = n-1-x, y
			case 3: // rotated 180°
				sx, sy = n-1-x, n-1-y
			case 4: // flipped
				sx, sy = x, n-1-y
			case 5: // transposed
				sx, sy = y, x
			case 6: // stored rotated 90° counter-clockwise; display turns it clockwise
				sx, sy = y, n-1-x
			case 7: // transversed
				sx, sy = n-1-y, n-1-x
			case 8: // stored rotated 90° clockwise; display turns it back
				sx, sy = n-1-y, x
			}
			d[y*n+x] = g[sy*n+sx]
		}
	}
	return d
}

// print is a picture's fingerprint: a perceptual hash for finding candidates fast,
// and the normalized grid for confirming them.
type print struct {
	hash uint64
	z    [grid * grid]float32 // the grid at zero mean and unit spread
	flat bool                 // too uniform to fingerprint reliably
}

// flatSpread is the luma standard deviation below which a picture is too uniform to
// compare. A blank page, a solid swatch, a white icon: their hashes are noise, and
// any two of them would "match".
const flatSpread = 4.0

func newPrint(g *gray) *print {
	var mean float64
	for _, v := range g {
		mean += float64(v)
	}
	mean /= float64(len(g))
	var variance float64
	for _, v := range g {
		d := float64(v) - mean
		variance += d * d
	}
	spread := math.Sqrt(variance / float64(len(g)))

	p := &print{hash: phash(g), flat: spread < flatSpread}
	if !p.flat {
		for i, v := range g {
			p.z[i] = float32((float64(v) - mean) / spread)
		}
	}
	return p
}

// Thresholds, from TestFingerprintSeparatesCopiesFromOtherPictures. The hash is a
// cheap filter and is loose; the grid distance decides.
const (
	maxHashDistance = 10
	maxGridDistance = 0.10
)

// similar reports whether two fingerprints are the same picture. Flat pictures are
// never similar to anything: their fingerprints carry no information.
func similar(a, b *print) bool {
	if a.flat || b.flat {
		return false
	}
	return hamming(a.hash, b.hash) <= maxHashDistance && distance(a, b) <= maxGridDistance
}

// hamming is how many bits two hashes differ in.
func hamming(a, b uint64) int { return bits.OnesCount64(a ^ b) }

// distance is the mean absolute difference between two normalized grids. Shrinking
// two sizes of one picture gives nearly the same grid, so this stays small for real
// copies — under 0.1 in practice — while two different pictures sit near 0.8.
// Normalizing first forgives a change in brightness or contrast from re-encoding.
func distance(a, b *print) float64 {
	var sum float64
	for i := range a.z {
		sum += math.Abs(float64(a.z[i] - b.z[i]))
	}
	return sum / float64(len(a.z))
}

// dctCos[u][x] is cos((2x+1)uπ / 2·grid), the DCT-II basis for the 8 lowest
// frequencies.
var dctCos = func() (t [8][grid]float64) {
	for u := 0; u < 8; u++ {
		for x := 0; x < grid; x++ {
			t[u][x] = math.Cos(float64(2*x+1) * float64(u) * math.Pi / (2 * grid))
		}
	}
	return t
}()

// phash is the perceptual hash of a grid: a 2-D DCT, keeping the 8×8 lowest
// frequencies — the broad structure of the picture, which survives resizing and
// recompression — and one bit per coefficient for whether it sits above their
// median. Only the low frequencies are computed: about ten thousand multiplies.
func phash(g *gray) uint64 {
	var rows [grid][8]float64
	for y := 0; y < grid; y++ {
		for u := 0; u < 8; u++ {
			var s float64
			for x := 0; x < grid; x++ {
				s += float64(g[y*grid+x]) * dctCos[u][x]
			}
			rows[y][u] = s
		}
	}
	var coef [64]float64
	for v := 0; v < 8; v++ {
		for u := 0; u < 8; u++ {
			var s float64
			for y := 0; y < grid; y++ {
				s += rows[y][u] * dctCos[v][y]
			}
			coef[v*8+u] = s
		}
	}
	// The median of the 63 AC terms. The DC term, the overall brightness, would
	// skew it.
	ac := make([]float64, 63)
	copy(ac, coef[1:])
	sort.Float64s(ac)
	median := ac[31]

	var h uint64
	for i, c := range coef {
		if c > median {
			h |= 1 << uint(i)
		}
	}
	return h
}
