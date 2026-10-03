// Package dedup finds files in a copy plan whose contents are byte-for-byte
// identical, so the user can copy one of each instead of all of them.
//
// Identity is decided by content, never by name: "DSC_0042.NEF" and
// "DSC_0042 copy.NEF" are the same photograph, and two different photographs may
// share a name across folders. Content means reading the file, which on an external
// drive is the expensive thing, so the work is gated in three stages:
//
//  1. Size. Files of different sizes cannot be identical. Most photographs have a
//     unique byte size, so most files are never opened at all — one stat each.
//  2. A partial hash of the first 64 KiB, for files that share a size. Two distinct
//     photographs of the same size almost always differ in their first block.
//  3. A full hash, only for files whose leading block also matched.
//
// On a folder of eleven thousand photographs this typically reads a few hundred.
package dedup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

// partialHashBytes is how much of a file the second stage reads. One filesystem
// block is plenty to separate two different photographs of the same size.
const partialHashBytes = 64 << 10

// pollInterval is how often a hash in flight is checked for a stall. A var so tests
// need not wait.
var pollInterval = 100 * time.Millisecond

// Options configure Find.
type Options struct {
	// StallTimeout abandons a file that yields no bytes for this long and treats it
	// as unique rather than hanging the scan. The drive that motivated the duplicate
	// finder also had an unreadable file on it; a scan that hangs is no better than a
	// copy that hangs. Zero disables the guard.
	StallTimeout time.Duration

	// OnProgress reports the scan as it goes: once per batch of files while sizes
	// are checked, then before each read and periodically through a long one. Only
	// candidate files — those sharing a size with another — are ever read, so the
	// hashing Total is usually far below the number of files in the plan.
	OnProgress func(Progress)

	// Cancel, once closed, stops the scan: the read in flight is abandoned and Find
	// returns at once with Result.Canceled set. A nil channel never cancels.
	Cancel <-chan struct{}
}

// Phase is the stage of the scan a Progress report belongs to.
type Phase int

const (
	// Sizing stats every file. Nothing is opened.
	Sizing Phase = iota
	// Hashing reads the files that share a size with another.
	Hashing
)

// Progress reports how far the scan has got.
//
// While hashing, Total grows as the scan runs: a pair whose leading blocks match
// needs a full read of each, and that is only known once the leading blocks are
// in. Read and Size cover the file in flight, so a long read of a large video is
// visibly moving rather than frozen on its name.
type Progress struct {
	Phase   Phase
	Done    int    // files sized, or reads finished
	Total   int    // files to size, or reads known to be needed
	Current string // base name of the file being read
	Read    int64  // bytes of Current read so far
	Size    int64  // bytes of Current this read will cover
}

// Set is a group of plan items with identical contents.
type Set struct {
	Size int64
	Keep int   // index into the plan of the copy to make
	Skip []int // indices of the identical files to leave behind
}

// Result describes what Find turned up.
type Result struct {
	Sets []Set

	// Files and Bytes count what skipping would save: the copies, not the originals.
	Files int
	Bytes int64

	// Hashed is how many files were actually read, and Unreadable lists files that
	// could not be, which are treated as unique.
	Hashed     int
	Unreadable []string

	// Canceled reports that Options.Cancel closed before the scan finished. The
	// sets found so far are incomplete and should not be applied.
	Canceled bool
}

// sizingBatch is how many stats pass between progress reports and stop checks.
const sizingBatch = 256

