package dedup

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

// Dims is a picture's displayed size in pixels.
type Dims struct{ W, H int }

// ImageSet is one picture found at more than one size.
type ImageSet struct {
	Keep int   // plan index of the largest copy
	Skip []int // plan indices of the smaller copies
}

// SimilarResult describes what FindSimilar turned up.
type SimilarResult struct {
	Sets []ImageSet

	// Files and Bytes count what skipping would save: the smaller copies.
	Files int
	Bytes int64

	// Dims holds the displayed size of every picture in a set, by plan index.
	Dims map[int]Dims

	// Pictures is how many plan items were pictures this could compare, and
	// Unreadable lists those it could not read or decode, which are kept.
	Pictures   int
	Unreadable []string

	// Canceled reports that Options.Cancel closed before the scan finished.
	Canceled bool
}

// Skipped lists the plan indices the result would leave behind.
func (r SimilarResult) Skipped() []int {
	var out []int
	for _, s := range r.Sets {
		out = append(out, s.Skip...)
	}
	return out
}

const (
	// minSide is the smallest picture compared. Below it a fingerprint carries too
	// little detail: two different 40-pixel icons can shrink to the same grid.
	minSide = 64

	// maxPixels is the largest picture decoded. A 150-megapixel image decodes to
	// 600 MB; such a picture is kept rather than risk exhausting memory.
	maxPixels = 150_000_000

	// ratioTol is how far two aspect ratios may differ, as a log, and still be the
	// same shape. Resizing rounds: 6000×4000 at a sixth is 1000×667, off by 0.05%.
	ratioTol = 0.015

	// thumbRatioTol is the same allowance for an embedded thumbnail against its
	// picture. A 160-pixel thumbnail rounds more coarsely.
	thumbRatioTol = 0.03
)

// FindSimilar finds pictures that appear more than once at different sizes — a
// photo and its resized export, a diagram and its thumbnail — and proposes keeping
// the largest. Pictures of the same size are never a copy of one another: frames
// from a burst share a size, and can look as alike as a resized copy does.
//
// Run it on a plan that has already been through Find, so byte-identical files are
// settled first. RAW camera files are not compared: a RAW and the JPEG beside it
// are kept together, by design. HEIC is compared only where macOS's sips can
// decode it.
//
// The work is gated, as Find's is, so that most pictures cost one header read:
//
//  1. Headers. A JPEG's header holds its size, its orientation, and usually an
//     embedded thumbnail of a few kilobytes, which is fingerprinted on the spot.
//     Other formats yield their size.
//  2. A shape gate. A picture can only have a copy at another size if some other
//     picture of the same shape has a different pixel count. A library straight
//     off one camera, with no resized copies, stops here.
//  3. Fingerprints, from the full picture, for those that passed without one.
//  4. Matching: same shape, different size, similar fingerprints.
//  5. Confirmation. A match found through an embedded thumbnail is checked against
//     the full pictures before anything is skipped, because an editor that crops a
//     photo may leave the old thumbnail behind.
func FindSimilar(items []flatten.Item, opts Options) SimilarResult {
	res := SimilarResult{Dims: map[int]Dims{}}
	report := func(p Progress) {
		if opts.OnProgress != nil {
			opts.OnProgress(p)
		}
	}

	var pics []*picture
	for i, it := range items {
		if k := kindOf(it.Src); k != notPicture {
			pics = append(pics, &picture{idx: i, kind: k})
		}
	}
	res.Pictures = len(pics)
	if len(pics) < 2 {
		return res
	}

	// Stage 1: headers.
	plain := reader{stall: opts.StallTimeout, cancel: opts.Cancel}
	var comparable []*picture
	for n, p := range pics {
		src := items[p.idx].Src
		report(Progress{Phase: ReadingHeaders, Done: n, Total: len(pics), Current: baseName(src)})
		kind := p.kind
		h, err := guarded(plain, src, func(f io.ReadSeeker) (header, error) { return readHeaderOf(f, kind) })
		if errors.Is(err, errCanceled) {
			res.Canceled = true
			return res
		}
		if err != nil {
			res.Unreadable = append(res.Unreadable, src)
			continue
		}
		p.w, p.h = h.displayed()
		p.orientation = h.orientation
		if min(p.w, p.h) < minSide || p.area() > maxPixels {
			continue
		}
		if h.thumb != nil && thumbFits(h) {
			if fp, err := fingerprintBytes(h.thumb, h.orientation); err == nil {
				p.fp, p.fromThumb = fp, true
			}
		}
		p.logRatio = math.Log(float64(p.w) / float64(p.h))
		comparable = append(comparable, p)
	}

	// Stage 2: the shape gate.
	candidates := gate(comparable)

	// Stage 3: full fingerprints for candidates without one.
	var need []*picture
	for _, p := range candidates {
		if p.fp == nil {
			need = append(need, p)
		}
	}
	if !fingerprintAll(need, items, opts, Fingerprinting, &res) {
		res.Canceled = true
		return res
	}

	// Stage 4: matching.
	edges := match(candidates)

	// Stage 5: confirm every match that rests on an embedded thumbnail.
	var thumbs []*picture
	seen := map[*picture]bool{}
	for _, e := range edges {
		for _, p := range []*picture{e.small, e.large} {
			if p.fromThumb && !seen[p] {
				seen[p] = true
				thumbs = append(thumbs, p)
			}
		}
	}
	if !fingerprintAll(thumbs, items, opts, Confirming, &res) {
		res.Canceled = true
		return res
	}
	confirmed := edges[:0]
	for _, e := range edges {
		if e.small.fp != nil && e.large.fp != nil && similar(e.small.fp, e.large.fp) {
			confirmed = append(confirmed, e)
		}
	}

	// Stage 6: sets.
	res.Sets = buildSets(items, confirmed)
	byIdx := make(map[int]*picture, len(pics))
	for _, p := range pics {
		byIdx[p.idx] = p
	}
	for _, s := range res.Sets {
		for _, i := range append([]int{s.Keep}, s.Skip...) {
			res.Dims[i] = Dims{byIdx[i].w, byIdx[i].h}
		}
		for _, i := range s.Skip {
			res.Files++
			if info, err := os.Stat(items[i].Src); err == nil {
				res.Bytes += info.Size()
			}
		}
	}
	sort.Strings(res.Unreadable)
	return res
}

