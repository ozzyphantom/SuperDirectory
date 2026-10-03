// Package guard reads files that may never finish reading. A file on a failing
// drive can hold a read in the kernel's retry loop for minutes; no syscall
// unblocks it. So the read runs on its own goroutine, and the caller walks away
// when the file stops delivering bytes or the user stops the run.
package guard

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/exif"
)

// PollInterval is how often a read in flight is checked. A var so tests need not
// wait.
var PollInterval = 100 * time.Millisecond

// StallError reports a file abandoned for delivering no data.
type StallError struct{ After time.Duration }

func (e *StallError) Error() string {
	return fmt.Sprintf("no data for %s while reading", e.After)
}

// ErrCanceled is what a read returns when its Cancel channel closes.
var ErrCanceled = errors.New("stopped")

// Reader describes how a read is guarded. The zero value reads plainly.
type Reader struct {
	Stall  time.Duration    // abandon after this long without a byte; zero never
	Cancel <-chan struct{}  // abandon when closed; nil never
	OnRead func(read int64) // bytes read so far, about every PollInterval
}

// Read opens path and runs fn on it, handing fn a file that counts the bytes it
// pulls. fn's result is returned unless the read is walked away from first.
//
// When the reader has a stall limit, a cancel channel, or a progress callback, fn
// runs on its own goroutine and is abandoned if it stops delivering bytes or the
// scan is stopped — the same treatment, and the same unavoidable goroutine leak,
// as the copier. A read parked in a disk retry cannot be cancelled; it can only
// be walked away from. fn must therefore hand everything back through its return
// values and touch no shared state: an abandoned fn may still finish later.
func Read[T any](r Reader, path string, fn func(f exif.File) (T, error)) (T, error) {
	var zero T
	if closed(r.Cancel) {
		return zero, ErrCanceled // stopped before this file began; do not open it
	}
	if r.Stall <= 0 && r.Cancel == nil && r.OnRead == nil {
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

	t := time.NewTicker(PollInterval)
	defer t.Stop()
	last, lastMoved := int64(0), time.Now()
	for {
		select {
		case o := <-done:
			return o.val, o.err
		case <-r.Cancel:
			abandon()
			return zero, ErrCanceled
		case now := <-t.C:
			n := read.Load()
			if n != last {
				last, lastMoved = n, now
			}
			if r.OnRead != nil {
				r.OnRead(n)
			}
			if idle := now.Sub(lastMoved); r.Stall > 0 && idle >= r.Stall {
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