// Find groups the plan's items by content. It never modifies the plan; pass the
// result to Filter to apply it.
func Find(items []flatten.Item, opts Options) Result {
	var res Result
	report := func(p Progress) {
		if opts.OnProgress != nil {
			opts.OnProgress(p)
		}
	}

	// Stage 1: size. A stat per file, and nothing is opened. On an external drive a
	// stat is a bus round trip, so this alone can take seconds and reports as it goes.
	bySize := map[int64][]int{}
	for i, it := range items {
		if i%sizingBatch == 0 {
			if closed(opts.Cancel) {
				res.Canceled = true
				return res
			}
			report(Progress{Phase: Sizing, Done: i, Total: len(items)})
		}
		info, err := os.Stat(it.Src)
		if err != nil || info.Size() == 0 {
			// Unstattable files are left alone. Empty files are all "identical" to
			// each other, which is true and useless; skipping them avoids proposing
			// to delete a directory full of zero-byte placeholders.
			continue
		}
		bySize[info.Size()] = append(bySize[info.Size()], i)
	}

	// Only sizes shared by more than one file are worth reading. The size travels
	// with its group: it is already known, and asking the drive again costs a stat.
	type sizeGroup struct {
		size  int64
		files []int
	}
	var candidates []sizeGroup
	total := 0
	for size, files := range bySize {
		if len(files) > 1 {
			candidates = append(candidates, sizeGroup{size, files})
			total += len(files)
		}
	}
	// Deterministic order, so progress and results do not depend on map iteration.
	sort.Slice(candidates, func(a, b int) bool { return candidates[a].files[0] < candidates[b].files[0] })

	done := 0
	// read hashes one file — its first limit bytes, or all of it when limit is
	// negative — reporting as it goes. It returns false for a file that could not
	// be read, which is recorded and treated as unique, and for a stopped scan,
	// which sets res.Canceled.
	read := func(idx int, limit, size int64) (string, bool) {
		name := baseName(items[idx].Src)
		span := size
		if limit >= 0 && limit < size {
			span = limit
		}
		report(Progress{Phase: Hashing, Done: done, Total: total, Current: name, Size: span})
		h := hasher{stall: opts.StallTimeout, cancel: opts.Cancel, onRead: func(n int64) {
			report(Progress{Phase: Hashing, Done: done, Total: total, Current: name, Read: n, Size: span})
		}}
		sum, err := h.hash(items[idx].Src, limit)
		done++
		switch {
		case errors.Is(err, errCanceled):
			res.Canceled = true
			return "", false
		case err != nil:
			res.Unreadable = append(res.Unreadable, items[idx].Src)
			return "", false
		}
		res.Hashed++
		return sum, true
	}

	for _, g := range candidates {
		// Stage 2: the leading block. For files at or below that size this is already
		// the whole file, so stage 3 has nothing left to do.
		partialIsWhole := g.size <= partialHashBytes

		byPartial := map[string][]int{}
		for _, idx := range g.files {
			sum, ok := read(idx, partialHashBytes, g.size)
			if res.Canceled {
				return res
			}
			if ok {
				byPartial[sum] = append(byPartial[sum], idx)
			}
		}

		for _, sameHead := range byPartial {
			if len(sameHead) < 2 {
				continue
			}
			if partialIsWhole {
				res.add(sameHead, g.size)
				continue
			}
			// Stage 3: the whole file, only for the few that still match.
			total += len(sameHead)
			byFull := map[string][]int{}
			for _, idx := range sameHead {
				sum, ok := read(idx, -1, g.size)
				if res.Canceled {
					return res
				}
				if ok {
					byFull[sum] = append(byFull[sum], idx)
				}
			}
			for _, identical := range byFull {
				res.add(identical, g.size)
			}
		}
	}

	if total > 0 {
		report(Progress{Phase: Hashing, Done: total, Total: total})
	}
	sort.Slice(res.Sets, func(a, b int) bool { return res.Sets[a].Keep < res.Sets[b].Keep })
	return res
}

// closed reports whether c has been closed, without blocking. A nil channel is
// never closed.
func closed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// add records a group of identical files, keeping the earliest in plan order. That
// matters: the plan gave the first occurrence the unsuffixed name, so keeping it
// leaves "beach.jpg" behind rather than "beach_1.jpg".
func (r *Result) add(identical []int, size int64) {
	if len(identical) < 2 {
		return
	}
	sort.Ints(identical)
	r.Sets = append(r.Sets, Set{Size: size, Keep: identical[0], Skip: identical[1:]})
	r.Files += len(identical) - 1
	r.Bytes += size * int64(len(identical)-1)
}

