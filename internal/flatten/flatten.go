// Package flatten holds the functional core of SuperDirectory: walk a source
// tree, decide a collision-free destination for every file, and copy.
//
// This package has NO knowledge of the TUI. It is pure, deterministic, and
// unit-testable — that separation is one of the reasons Go was chosen: the
// core stays independent of the interactive layer.
//
// Plan is one of two planners. The other, package organize, sorts files into
// type folders instead of one flat directory; both emit []Item and both are
// executed by Copy.
package flatten

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/fsmeta"
)

// Item is one planned copy: an absolute source path, and the destination
// relative to the target directory. Plan produces bare filenames here;
// package organize produces nested paths like "Images/jpg/beach.jpg". Copy
// creates whatever parent directories the path implies.
type Item struct {
	Src string
	Dst string
}

// Failure records a file that could not be copied, with the cause.
type Failure struct {
	Src string
	Err error
}

// Walk visits every regular file under source in lexical order, skipping
// excluded directories and everything beneath them. Unreadable entries are
// tolerated rather than fatal, mirroring the Python code's `except
// PermissionError` behavior. Symlinks, sockets, and devices are skipped — real
// files only.
//
// Filesystem bookkeeping is skipped too: .DS_Store, AppleDouble "._" sidecars,
// .Spotlight-V100/, $RECYCLE.BIN/ and friends (see package fsmeta). Without this,
// flattening the root of an external drive copies more operating-system metadata
// than user files. A metadata directory is pruned entirely, not merely skipped.
//
// The source directory itself is never treated as metadata: if the user
// deliberately points at .Trashes, that is their business.
//
// Both planners share this traversal so that exclusion semantics can never
// drift between them.
func Walk(source string, excluded map[string]bool, fn func(path string, d os.DirEntry)) error {
	return filepath.WalkDir(source, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path == source {
				return nil
			}
			if excluded[path] || fsmeta.IsMetadata(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || fsmeta.IsMetadata(d.Name()) {
			return nil
		}
		fn(path, d)
		return nil
	})
}

