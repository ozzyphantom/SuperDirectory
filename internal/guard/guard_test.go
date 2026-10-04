package guard

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ozzyphantom/SuperDirectory/internal/exif"
)

// TestCountingFileCountsEveryByte: progress and the stall clock depend on the
// count, so reads at an offset must count as much as reads in order.
func TestCountingFileCountsEveryByte(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.bin")
	os.WriteFile(p, make([]byte, 3000), 0o644)
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var n atomic.Int64
	c := &countingFile{f: f, n: &n}
	c.ReadAt(make([]byte, 1000), 2000)
	io.ReadAll(c)
	if n.Load() != 4000 {
		t.Errorf("counted %d bytes, want 4000", n.Load())
	}
}

func TestReadPlainAndCanceled(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(p, []byte("hello"), 0o644)
	read := func(f exif.File) (string, error) { b, err := io.ReadAll(f); return string(b), err }

	if got, err := Read(Reader{}, p, read); err != nil || got != "hello" {
		t.Errorf("plain read = %q, %v", got, err)
	}
	stop := make(chan struct{})
	close(stop)
	if _, err := Read(Reader{Cancel: stop}, p, read); !errors.Is(err, ErrCanceled) {
		t.Errorf("a stopped read returned %v", err)
	}
}
