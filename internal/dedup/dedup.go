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

	// OnProgress reports hashing progress. Only candidate files — those sharing a
	// size with another — are ever hashed, so Total is usually far below the number
	// of files in the plan.
	OnProgress func(Progress)
}

// Progress reports how far the hashing has got.
type Progress struct {
	Done    int    // candidate files hashed
	Total   int    // candidate files to hash
	Current string // base name of the file being read
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
}

// Find groups the plan's items by content. It never modifies the plan; pass the
// result to Filter to apply it.
func Find(items []flatten.Item, opts Options) Result {
	var res Result

	// Stage 1: size. A stat per file, and nothing is opened.
	bySize := map[int64][]int{}
	for i, it := range items {
		info, err := os.Stat(it.Src)
		if err != nil || info.Size() == 0 {
			// Unstattable files are left alone. Empty files are all "identical" to
			// each other, which is true and useless; skipping them avoids proposing
			// to delete a directory full of zero-byte placeholders.
			continue
		}
		bySize[info.Size()] = append(bySize[info.Size()], i)
	}

	// Only sizes shared by more than one file are worth reading.
	var candidates [][]int
	total := 0
	for _, group := range bySize {
		if len(group) > 1 {
			candidates = append(candidates, group)
			total += len(group)
		}
	}
	// Deterministic order, so progress and results do not depend on map iteration.
	sort.Slice(candidates, func(a, b int) bool { return candidates[a][0] < candidates[b][0] })

	done := 0
	hash := func(idx int, limit int64) (string, bool) {
		if opts.OnProgress != nil {
			opts.OnProgress(Progress{Done: done, Total: total, Current: baseName(items[idx].Src)})
		}
		sum, err := hashFile(items[idx].Src, limit, opts.StallTimeout)
		if err != nil {
			res.Unreadable = append(res.Unreadable, items[idx].Src)
			return "", false
		}
		res.Hashed++
		return sum, true
	}

	for _, group := range candidates {
		size := sizeOf(items, group)

		// Stage 2: the leading block. For files at or below that size this is already
		// the whole file, so stage 3 has nothing left to do.
		limit := int64(partialHashBytes)
		partialIsWhole := size <= partialHashBytes

		byPartial := map[string][]int{}
		for _, idx := range group {
			sum, ok := hash(idx, limit)
			done++
			if ok {
				byPartial[sum] = append(byPartial[sum], idx)
			}
		}

		for _, sameHead := range byPartial {
			if len(sameHead) < 2 {
				continue
			}
			if partialIsWhole {
				res.add(sameHead, size)
				continue
			}
			// Stage 3: the whole file, only for the few that still match.
			byFull := map[string][]int{}
			for _, idx := range sameHead {
				sum, ok := hashFile2(items[idx].Src, opts.StallTimeout)
				if !ok {
					res.Unreadable = append(res.Unreadable, items[idx].Src)
					continue
				}
				res.Hashed++
				byFull[sum] = append(byFull[sum], idx)
			}
			for _, identical := range byFull {
				res.add(identical, size)
			}
		}
	}

	if opts.OnProgress != nil && total > 0 {
		opts.OnProgress(Progress{Done: total, Total: total})
	}
	sort.Slice(res.Sets, func(a, b int) bool { return res.Sets[a].Keep < res.Sets[b].Keep })
	return res
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

func sizeOf(items []flatten.Item, group []int) int64 {
	info, err := os.Stat(items[group[0]].Src)
	if err != nil {
		return 0
	}
	return info.Size()
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

func hashFile2(path string, stall time.Duration) (string, bool) {
	sum, err := hashFile(path, -1, stall)
	return sum, err == nil
}

// StallError reports a file abandoned during hashing.
type StallError struct{ After time.Duration }

func (e *StallError) Error() string {
	return fmt.Sprintf("no data for %s while hashing", e.After)
}

// hashFile returns the SHA-256 of the file, or of its first limit bytes when limit
// is positive.
//
// When stall is positive the read runs on its own goroutine and is abandoned if it
// stops delivering bytes — the same treatment, and the same unavoidable goroutine
// leak, as flatten.Copy. A read parked in a disk retry cannot be cancelled; it can
// only be walked away from.
func hashFile(path string, limit int64, stall time.Duration) (string, error) {
	if stall <= 0 {
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

	t := time.NewTicker(pollInterval)
	defer t.Stop()
	last, lastMoved := int64(0), time.Now()
	for {
		select {
		case o := <-done:
			return o.sum, o.err
		case now := <-t.C:
			if n := read.Load(); n != last {
				last, lastMoved = n, now
			}
			if idle := now.Sub(lastMoved); idle >= stall {
				aborted.Store(true)
				mu.Lock()
				f := open
				mu.Unlock()
				if f != nil {
					f.Close()
				}
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
	if limit > 0 {
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
