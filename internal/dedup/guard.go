package dedup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/exif"
)

// StallError reports a file abandoned for delivering no data.
type StallError struct{ After time.Duration }

func (e *StallError) Error() string {
	return fmt.Sprintf("no data for %s while reading", e.After)
}

// errCanceled is what a read returns when Options.Cancel closes.
var errCanceled = errors.New("scan canceled")

// reader reads files for the scans. A read is abandonable — when it stalls, or
// when the scan is stopped — and reports its progress while it goes.
type reader struct {
	stall  time.Duration
	cancel <-chan struct{}
	onRead func(read int64) // bytes read so far, about every pollInterval
}

// guarded opens path and runs fn on it, handing fn a reader that counts the bytes
// it pulls. fn's result is returned unless the read is walked away from first.
//
// When the reader has a stall limit, a cancel channel, or a progress callback, fn
// runs on its own goroutine and is abandoned if it stops delivering bytes or the
// scan is stopped — the same treatment, and the same unavoidable goroutine leak,
// as flatten.Copy. A read parked in a disk retry cannot be cancelled; it can only
// be walked away from. fn must therefore hand everything back through its return
// values and touch no shared state: an abandoned fn may still finish later.
func guarded[T any](r reader, path string, fn func(f exif.File) (T, error)) (T, error) {
	var zero T
	if closed(r.cancel) {
		return zero, errCanceled // stopped before this file began; do not open it
	}
	if r.stall <= 0 && r.cancel == nil && r.onRead == nil {
		f, err := os.Open(path)
		if err != nil {
			return zero, err
		}
		defer f.Close()
		return fn(f)
	}

	type outcome struct {
		val T
		err error
	}
	var (
		read    atomic.Int64
		aborted atomic.Bool
		mu      sync.Mutex
		open    *os.File
	)
	done := make(chan outcome, 1) // buffered: an abandoned fn must not block forever

	go func() {
		f, err := os.Open(path)
		if err != nil {
			done <- outcome{zero, err}
			return
		}
		mu.Lock()
		if aborted.Load() {
			mu.Unlock()
			f.Close()
			done <- outcome{zero, &StallError{}}
			return
		}
		open = f
		mu.Unlock()
		defer f.Close()

		v, err := fn(&countingFile{f: f, n: &read})
		done <- outcome{v, err}
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
			return o.val, o.err
		case <-r.cancel:
			abandon()
			return zero, errCanceled
		case now := <-t.C:
			n := read.Load()
			if n != last {
				last, lastMoved = n, now
			}
			if r.onRead != nil {
				r.onRead(n)
			}
			if idle := now.Sub(lastMoved); r.stall > 0 && idle >= r.stall {
				abandon()
				return zero, &StallError{After: idle}
			}
		}
	}
}

// countingFile counts the bytes read through it, for stall detection and progress.
// Seeks move no bytes and count nothing.
type countingFile struct {
	f *os.File
	n *atomic.Int64
}

func (c *countingFile) Read(p []byte) (int, error) {
	n, err := c.f.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *countingFile) Seek(offset int64, whence int) (int64, error) {
	return c.f.Seek(offset, whence)
}

func (c *countingFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.f.ReadAt(p, off)
	c.n.Add(int64(n))
	return n, err
}

// hash returns the SHA-256 of the file, or of its first limit bytes when limit is
// not negative.
func (r reader) hash(path string, limit int64) (string, error) {
	return guarded(r, path, func(f exif.File) (string, error) {
		var src io.Reader = f
		if limit >= 0 {
			src = io.LimitReader(f, limit)
		}
		h := sha256.New()
		if _, err := io.CopyBuffer(h, src, make([]byte, 256<<10)); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	})
}