// Filter returns the plan with every duplicate removed, preserving order.
func Filter(items []flatten.Item, res Result) []flatten.Item {
	if res.Files == 0 {
		return items
	}
	skip := make(map[int]bool, res.Files)
	for _, s := range res.Sets {
		for _, i := range s.Skip {
			skip[i] = true
		}
	}
	out := make([]flatten.Item, 0, len(items)-res.Files)
	for i, it := range items {
		if !skip[i] {
			out = append(out, it)
		}
	}
	return out
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

// StallError reports a file abandoned during hashing.
type StallError struct{ After time.Duration }

func (e *StallError) Error() string {
	return fmt.Sprintf("no data for %s while hashing", e.After)
}

// errCanceled is what a read returns when Options.Cancel closes.
var errCanceled = errors.New("scan canceled")

// hasher reads files for Find. A read is abandonable — when it stalls, or when the
// scan is stopped — and reports its progress while it goes.
type hasher struct {
	stall  time.Duration
	cancel <-chan struct{}
	onRead func(read int64) // bytes read so far, about every pollInterval
}

// hash returns the SHA-256 of the file, or of its first limit bytes when limit is
// not negative.
//
// When stall is positive or cancel is set, the read runs on its own goroutine and is
// abandoned if it stops delivering bytes or the scan is stopped — the same
// treatment, and the same unavoidable goroutine leak, as flatten.Copy. A read parked
// in a disk retry cannot be cancelled; it can only be walked away from.
func (h hasher) hash(path string, limit int64) (string, error) {
	if closed(h.cancel) {
		return "", errCanceled // stopped before this file began; do not open it
	}
	if h.stall <= 0 && h.cancel == nil {
		return hashDirect(path, limit)
	}

	type outcome struct {
		sum string
		err error
	}
	var (
		read    atomic.Int64
		aborted atomic.Bool
		mu      sync.Mutex
		open    *os.File
	)
	done := make(chan outcome, 1)

	go func() {
		f, err := os.Open(path)
		if err != nil {
			done <- outcome{"", err}
			return
		}
		mu.Lock()
		if aborted.Load() {
			mu.Unlock()
			f.Close()
			done <- outcome{"", &StallError{}}
			return
		}
		open = f
		mu.Unlock()
		defer f.Close()

		sum, err := hashReader(f, limit, func(n int64) { read.Add(n) })
		done <- outcome{sum, err}
	}()

	abandon := func() {
		aborted.Store(true)
		mu.Lock()
		f := open
		mu.Unlock()
		if f != nil {
			f.Close()
		}
	}

	t := time.NewTicker(pollInterval)
	defer t.Stop()
	last, lastMoved := int64(0), time.Now()
	for {
		select {
		case o := <-done:
			return o.sum, o.err
		case <-h.cancel:
			abandon()
			return "", errCanceled
		case now := <-t.C:
			n := read.Load()
			if n != last {
				last, lastMoved = n, now
			}
			if h.onRead != nil {
				h.onRead(n)
			}
			if idle := now.Sub(lastMoved); h.stall > 0 && idle >= h.stall {
				abandon()
				return "", &StallError{After: idle}
			}
		}
	}
}

func hashDirect(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return hashReader(f, limit, nil)
}

func hashReader(f io.Reader, limit int64, onRead func(int64)) (string, error) {
	var r io.Reader = f
	if limit >= 0 {
		r = io.LimitReader(f, limit)
	}
	h := sha256.New()
	buf := make([]byte, 256<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if onRead != nil {
				onRead(int64(n))
			}
		}
		if err == io.EOF {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}