// Plan walks source and returns the ordered copy plan for a flat superdirectory.
// Files in the source root keep their name; files in a subdirectory are prefixed
// with "<parentdir>_" to preserve context. Remaining collisions get a numeric
// "_1", "_2" suffix — matching the Python implementation.
func Plan(source string, excluded map[string]bool) ([]Item, error) {
	var items []Item
	used := map[string]bool{}

	err := Walk(source, excluded, func(path string, d os.DirEntry) {
		parent := filepath.Dir(path)
		name := d.Name()
		newName := name
		if parent != source {
			newName = filepath.Base(parent) + "_" + name
		}
		items = append(items, Item{Src: path, Dst: Unique(used, newName)})
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// Unique reserves name in used, appending _1, _2, ... before the extension
// until it finds a free slot. The returned name keeps its original case.
// Exported so package organize can apply the same collision rule to its nested
// paths.
//
// Reservation is CASE-INSENSITIVE. On macOS (APFS), Windows (NTFS), and nearly
// every external drive (exFAT, FAT32), "beach.JPG" and "Beach.jpg" name the same
// file. Reserving them as distinct would plan two copies to one destination, and
// the second would silently overwrite the first — data loss with no failure
// reported. Package organize makes this easy to reach: pooling by file type
// discards the directory, so two files that differed only by folder can end up
// differing only by case.
//
// On a genuinely case-sensitive volume this only ever adds a numeric suffix to
// two names that differ solely in case. That is the safe direction, and matches
// the same trade-off the wizard's copy-into-itself guard already accepts.
func Unique(used map[string]bool, name string) string {
	if key := strings.ToLower(name); !used[key] {
		used[key] = true
		return name
	}
	ext := filepath.Ext(name)
	base := name[:len(name)-len(ext)]
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s_%d%s", base, i, ext)
		if key := strings.ToLower(cand); !used[key] {
			used[key] = true
			return cand
		}
	}
}

// Progress is reported to Copy's callback: once when a file is about to be read,
// repeatedly while a large file streams, and once when it completes.
//
// Reporting only on completion was a mistake. Through a single large file the bar,
// the byte count, and the rate all froze at their last values — which is precisely
// what a hang looks like. Worse, the frozen frame named no file, so a copy blocked
// on an unreadable source told the user nothing. Current fixes that: whatever the
// copy is stuck on, its name is on screen, and Bytes keeps moving if it is merely
// big rather than broken.
type Progress struct {
	Done    int    // files fully copied
	Total   int    // files in the plan
	Bytes   int64  // bytes written so far, including the file in flight
	Current string // base name of the file being copied
	Elapsed time.Duration
}

// Rate returns the average throughput in bytes per second since the copy began,
// or 0 before any time has passed.
func (p Progress) Rate() float64 {
	if p.Elapsed <= 0 {
		return 0
	}
	return float64(p.Bytes) / p.Elapsed.Seconds()
}

// DefaultStallTimeout is a reasonable ceiling on how long one file may produce no
// bytes before it is presumed unreadable. A bad sector holds the kernel in a retry
// loop; a healthy file, however large, keeps delivering chunks.
const DefaultStallTimeout = 60 * time.Second

// pollInterval is how often Copy samples the in-flight file's byte counter. A var
// rather than a const so tests can drive the stall logic without waiting.
var pollInterval = 100 * time.Millisecond

// Options configure Copy.
type Options struct {
	// OnProgress, if set, is called before each file is opened — so the name of a
	// file that then blocks forever is already on screen — about every
	// pollInterval while it copies, and once when it completes. Callers should
	// rate-limit their own drawing; this reports faithfully and cheaply.
	OnProgress func(Progress)

	// StallTimeout abandons a file that has produced no bytes for this long,
	// records it as a Failure, and moves on. Zero disables it, and the copy will
	// wait on a stuck file forever, which is the old behavior.
	//
	// This cannot interrupt the read. Go's SetReadDeadline works only on pollable
	// descriptors — pipes and sockets — never on a regular file, and no syscall
	// unblocks a read stuck in a disk retry. So the file's copy runs on its own
	// goroutine and is *abandoned*: its descriptors are closed, its partial
	// destination is removed, and Copy moves on. The goroutine remains parked in
	// the kernel until the read finally returns, holding one buffer. That is a
	// deliberate leak, bounded by the number of unreadable files, and it beats
	// hanging the whole program on one bad sector.
	//
	// Enabling it also forces every file through the chunked copy loop, because a
	// stall can only be detected in a copy that reports as it goes. On Linux that
	// gives up the kernel's copy_file_range for small files. You cannot both hand
	// the copy to the kernel and watch it progress.
	StallTimeout time.Duration
}

// StallError reports a file abandoned for producing no data.
type StallError struct{ After time.Duration }

func (e *StallError) Error() string {
	d := e.After
	if d >= time.Second {
		d = d.Round(time.Second) // "no data for 0s" is not a sentence worth printing
	}
	return fmt.Sprintf("no data for %s — file abandoned, it may be unreadable", d)
}

// errAborted is the sentinel a job returns when it notices it has been abandoned.
var errAborted = errors.New("copy abandoned")

// Copy executes the plan into target. It never aborts on a single failure; instead
// it collects and returns them so the caller can report at the end. A file that
// fails part-way has its partial destination removed: a truncated photo silently
// sitting in the output is worse than a missing one that is named in the failures.
//
// Items whose Dst names a nested path get their parent directories created on
// demand. The set of already-created directories is cached, so a plan with
// thousands of files in one type folder costs one mkdir, not thousands.
func Copy(target string, items []Item, opts Options) []Failure {
	var failures []Failure
	total := len(items)
	made := map[string]bool{target: true}
	start := time.Now()

	var base int64   // bytes from completed files
	var cur *copyJob // the file in flight, if any
	report := func(done int, current string) {
		if opts.OnProgress == nil {
			return
		}
		bytes := base
		if cur != nil {
			bytes += cur.written.Load()
		}
		opts.OnProgress(Progress{
			Done: done, Total: total, Bytes: bytes,
			Current: current, Elapsed: time.Since(start),
		})
	}

	for i, it := range items {
		name := filepath.Base(it.Src)
		cur = nil
		report(i, name) // announce BEFORE touching the file

		dst := filepath.Join(target, it.Dst)
		if err := ensureParent(made, filepath.Dir(dst)); err != nil {
			failures = append(failures, Failure{Src: it.Src, Err: err})
			report(i+1, name)
			continue
		}

		job := &copyJob{src: it.Src, dst: dst, stream: opts.StallTimeout > 0}
		cur = job
		errc := make(chan error, 1) // buffered: an abandoned job must not block forever
		go func() { errc <- job.run() }()

		err := awaitFile(job, errc, opts.StallTimeout, func() { report(i, name) })

		// Bytes that reached the device count toward throughput even if the file is
		// then discarded: the drive did the work, and the rate should say so.
		base += job.written.Load()
		cur = nil

		if err != nil {
			failures = append(failures, Failure{Src: it.Src, Err: err})
			os.Remove(dst) // leave no truncated file behind
		}
		report(i+1, name)
	}
	return failures
}

// awaitFile waits for the job, polling its byte counter so progress is visible
// through a long file and a stall is noticed in a short one.
func awaitFile(job *copyJob, errc <-chan error, stall time.Duration, tick func()) error {
	t := time.NewTicker(pollInterval)
	defer t.Stop()

	lastBytes, lastMoved := int64(0), time.Now()
	for {
		select {
		case err := <-errc:
			return err
		case now := <-t.C:
			if n := job.written.Load(); n != lastBytes {
				lastBytes, lastMoved = n, now
			}
			tick()
			if stall > 0 {
				if idle := now.Sub(lastMoved); idle >= stall {
					job.abort()
					return &StallError{After: idle}
				}
			}
		}
	}
}

// ensureParent creates dir once, remembering it. A failed MkdirAll is not
// cached, so a later item targeting the same directory retries rather than
// silently inheriting the earlier error.
func ensureParent(made map[string]bool, dir string) error {
	if made[dir] {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	made[dir] = true
	return nil
}

const (
	// streamChunk is the read size used when reporting progress through a file.
	// Measured against exFAT, buffer size makes no difference to throughput (32 KiB
	// and 4 MiB are within run-to-run noise), so this is chosen for a sensible
	// callback cadence rather than for speed.
	streamChunk = 256 << 10

	// streamAbove is the size at which a file is copied chunk by chunk so its
	// progress can be reported. Below it, a file finishes faster than a frame, and
	// io.CopyBuffer is used instead — which lets the kernel fast path (Linux's
	// copy_file_range) engage. On macOS there is no such fast path: os.File.readFrom
	// is a stub for every GOOS except freebsd, linux, and solaris.
	streamAbove = 8 << 20
)

// bufPool recycles copy buffers. A pool rather than one shared buffer, because an
// abandoned job may still be reading into its buffer long after Copy has moved to
// the next file; reusing that memory would corrupt both. An abandoned job returns
// its buffer whenever the kernel finally releases it, or never — either is safe.
var bufPool = sync.Pool{New: func() any { b := make([]byte, streamChunk); return &b }}

// copyJob is one file's copy, made abandonable. Its descriptors are recorded as
// they open so that abort can close them from another goroutine, and its byte
// counter is atomic because Copy polls it while the job writes it.
type copyJob struct {
	src, dst string

	// stream forces the chunked loop even for a small file. It must be set whenever
	// the caller is watching for a stall: io.CopyBuffer reports its bytes once, at
	// the end, so a small file transferring slowly over a sick drive would sit at
	// zero bytes for its whole life and be abandoned as stuck. You cannot both hand
	// the copy to the kernel and watch it progress.
	stream bool

	written atomic.Int64
	aborted atomic.Bool

	mu      sync.Mutex
	in, out *os.File
}

// keep records an open descriptor so abort can reach it. It returns false if the
// job was abandoned in the meantime, in which case the caller closes and gives up.
func (j *copyJob) keep(f *os.File, isSource bool) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.aborted.Load() {
		return false
	}
	if isSource {
		j.in = f
	} else {
		j.out = f
	}
	return true
}

// abort closes whatever the job has opened. Closing does not unblock a read stuck
// in a disk retry — nothing does — but it releases the descriptors as soon as the
// kernel returns, and makes every later write on this job fail rather than scribble
// into a file Copy has already discarded.
func (j *copyJob) abort() {
	j.aborted.Store(true)
	j.mu.Lock()
	in, out := j.in, j.out
	j.mu.Unlock()
	if in != nil {
		in.Close()
	}
	if out != nil {
		out.Close()
	}
}

// run performs the copy, preserving permission bits and modification time. It
// checks for abandonment between steps so a job that was given up on cannot
// recreate the destination Copy just removed.
func (j *copyJob) run() error {
	in, err := os.Open(j.src)
	if err != nil {
		return err
	}
	if !j.keep(in, true) {
		in.Close()
		return errAborted
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	if j.aborted.Load() {
		return errAborted
	}

	out, err := os.OpenFile(j.dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if !j.keep(out, false) {
		out.Close()
		return errAborted
	}

	bufp := bufPool.Get().(*[]byte)
	if j.stream || info.Size() >= streamAbove {
		err = streamCopy(out, in, *bufp, func(n int64) { j.written.Add(n) })
	} else {
		// io.CopyBuffer ignores buf when dst implements io.ReaderFrom, which is how
		// the Linux fast path survives. Only reachable with stall detection off.
		n, cerr := io.CopyBuffer(out, in, *bufp)
		if n > 0 {
			j.written.Add(n)
		}
		err = cerr
	}
	bufPool.Put(bufp)

	if err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if j.aborted.Load() {
		return errAborted
	}

	// Preserve the modification time. A superdirectory is usually an archive or a
	// working copy, and a folder where every file claims to be from today is much
	// less useful than one that remembers when its files were written.
	//
	// The zero atime means "leave access time alone" (os.Chtimes documents this).
	// A failure here is deliberately NOT fatal: the file's contents are already
	// safely on disk, and reporting the whole copy as failed over a timestamp would
	// send the user hunting for data that arrived intact. Some filesystems and
	// mount options simply refuse the update.
	_ = os.Chtimes(j.dst, time.Time{}, info.ModTime())
	return nil
}

// streamCopy is io.Copy's inner loop with a progress callback. It exists only so
// that a multi-gigabyte file reports as it goes; using it for every file would
// disable the kernel copy path on the systems that have one.
func streamCopy(dst io.Writer, src io.Reader, buf []byte, onWritten func(int64)) error {
	for {
		nr, rerr := src.Read(buf)
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			if nw > 0 && onWritten != nil {
				onWritten(int64(nw))
			}
			if werr != nil {
				return werr
			}
			if nw != nr {
				return io.ErrShortWrite
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}
