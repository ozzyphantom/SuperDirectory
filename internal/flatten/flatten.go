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
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Item is one planned copy: an absolute source path, and the destination
// relative to the target directory. Plan produces bare filenames here;
// package organize produces nested paths like "Images/jpg/beach.jpg". Copy
// creates whatever parent directories the path implies.
type Item struct {
	Src string
	Dst string

	// Want is the destination the planner asked for, before Assign gave it a
	// collision suffix. Dst equals Want unless an earlier file claimed it.
	Want string

	// Rel is where the file sat, relative to the sources, for the report. Size
	// and ModTime are as the walk measured them: resume compares them with what
	// is already in the destination.
	Rel     string
	Size    int64
	ModTime time.Time

	// Move says Src is a file the run made itself — unpacked from an archive,
	// merged from documents — staged on the destination's volume. It is renamed
	// into place rather than copied a second time.
	Move bool
}

// Assign gives every item a collision-free Dst from its Want, in plan order. The
// planners call it. A caller that drops items from a plan — skipped duplicates —
// calls it again, so a survivor reclaims the plain name a dropped file was
// holding: "beach.jpg", not "beach_1.jpg". An item with no Want keeps its Dst as
// the name it wants.
func Assign(items []Item) {
	used := map[string]bool{}
	for i := range items {
		want := items[i].Want
		if want == "" {
			want = items[i].Dst
		}
		items[i].Dst = Unique(used, want)
	}
}

// Failure records a file that could not be copied, with the cause.
type Failure struct {
	Src string
	Err error
}

// Plan walks source and returns the ordered copy plan for a flat superdirectory.
func Plan(source string, excluded map[string]bool) ([]Item, error) {
	files, err := Scan{Sources: []string{source}, Excluded: excluded}.Files()
	if err != nil {
		return nil, err
	}
	return PlanFiles(files), nil
}

// PlanFiles is the flat layout. Files at the top keep their name; files in a
// folder are prefixed with "<folder>_" to keep a hint of where they came from.
// Remaining collisions get a numeric "_1", "_2" suffix from Assign.
func PlanFiles(files []File) []Item {
	items := make([]Item, len(files))
	for i, f := range files {
		name := f.BaseName()
		if dir := f.Dir(); dir != "." {
			name = filepath.Base(dir) + "_" + name
		}
		items[i] = itemFor(f, name)
	}
	Assign(items)
	return items
}

// PlanDepth keeps the top depth levels of folders and flattens everything below
// them into the folder at that level, each file prefixed with "<folder>_" as in
// the flat layout. Depth 1 turns Docs/A/B/c.pdf into Docs/B_c.pdf.
func PlanDepth(files []File, depth int) []Item {
	items := make([]Item, len(files))
	for i, f := range files {
		name := f.BaseName()
		dir := f.Dir()
		if dir != "." {
			parts := strings.Split(dir, string(filepath.Separator))
			if len(parts) > depth {
				name = parts[len(parts)-1] + "_" + name
				dir = filepath.Join(parts[:depth]...)
			}
			name = filepath.Join(dir, name)
		}
		items[i] = itemFor(f, name)
	}
	Assign(items)
	return items
}