// kind is how a picture is read.
type kind int

const (
	notPicture kind = iota
	jpegPicture
	otherPicture // PNG, GIF, BMP, TIFF, WebP
	heicPicture
)

// kindOf classifies a path by extension. RAW formats are absent on purpose: a RAW
// never competes with the JPEG made from it.
func kindOf(path string) kind {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "jpg", "jpeg", "jpe", "jfif":
		return jpegPicture
	case "png", "gif", "bmp", "tif", "tiff", "webp":
		return otherPicture
	case "heic", "heif":
		if sipsPath != "" {
			return heicPicture
		}
	}
	return notPicture
}

func readHeaderOf(r io.ReadSeeker, k kind) (header, error) {
	switch k {
	case jpegPicture:
		return readJPEGHeader(r)
	case heicPicture:
		return readHEICHeader(r)
	default:
		return readOtherHeader(r)
	}
}

// picture is one image in the plan, as the scan learns about it.
type picture struct {
	idx         int // plan index
	kind        kind
	w, h        int // displayed size
	orientation int
	logRatio    float64

	fp        *print
	fromThumb bool // fp came from the embedded thumbnail, and is unconfirmed
}

func (p *picture) area() int64 { return int64(p.w) * int64(p.h) }

// thumbFits reports whether an embedded thumbnail shows the whole picture: its
// shape must match the picture's. Some cameras store a 4:3 thumbnail for a 3:2
// photo, padded with black bars, and fingerprinting that would describe a
// different picture. Thumbnails are stored the same way up as their picture, so
// the stored size is the one compared.
func thumbFits(h header) bool {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(h.thumb))
	if err != nil || cfg.Width < 32 || cfg.Height < 32 || h.w == 0 || h.h == 0 {
		return false
	}
	t := math.Log(float64(cfg.Width) / float64(cfg.Height))
	return math.Abs(t-math.Log(float64(h.w)/float64(h.h))) <= thumbRatioTol
}

// fingerprintBytes decodes a whole image file held in memory and fingerprints it,
// turned upright by an EXIF orientation.
func fingerprintBytes(data []byte, orientation int) (*print, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	g := shrink(img).orient(orientation)
	return newPrint(&g), nil
}

// gate keeps the pictures that could have a copy at another size: those whose shape
// is shared by a picture with a different pixel count. Shapes are bucketed by log
// aspect ratio, ratioTol wide, so any two pictures within tolerance sit in the same
// or neighboring buckets.
func gate(pics []*picture) []*picture {
	key := func(p *picture) int { return int(math.Floor(p.logRatio / ratioTol)) }
	areas := map[int]map[int64]bool{}
	for _, p := range pics {
		k := key(p)
		if areas[k] == nil {
			areas[k] = map[int64]bool{}
		}
		areas[k][p.area()] = true
	}
	varied := map[int]bool{} // bucket -> its neighborhood holds two or more sizes
	for k := range areas {
		var first int64 = -1
	scan:
		for d := -1; d <= 1; d++ {
			for a := range areas[k+d] {
				if first == -1 {
					first = a
				} else if a != first {
					varied[k] = true
					break scan
				}
			}
		}
	}
	var out []*picture
	for _, p := range pics {
		if varied[key(p)] {
			out = append(out, p)
		}
	}
	return out
}

// edge is a smaller picture that matched a larger one.
type edge struct{ small, large *picture }

