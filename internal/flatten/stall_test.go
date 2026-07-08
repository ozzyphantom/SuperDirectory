//go:build !windows

package flatten

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO is the only portable way to make a read block indefinitely on demand. It
// is not a perfect stand-in for a regular file on a failing disk — a FIFO is
// pollable, so closing it unblocks the reader, whereas a read stuck in a disk retry
// unblocks for nobody. What the FIFO does exercise faithfully is everything Copy
// controls: noticing the stall, abandoning the job, discarding the partial
// destination, recording a Failure, and moving on.

func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
}

// TestStallTimeoutAbandonsAFileThatNeverOpens models the worst case: the source
// blocks in open, before a single byte or even a file descriptor exists.
func TestStallTimeoutAbandonsAFileThatNeverOpens(t *testing.T) {
	defer swapPollInterval(t, 5*time.Millisecond)()

	dir := t.TempDir()
	fifo := filepath.Join(dir, "stuck.bin")
	mkfifo(t, fifo)

	// Release the parked goroutine when the test ends, or it holds the FIFO open
	// for the life of the test binary.
	defer func() {
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
	}()

	target := t.TempDir()
	items := []Item{{Src: fifo, Dst: "out.bin"}}

	start := time.Now()
	failures := Copy(target, items, Options{StallTimeout: 60 * time.Millisecond})
	elapsed := time.Since(start)

	if len(failures) != 1 {
		t.Fatalf("expected the stuck file to be recorded as a failure, got %d", len(failures))
	}
	var se *StallError
	if !errors.As(failures[0].Err, &se) {
		t.Fatalf("failure is %v, want a *StallError", failures[0].Err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Copy took %v to give up; the whole point is that it does not hang", elapsed)
	}
	if _, err := os.Stat(filepath.Join(target, "out.bin")); !os.IsNotExist(err) {
		t.Error("a destination was left behind for a file that never opened")
	}
}

// TestStallTimeoutDiscardsThePartialDestination models a file that starts fine and
// then stops delivering data. The bytes already written must not be left on disk as
// a silently truncated file.
func TestStallTimeoutDiscardsThePartialDestination(t *testing.T) {
	defer swapPollInterval(t, 5*time.Millisecond)()

	dir := t.TempDir()
	fifo := filepath.Join(dir, "dribble.bin")
	mkfifo(t, fifo)

	// A writer that delivers a little and then goes quiet forever.
	writerReady := make(chan *os.File, 1)
	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			writerReady <- nil
			return
		}
		w.Write([]byte("the beginning of a photograph"))
		writerReady <- w // then never write again, never close
	}()
	defer func() {
		if w := <-writerReady; w != nil {
			w.Close()
		}
	}()

	target := t.TempDir()
	items := []Item{{Src: fifo, Dst: "out.bin"}}

	failures := Copy(target, items, Options{StallTimeout: 60 * time.Millisecond})
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(failures))
	}
	var se *StallError
	if !errors.As(failures[0].Err, &se) {
		t.Fatalf("failure is %v, want a *StallError", failures[0].Err)
	}
	if _, err := os.Stat(filepath.Join(target, "out.bin")); !os.IsNotExist(err) {
		t.Error("a truncated destination survived; it should have been removed")
	}
}

// TestStallTimeoutZeroIsDisabled: a healthy copy must be untouched by the feature,
// and the old wait-forever behavior must still be reachable.
func TestStallTimeoutZeroIsDisabled(t *testing.T) {
	defer swapPollInterval(t, time.Millisecond)()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fine.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if f := Copy(target, items, Options{StallTimeout: 0}); len(f) != 0 {
		t.Fatalf("a healthy file was failed: %v", f)
	}
	if _, err := os.Stat(filepath.Join(target, "fine.bin")); err != nil {
		t.Errorf("healthy file did not land: %v", err)
	}
}

// TestStallTimeoutDoesNotFireOnASlowButMovingFile: the trigger is a stall, not a
// deadline. A file that keeps delivering bytes must survive, however long it takes.
func TestStallTimeoutDoesNotFireOnASlowButMovingFile(t *testing.T) {
	defer swapPollInterval(t, 2*time.Millisecond)()

	dir := t.TempDir()
	fifo := filepath.Join(dir, "slow.bin")
	mkfifo(t, fifo)

	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer w.Close()
		// Dribble for well past the stall timeout, but never actually stop.
		for i := 0; i < 12; i++ {
			w.Write(make([]byte, 1024))
			time.Sleep(5 * time.Millisecond)
		}
	}()

	target := t.TempDir()
	items := []Item{{Src: fifo, Dst: "slow.bin"}}

	// The whole transfer takes ~60ms, far longer than this timeout would allow if
	// it were a deadline rather than a stall detector.
	failures := Copy(target, items, Options{StallTimeout: 30 * time.Millisecond})
	if len(failures) != 0 {
		t.Fatalf("a slow but healthy file was abandoned: %v", failures[0].Err)
	}
	info, err := os.Stat(filepath.Join(target, "slow.bin"))
	if err != nil {
		t.Fatalf("file did not land: %v", err)
	}
	if info.Size() != 12*1024 {
		t.Errorf("landed %d bytes, want %d", info.Size(), 12*1024)
	}
}