// itemFor is the plan entry for a file headed for want.
func itemFor(f File, want string) Item {
	return Item{Src: f.Path, Want: want, Rel: f.Rel, Size: f.Size, ModTime: f.ModTime}
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

// Progress is reported to Execute's callback: once when a file is about to be
// read, repeatedly while a large file streams, and once when it completes.
//
// Reporting only on completion was a mistake. Through a single large file the bar,
// the byte count, and the rate all froze at their last values — which is precisely
// what a hang looks like. Worse, the frozen frame named no file, so a copy blocked
// on an unreadable source told the user nothing. Current fixes that: whatever the
// copy is stuck on, its name is on screen, and Bytes keeps moving if it is merely
// big rather than broken.
type Progress struct {
	Done    int    // files settled: copied, cloned, already there, or failed
	Total   int    // files in the plan
	Bytes   int64  // bytes written so far, including the file in flight
	Current string // base name of the file being copied
	Elapsed time.Duration

	// Existing counts files a resumed copy found already in place, and
	// ExistingBytes their size: work done by an earlier run, not this one.
	Existing      int
	ExistingBytes int64

	// ClonedBytes is the size of files cloned rather than copied. A clone
	// finishes a file without moving its data, so it advances the bar but
	// not the transfer rate.
	ClonedBytes int64
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

// pollInterval is how often the copy samples the in-flight file's byte counter. A
// var rather than a const so tests can drive the stall logic without waiting.
var pollInterval = 100 * time.Millisecond

// Options configure Execute.
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
	// destination is removed, and the copy moves on. The goroutine remains parked
	// in the kernel until the read finally returns, holding one buffer. That is a
	// deliberate leak, bounded by the number of unreadable files, and it beats
	// hanging the whole program on one bad sector.
	//
	// Enabling it also forces every file through the chunked copy loop, because a
	// stall can only be detected in a copy that reports as it goes. On Linux that
	// gives up the kernel's copy_file_range for small files. You cannot both hand
	// the copy to the kernel and watch it progress.
	StallTimeout time.Duration

	// Cancel, once closed, stops the copy. The file in flight is abandoned exactly
	// as a stalled one is, its partial destination is removed, and it is returned as
	// a Failure wrapping ErrCanceled. No further file is started. Files already
	// copied stay where they are. A nil channel never cancels.
	//
	// Without this, Ctrl+C killed the process mid-write and left a truncated file in
	// the superdirectory under the source's own name, looking complete.
	Cancel <-chan struct{}

	// Pause, when set, holds the copy while paused: between files, and between
	// chunks of a large one. A paused copy is not a stalled one; the stall clock
	// stops with it. A drive that overheats can cool without the run starting over.
	Pause *Pauser

	// Resume skips a file whose destination already holds a file of the same size
	// and modification time. Copies preserve the time, so a run that stopped part
	// way — Ctrl+C, a hot drive, a pulled cable — picks up where it left off. The
	// times are allowed two seconds' difference, FAT32's resolution.
	Resume bool

	// Verify reads each copy back and compares its SHA-256 with the source's,
	// hashed as it streamed. It doubles the reading on the destination drive. A
	// copy that does not match is deleted and reported.
	Verify bool

	// Clone makes copy-on-write clones where the filesystem offers them: APFS on
	// macOS, Btrfs and XFS on Linux. A clone is instant and takes no space until
	// one side changes. Where cloning fails the file is copied normally.
	Clone bool
}

// Outcome is what happened to one file of the plan.
type Outcome int

const (
	NotReached Outcome = iota // the copy stopped before this file
	Copied                    // written
	Cloned                    // cloned, sharing the source's blocks
	Existing                  // already in place from an earlier run (Resume)
	Failed                    // could not be copied; see Failures
)

// Result is what Execute did.
type Result struct {
	Outcomes []Outcome // one per plan item, in plan order
	Failures []Failure

	Copied, Cloned, Existing int
	Bytes                    int64 // bytes written, including discarded partial files
	ClonedBytes              int64 // size of the files cloned, which wrote no data

	// Elapsed is how long the copy worked, pauses excluded; Paused is how long it
	// was paused. A rate is Bytes over Elapsed.
	Elapsed, Paused time.Duration

	// Full says the destination filled up. The copy stopped at the file that did
	// not fit: every file after it would fail the same way, slowly, on a slow drive.
	// The rest are NotReached, for a resume once there is room.
	Full bool

	existingBytes int64
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

// isFull is diskFull, replaceable in tests: no test can fill a disk on demand.
var isFull = diskFull

// VerifyError reports a copy whose contents did not match its source.
type VerifyError struct{}

func (VerifyError) Error() string {
	return "the copy did not match its source when read back — it was deleted"
}

// errAborted is the sentinel a job returns when it notices it has been abandoned.
var errAborted = errors.New("copy abandoned")

// ErrCanceled marks the file that was in flight when Options.Cancel closed.
var ErrCanceled = errors.New("copy stopped — partial file removed")

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

// Copy executes the plan and returns only its failures.
func Copy(target string, items []Item, opts Options) []Failure {
	return Execute(target, items, opts).Failures
}

// Execute carries out the plan into target. It never aborts on a single failure;
// instead it collects them so the caller can report at the end. A file that fails
// part-way has its partial destination removed: a truncated photo silently sitting
// in the output is worse than a missing one that is named in the failures.
//
// Items whose Dst names a nested path get their parent directories created on
// demand. The set of already-created directories is cached, so a plan with
// thousands of files in one type folder costs one mkdir, not thousands.
func Execute(target string, items []Item, opts Options) (res Result) {
	res = Result{Outcomes: make([]Outcome, len(items))}
	total := len(items)
	made := map[string]bool{target: true}
	start := time.Now()
	defer func() {
		res.Paused = opts.Pause.Total()
		res.Elapsed = time.Since(start) - res.Paused
	}()

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
		var existingBytes int64
		if res.Existing > 0 {
			existingBytes = res.existingBytes
		}
		opts.OnProgress(Progress{
			Done: done, Total: total, Bytes: bytes,
			Current: current, Elapsed: time.Since(start),
			Existing: res.Existing, ExistingBytes: existingBytes,
			ClonedBytes: res.ClonedBytes,
		})
	}

	for i, it := range items {
		opts.Pause.wait(opts.Cancel) // a pause between files holds here
		if closed(opts.Cancel) {
			return res
		}
		name := filepath.Base(it.Src)
		cur = nil
		report(i, name) // announce BEFORE touching the file

		dst := filepath.Join(target, it.Dst)
		if opts.Resume && alreadyThere(dst, it) {
			res.Outcomes[i] = Existing
			res.Existing++
			res.existingBytes += it.Size
			report(i+1, name)
			continue
		}
		if err := ensureParent(made, filepath.Dir(dst)); err != nil {
			res.Failures = append(res.Failures, Failure{Src: it.Src, Err: err})
			res.Outcomes[i] = Failed
			report(i+1, name)
			if isFull(err) {
				res.Full = true
				return res
			}
			continue
		}
		if it.Move && os.Rename(it.Src, dst) == nil {
			res.Outcomes[i] = Copied
			res.Copied++
			res.ClonedBytes += it.Size // in place, but no data moved
			report(i+1, name)
			continue
		}

		job := &copyJob{
			src: it.Src, dst: dst,
			stream: opts.StallTimeout > 0 || opts.Verify || opts.Pause != nil,
			verify: opts.Verify, clone: opts.Clone, pause: opts.Pause,
			stopped: make(chan struct{}),
		}
		cur = job
		errc := make(chan error, 1) // buffered: an abandoned job must not block forever
		go func() { errc <- job.run() }()

		err := awaitFile(job, errc, opts.StallTimeout, opts.Cancel, opts.Pause, func() { report(i, name) })

		// Bytes that reached the device count toward throughput even if the file is
		// then discarded: the drive did the work, and the rate should say so.
		base += job.written.Load()
		res.Bytes += job.written.Load()
		cur = nil

		switch {
		case err != nil:
			res.Failures = append(res.Failures, Failure{Src: it.Src, Err: err})
			res.Outcomes[i] = Failed
			os.Remove(dst) // leave no truncated file behind
		case job.cloned.Load():
			res.Outcomes[i] = Cloned
			res.Cloned++
			res.ClonedBytes += it.Size
		default:
			res.Outcomes[i] = Copied
			res.Copied++
		}
		if errors.Is(err, ErrCanceled) {
			return res // the file never completed, so it is not reported done
		}
		report(i+1, name)
		if isFull(err) {
			res.Full = true
			return res
		}
	}
	return res
}

// alreadyThere reports whether dst holds what a resumed copy would write: a
// regular file of the item's size, modified at the item's time give or take two
// seconds. An item with no measured time cannot be matched.
func alreadyThere(dst string, it Item) bool {
	if it.ModTime.IsZero() {
		return false
	}
	info, err := os.Stat(dst)
	if err != nil || !info.Mode().IsRegular() || info.Size() != it.Size {
		return false
	}
	d := info.ModTime().Sub(it.ModTime)
	return d <= 2*time.Second && d >= -2*time.Second
}

// awaitFile waits for the job, polling its byte counters so progress is visible
// through a long file and a stall is noticed in a short one. A closed cancel
// abandons the job the same way a stall does. Paused time is never stalled time.
func awaitFile(job *copyJob, errc <-chan error, stall time.Duration, cancel <-chan struct{}, pause *Pauser, tick func()) error {
	t := time.NewTicker(pollInterval)
	defer t.Stop()

	lastBytes, lastMoved := int64(0), time.Now()
	for {
		select {
		case err := <-errc:
			return err
		case <-cancel:
			// A job that finished in the same instant keeps its result: discarding a
			// complete file to honor a stop request would be perverse.
			select {
			case err := <-errc:
				return err
			default:
			}
			job.abort()
			return ErrCanceled
		case now := <-t.C:
			if n := job.written.Load() + job.verified.Load(); n != lastBytes || pause.Paused() {
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
// abandoned job may still be reading into its buffer long after the copy has moved
// to the next file; reusing that memory would corrupt both. An abandoned job
// returns its buffer whenever the kernel finally releases it, or never — either is
// safe.
var bufPool = sync.Pool{New: func() any { b := make([]byte, streamChunk); return &b }}

// copyJob is one file's copy, made abandonable. Its descriptors are recorded as
// they open so that abort can close them from another goroutine, and its byte
// counters are atomic because the copy polls them while the job writes them.
type copyJob struct {
	src, dst string

	// stream forces the chunked loop even for a small file. It must be set whenever
	// the caller is watching for a stall, verifying, or able to pause: io.CopyBuffer
	// reports its bytes once, at the end, so a small file transferring slowly over a
	// sick drive would sit at zero bytes for its whole life and be abandoned as
	// stuck. You cannot both hand the copy to the kernel and watch it progress.
	stream bool
	verify bool
	clone  bool
	pause  *Pauser

	written  atomic.Int64
	verified atomic.Int64 // bytes read back while verifying: movement, for the stall clock
	cloned   atomic.Bool
	aborted  atomic.Bool
	stopped  chan struct{} // closed by abort, so a paused job wakes to see it

	mu       sync.Mutex
	in, out  *os.File
	stopOnce sync.Once
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
// into a file the copy has already discarded.
func (j *copyJob) abort() {
	j.aborted.Store(true)
	j.stopOnce.Do(func() {
		if j.stopped != nil {
			close(j.stopped)
		}
	})
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

// wait holds the job while paused. It returns false if the job was abandoned.
func (j *copyJob) wait() bool {
	j.pause.wait(j.stopped)
	return !j.aborted.Load()
}

// run performs the copy, preserving permission bits and modification time. It
// checks for abandonment between steps so a job that was given up on cannot
// recreate the destination the copy just removed.
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

	if j.clone && j.tryClone(info) {
		return nil
	}

	out, err := os.OpenFile(j.dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if !j.keep(out, false) {
		// Abandoned between the check above and this open: the copy may already
		// have removed the destination, and this open just recreated it, empty.
		out.Close()
		os.Remove(j.dst)
		return errAborted
	}

	bufp := bufPool.Get().(*[]byte)
	var sum hash.Hash
	if j.verify {
		sum = sha256.New()
	}
	if j.stream || info.Size() >= streamAbove {
		err = streamCopy(out, in, *bufp, func(n int64) { j.written.Add(n) }, j.wait, sum)
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
	if sum != nil {
		if err := j.check(sum.Sum(nil)); err != nil {
			return err
		}
	}

	// Preserve the modification time. A superdirectory is usually an archive or a
	// working copy, and a folder where every file claims to be from today is much
	// less useful than one that remembers when its files were written. Resume also
	// depends on it.
	//
	// The zero atime means "leave access time alone" (os.Chtimes documents this).
	// A failure here is deliberately NOT fatal: the file's contents are already
	// safely on disk, and reporting the whole copy as failed over a timestamp would
	// send the user hunting for data that arrived intact. Some filesystems and
	// mount options simply refuse the update.
	_ = os.Chtimes(j.dst, time.Time{}, info.ModTime())
	return nil
}

// tryClone attempts a copy-on-write clone. It reports whether the file is done;
// on any failure the caller copies normally.
func (j *copyJob) tryClone(info os.FileInfo) bool {
	os.Remove(j.dst) // a clone will not replace a file; a stale one goes first
	if err := cloneFile(j.src, j.dst); err != nil {
		os.Remove(j.dst)
		return false
	}
	if j.aborted.Load() {
		os.Remove(j.dst)
		return false
	}
	j.cloned.Store(true)
	_ = os.Chtimes(j.dst, time.Time{}, info.ModTime())
	return true
}

// check reads the copy back and compares its hash with the source's.
func (j *copyJob) check(want []byte) error {
	f, err := os.Open(j.dst)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	bufp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufp)
	for {
		if !j.wait() {
			return errAborted
		}
		n, err := f.Read(*bufp)
		if n > 0 {
			h.Write((*bufp)[:n])
			j.verified.Add(int64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return VerifyError{}
	}
	return nil
}

// streamCopy is io.Copy's inner loop with a progress callback, a pause gate
// between chunks, and an optional hash of what it reads. It exists so a
// multi-gigabyte file reports as it goes, can be paused part way, and can be
// verified without reading the source twice.
func streamCopy(dst io.Writer, src io.Reader, buf []byte, onWritten func(int64), gate func() bool, sum hash.Hash) error {
	for {
		if gate != nil && !gate() {
			return errAborted
		}
		nr, rerr := src.Read(buf)
		if nr > 0 {
			if sum != nil {
				sum.Write(buf[:nr])
			}
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

// Pauser holds a copy while paused. The zero value is running. A nil *Pauser is
// never paused, so callers that do not offer pausing pass nothing.
type Pauser struct {
	mu    sync.Mutex
	gate  chan struct{} // non-nil while paused; closed to release waiters
	since time.Time
	total time.Duration // pauses that have ended
}

// Toggle pauses a running copy or resumes a paused one, and reports the new state.
func (p *Pauser) Toggle() (paused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate == nil {
		p.gate, p.since = make(chan struct{}), time.Now()
		return true
	}
	close(p.gate)
	p.gate = nil
	p.total += time.Since(p.since)
	return false
}

// Total is how long the copy has been paused, the current pause included.
func (p *Pauser) Total() time.Duration {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.total
	if p.gate != nil {
		t += time.Since(p.since)
	}
	return t
}

// Paused reports whether the copy is paused, and since when.
func (p *Pauser) Paused() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gate != nil
}

// Since is when the current pause began; zero when running.
func (p *Pauser) Since() time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate == nil {
		return time.Time{}
	}
	return p.since
}

// wait blocks while paused, until resumed or stop closes.
func (p *Pauser) wait(stop <-chan struct{}) {
	if p == nil {
		return
	}
	p.mu.Lock()
	g := p.gate
	p.mu.Unlock()
	if g == nil {
		return
	}
	select {
	case <-g:
	case <-stop:
	}
}