// match pairs pictures of the same shape and different sizes whose fingerprints
// agree. Sorted by shape, each picture is compared only with its near neighbors.
func match(pics []*picture) []edge {
	var sorted []*picture
	for _, p := range pics {
		if p.fp != nil && !p.fp.flat {
			sorted = append(sorted, p)
		}
	}
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].logRatio < sorted[b].logRatio })

	var edges []edge
	for i, a := range sorted {
		for _, b := range sorted[i+1:] {
			if b.logRatio-a.logRatio > ratioTol {
				break
			}
			if a.area() == b.area() || !similar(a.fp, b.fp) {
				continue // the same size is never a copy; see FindSimilar
			}
			if a.area() < b.area() {
				edges = append(edges, edge{a, b})
			} else {
				edges = append(edges, edge{b, a})
			}
		}
	}
	return edges
}

// buildSets groups confirmed matches under the largest copy of each picture. Each
// smaller picture points at the largest picture it matched, ties going to the name
// that does not read as a copy; following the pointers ends at the set's keeper,
// since every step is to a strictly larger picture.
func buildSets(items []flatten.Item, edges []edge) []ImageSet {
	best := map[*picture]*picture{}
	for _, e := range edges {
		cur := best[e.small]
		switch {
		case cur == nil || e.large.area() > cur.area():
			best[e.small] = e.large
		case e.large.area() == cur.area() && pickKeeper(items, []int{cur.idx, e.large.idx}) == e.large.idx:
			best[e.small] = e.large
		}
	}
	groups := map[*picture][]int{}
	for small := range best {
		keep := small
		for best[keep] != nil {
			keep = best[keep]
		}
		groups[keep] = append(groups[keep], small.idx)
	}
	sets := make([]ImageSet, 0, len(groups))
	for keep, skip := range groups {
		sort.Ints(skip)
		sets = append(sets, ImageSet{Keep: keep.idx, Skip: skip})
	}
	sort.Slice(sets, func(a, b int) bool { return sets[a].Keep < sets[b].Keep })
	return sets
}

// sipsTimeout bounds one HEIC rendering when the scan has no stall limit of its own.
const sipsTimeout = time.Minute

// fingerprintAll fingerprints pictures from their full pixels. One goroutine — this
// one — reads the files in plan order, since sequential reads suit a spinning disk
// and a USB bridge; decoders work in parallel on the bytes it hands over. The jobs
// channel holds as many files as there are decoders, which bounds the memory in
// flight. Progress is reported from this goroutine only.
//
// It returns false if the scan was stopped.
func fingerprintAll(pics []*picture, items []flatten.Item, opts Options, phase Phase, res *SimilarResult) bool {
	if len(pics) == 0 {
		return true
	}
	report := func(p Progress) {
		if opts.OnProgress != nil {
			opts.OnProgress(p)
		}
	}
	limit := sipsTimeout
	if opts.StallTimeout > 0 {
		limit = opts.StallTimeout
	}
	var tmp string
	for _, p := range pics {
		if p.kind == heicPicture {
			var err error
			if tmp, err = os.MkdirTemp("", "superdirectory-"); err == nil {
				defer os.RemoveAll(tmp)
			}
			break
		}
	}

	type job struct {
		p    *picture
		data []byte
	}
	type result struct {
		p   *picture
		fp  *print
		err error
	}
	workers := min(4, runtime.NumCPU())
	jobs := make(chan job, workers)
	results := make(chan result, len(pics))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				var fp *print
				var err error
				if j.p.kind == heicPicture {
					if tmp == "" {
						err = errors.New("no temporary folder for sips")
					} else {
						fp, err = heicPrint(items[j.p.idx].Src, tmp, j.p.idx, opts.Cancel, limit)
					}
				} else {
					fp, err = fingerprintBytes(j.data, j.p.orientation)
				}
				results <- result{j.p, fp, err}
			}
		}()
	}

	canceled := false
	for n, p := range pics {
		src := items[p.idx].Src
		name := baseName(src)
		var size int64
		if info, err := os.Stat(src); err == nil {
			size = info.Size()
		}
		report(Progress{Phase: phase, Done: n, Total: len(pics), Current: name, Size: size})
		if p.kind == heicPicture {
			if closed(opts.Cancel) {
				canceled = true
				break
			}
			jobs <- job{p: p}
			continue
		}
		r := reader{stall: opts.StallTimeout, cancel: opts.Cancel, onRead: func(read int64) {
			report(Progress{Phase: phase, Done: n, Total: len(pics), Current: name, Read: read, Size: size})
		}}
		data, err := guarded(r, src, func(f io.ReadSeeker) ([]byte, error) { return io.ReadAll(f) })
		if errors.Is(err, errCanceled) {
			canceled = true
			break
		}
		if err != nil {
			results <- result{p: p, err: err}
			continue
		}
		jobs <- job{p: p, data: data}
	}
	close(jobs)
	wg.Wait()
	close(results)

	for r := range results {
		switch {
		case errors.Is(r.err, errCanceled):
			canceled = true
		case r.err != nil:
			// Unreadable or undecodable: it cannot be confirmed, so it is kept.
			res.Unreadable = append(res.Unreadable, items[r.p.idx].Src)
			r.p.fp = nil
		default:
			r.p.fp, r.p.fromThumb = r.fp, false
		}
	}
	if !canceled {
		report(Progress{Phase: phase, Done: len(pics), Total: len(pics)})
	}
	return !canceled
}
