//go:build !windows

package dedup

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/guard"
)

// TestHashStallIsAbandoned. The drive that motivated the duplicate finder also had an
// unreadable file on it. A scan that hangs is no better than a copy that hangs.
//
// A FIFO is the only portable way to make a read block on demand. It is not a perfect
// stand-in for a regular file on a failing disk — a FIFO is pollable, so closing it
// unblocks the reader — but it exercises everything the reader controls: noticing the
// stall, closing the descriptor, and returning rather than waiting forever.
func TestHashStallIsAbandoned(t *testing.T) {
	defer swapPollInterval(t, 5*time.Millisecond)()

	dir := t.TempDir()
	fifo := filepath.Join(dir, "stuck.bin")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	// The reader's goroutine parks in os.Open until a writer arrives. Release it, or it
	// holds the FIFO for the life of the test binary. Opening the write end succeeds
	// immediately because a reader is already waiting — which is the whole problem.
	defer func() {
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
	}()

	start := time.Now()
	_, err := hashFile(guard.Reader{Stall: 60 * time.Millisecond}, fifo, -1)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("hash took %v to give up", elapsed)
	}
	var se *guard.StallError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a *guard.StallError", err)
	}
}

// TestFindTreatsAnUnreadableFileAsUnique: one bad file must not stop the scan, must not
// be paired with anything, and must survive into the plan.
//
// The file here is unreadable by permission rather than by hardware, which reaches the
// same code path: the read fails, the file is recorded, the scan carries on. A FIFO
// would not do — it stats as zero bytes and never becomes a candidate.
func TestFindTreatsAnUnreadableFileAsUnique(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	dir := t.TempDir()
	photo := make([]byte, 4096)
	a := write(t, dir, "a.bin", photo)
	b := write(t, dir, "b.bin", photo)
	bad := write(t, dir, "bad.bin", photo) // same size, so it becomes a candidate

	if err := os.Chmod(bad, 0o000); err != nil {
		t.Skipf("cannot make a file unreadable: %v", err)
	}
	defer os.Chmod(bad, 0o644)

	items := plan(a, b, bad)
	res := Find(items, Options{})

	if res.Files != 1 {
		t.Errorf("expected a and b to pair up, got %d skippable files", res.Files)
	}
	if len(res.Unreadable) != 1 || filepath.Base(res.Unreadable[0]) != "bad.bin" {
		t.Errorf("Unreadable = %v, want just bad.bin", res.Unreadable)
	}
	for _, s := range res.Sets {
		for _, i := range append([]int{s.Keep}, s.Skip...) {
			if items[i].Src == bad {
				t.Error("an unreadable file was placed in a duplicate set")
			}
		}
	}
	// It survives the filter: never skip a file we could not read.
	for _, it := range Filter(items, res) {
		if it.Src == bad {
			return
		}
	}
	t.Error("the unreadable file was dropped from the plan")
}

func swapPollInterval(t *testing.T, d time.Duration) func() {
	t.Helper()
	old := guard.PollInterval
	guard.PollInterval = d
	return func() { guard.PollInterval = old }
}

// TestHashCancelIsAbandoned: Ctrl+C during a read that will never finish must return
// at once rather than wait for a byte that is not coming.
func TestHashCancelIsAbandoned(t *testing.T) {
	defer swapPollInterval(t, 5*time.Millisecond)()

	dir := t.TempDir()
	fifo := filepath.Join(dir, "stuck.bin")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	defer func() {
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
	}()

	cancel := make(chan struct{})
	time.AfterFunc(30*time.Millisecond, func() { close(cancel) })

	start := time.Now()
	_, err := hashFile(guard.Reader{Cancel: cancel}, fifo, -1) // stall guard off: cancel alone must do it
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("hash took %v to stop", elapsed)
	}
	if !errors.Is(err, guard.ErrCanceled) {
		t.Fatalf("err = %v, want guard.ErrCanceled", err)
	}
}

// TestLongReadReportsBytes: a multi-gigabyte video takes minutes to read on a slow
// drive. Its progress must move through the read, not sit frozen on its name.
func TestLongReadReportsBytes(t *testing.T) {
	defer swapPollInterval(t, 2*time.Millisecond)()

	dir := t.TempDir()
	fifo := filepath.Join(dir, "long.mov")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer w.Close()
		for i := 0; i < 10; i++ {
			w.Write(make([]byte, 1024))
			time.Sleep(4 * time.Millisecond)
		}
	}()

	var seen []int64
	h := guard.Reader{Stall: time.Second, OnRead: func(n int64) { seen = append(seen, n) }}
	if _, err := hashFile(h, fifo, -1); err != nil {
		t.Fatal(err)
	}
	moving := false
	for i := 1; i < len(seen); i++ {
		if seen[i] > seen[i-1] && seen[i-1] > 0 {
			moving = true
		}
	}
	if !moving {
		t.Errorf("byte reports never advanced mid-read: %v", seen)
	}
}
