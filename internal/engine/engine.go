// Package engine runs a job: it walks the sources, inspects what it must, plans the
// superdirectory, finds duplicates, checks the space, copies, retries, and leaves a
// report behind.
//
// It draws nothing and asks nothing itself. A front end — the wizard's screens, or
// the command line's plain output — takes part through Hooks: it shows progress,
// decides which duplicates to skip, and runs the copy so it can own the display
// and the pause key. That keeps every rule about what is copied in one place, and
// the wizard and the command line can never disagree about it.
package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/dedup"
	"github.com/ozzyphantom/SuperDirectory/internal/filter"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
	"github.com/ozzyphantom/SuperDirectory/internal/organize"
)

// Stage is a step of a run, for progress display.
type Stage int

const (
	Measuring  Stage = iota // walking the sources, one stat per file
	Expanding               // unpacking archives found in the sources
	Inspecting              // reading types, titles and capture dates
	Scanning                // looking for duplicates
	Merging                 // merging text documents
	Copying
	Finishing // writing the report
)

func (s Stage) String() string {
	return [...]string{"measuring", "expanding", "inspecting", "scanning", "merging", "copying", "finishing"}[s]
}

// ErrStopped is returned when the run was stopped through its stop channel.
var ErrStopped = errors.New("stopped")

// ErrAbandoned is returned when a front end declined to go on: a cancel at a
// prompt. Nothing was copied.
var ErrAbandoned = errors.New("abandoned")

// Hooks are how a front end takes part in a run.
type Hooks interface {
	// Stage announces a step of the run.
	Stage(s Stage)
	// Progress reports a step's progress: done of total (zero when unknown), and
	// the file in hand.
	Progress(s Stage, done, total int, current string)
	// Scan reports a duplicate scan's progress.
	Scan(p dedup.Progress)
	// Duplicates decides which of the found sets to apply. Returning none skips
	// nothing. A front end may change a set's keeper (a review) before returning it.
	Duplicates(f *Found) ([]DupSet, error)
	// Space asks whether to copy although the destination looks too small.
	Space(need, free int64) (bool, error)
	// Copy runs the copy. It must call c.Run with c.Options, adding its own
	// OnProgress and Pause, and return the result.
	Copy(c *CopyRun) flatten.Result
	// Retry asks whether to copy the failed files again.
	Retry(failures []flatten.Failure, attempt, of int) bool
}

// CopyRun is one pass of the copy, handed to Hooks.Copy.
type CopyRun struct {
	Target  string
	Files   int
	Bytes   int64 // the plan's total size
	Retry   int   // 0 for the first pass; the attempt number when retrying
	Options flatten.Options
	Run     func(opts flatten.Options) flatten.Result
}

// DupSet is one group of duplicates over the plan: Keep is copied, Skip is not.
type DupSet struct {
	Kind  string // job.Identical, job.Pictures or job.Documents
	Keep  int    // plan index
	Skip  []int  // plan indices
	Bytes int64  // what skipping saves
	Score float64
}

// Found is what the duplicate scans turned up.
type Found struct {
	Items      []flatten.Item     // the plan the indices refer to
	Sets       []DupSet           // every set, of every kind
	Dims       map[int]dedup.Dims // displayed size of pictures in a set, by plan index
	Unreadable []string           // files a scan could not read; they are copied
	Review     bool               // the job asked to review the sets before skipping
}

// Count totals the files and bytes the sets of one kind would skip.
func (f *Found) Count(kind string) (files int, bytes int64) {
	for _, s := range f.Sets {
		if s.Kind == kind {
			files += len(s.Skip)
			bytes += s.Bytes
		}
	}
	return
}

// Summary is what a run did.
type Summary struct {
	Job      job.Job
	Planned  int // files in the final plan
	Result   flatten.Result
	Skipped  []DupSet // duplicate sets applied
	Renamed  int      // files named after their titles
	Retyped  int      // files whose extension was corrected from their content
	Expanded int      // files taken out of archives
	Merged   int      // documents merged into larger files
	Batches  int
	Elapsed  time.Duration
	Stopped  bool   // the run was stopped before it finished
	Report   string // the folder holding the report, or "" when none was written
}

// run carries one job through its stages.
type run struct {
	j     job.Job
	h     Hooks
	stop  <-chan struct{}
	table *organize.Table
	start time.Time

	notes  map[string]string // what happened to a planned file, by source path, for the report
	origin map[string]string // where a staged file came from, for the report: "manual.zip!/a.htm"
	gone   []row             // files that left the plan: expanded archives, skipped duplicates, merged documents
	sum    Summary
}

// Run carries out the job. It returns the summary of what was done, and an error
// when the run could not start or did not finish: ErrStopped when stopped,
// ErrAbandoned when the front end declined at a prompt.
func Run(j job.Job, h Hooks, stop <-chan struct{}) (Summary, error) {
	r := &run{j: j, h: h, stop: stop, start: time.Now(), notes: map[string]string{}, origin: map[string]string{}}
	r.sum.Job = j
	if err := j.Validate(); err != nil {
		return r.sum, err
	}
	table, err := loadTable()
	if err != nil {
		return r.sum, err
	}
	r.table = table

	_, statErr := os.Stat(j.Target)
	created := errors.Is(statErr, os.ErrNotExist)
	items, err := r.plan()
	if err != nil {
		// Planning may have staged files (expanded archives) in a destination it
		// created; a run that ends before copying leaves nothing behind.
		r.cleanup()
		if created {
			os.Remove(StateDir(j.Target))
			os.Remove(j.Target)
		}
		return r.sum, err
	}
	r.sum.Planned = len(items)
	if len(items) == 0 {
		r.sum.Elapsed = time.Since(r.start)
		return r.sum, nil
	}

	if err := os.MkdirAll(j.Target, 0o755); err != nil {
		return r.sum, fmt.Errorf("creating %s: %w", j.Target, err)
	}
	if !j.NoReport {
		if err := saveState(j.Target, State{Job: j, Started: r.start}); err != nil {
			return r.sum, fmt.Errorf("writing the run record: %w", err)
		}
		r.sum.Report = StateDir(j.Target)
	}

	if err := r.checkSpace(items); err != nil {
		return r.sum, err
	}

	res := r.copy(items, 0)
	for attempt := 1; attempt <= j.Retries && !closed(stop); attempt++ {
		retry := retryable(items, res)
		if len(retry) == 0 || !h.Retry(failuresOf(items, res, retry), attempt, j.Retries) {
			break
		}
		again := r.copy(pick(items, retry), attempt)
		res = mergeResults(items, res, again, retry)
	}
	r.sum.Result = res
	r.sum.Stopped = closed(stop)
	r.sum.Elapsed = time.Since(r.start)

	h.Stage(Finishing)
	r.cleanup()
	if !j.NoReport {
		if err := writeReport(r, items, res); err != nil {
			return r.sum, fmt.Errorf("writing the report: %w", err)
		}
		if err := saveState(j.Target, State{Job: j, Started: r.start, Finished: time.Now(), Complete: !r.sum.Stopped}); err != nil {
			return r.sum, err
		}
	}
	if r.sum.Stopped {
		return r.sum, ErrStopped
	}
	return r.sum, nil
}

// plan walks, inspects, lays out, and settles duplicates: the files to copy, with
// their destinations.
func (r *run) plan() ([]flatten.Item, error) {
	j := r.j
	rules := filter.From(&j, r.table)
	excluded := map[string]bool{}
	for _, e := range j.Excluded {
		excluded[filepath.Clean(e)] = true
	}

	r.h.Stage(Measuring)
	files, err := flatten.Scan{
		Sources:    j.Sources,
		Excluded:   excluded,
		Prune:      rules.Prune,
		Keep:       rules.Keep,
		Cancel:     r.stop,
		OnProgress: func(n int) { r.h.Progress(Measuring, n, 0, "") },
	}.Files()
	if errors.Is(err, flatten.ErrStopped) {
		return nil, ErrStopped
	}
	if err != nil {
		return nil, err
	}

	if j.Expand {
		if files, err = r.expand(files, rules); err != nil {
			return nil, err
		}
	}
	if err := r.inspect(files); err != nil {
		return nil, err
	}

	var items []flatten.Item
	switch j.LayoutOrFlat() {
	case job.ByType:
		items = organize.PlanFiles(files, organize.Options{KeepSourceTree: j.KeepFolders, Table: r.table})
	case job.ByDate:
		items = organize.PlanByDate(files)
	case job.ByDepth:
		items = flatten.PlanDepth(files, j.Depth)
	default:
		items = flatten.PlanFiles(files)
	}

	if len(j.Duplicates) > 0 && len(items) > 1 {
		if items, err = r.duplicates(items); err != nil {
			return nil, err
		}
	}
	if j.MergeText {
		if items, err = r.mergeText(items); err != nil {
			return nil, err
		}
	}
	if j.Batch > 0 {
		r.sum.Batches = batch(items, j.Batch)
	}
	for i := range items {
		items[i].Move = r.staged(items[i].Src)
	}
	return items, nil
}

// duplicates runs the scans the job asks for, each over what the one before left,
// then lets the front end decide.
func (r *run) duplicates(items []flatten.Item) ([]flatten.Item, error) {
	found := &Found{Items: items, Dims: map[int]dedup.Dims{}, Review: r.j.Review}
	opts := dedup.Options{StallTimeout: flatten.DefaultStallTimeout, Cancel: r.stop, OnProgress: r.h.Scan}

	if r.j.Finds(job.Identical) {
		r.h.Stage(Scanning)
		res := dedup.Find(items, opts)
		if res.Canceled {
			return nil, ErrStopped
		}
		for _, s := range res.Sets {
			found.Sets = append(found.Sets, DupSet{Kind: job.Identical, Keep: s.Keep, Skip: s.Skip, Bytes: s.Size * int64(len(s.Skip))})
		}
		found.Unreadable = append(found.Unreadable, res.Unreadable...)
	}
	if r.j.Finds(job.Pictures) {
		r.h.Stage(Scanning)
		rest, idx := remaining(items, found.Sets)
		res := dedup.FindSimilar(rest, opts)
		if res.Canceled {
			return nil, ErrStopped
		}
		for _, s := range res.Sets {
			set := DupSet{Kind: job.Pictures, Keep: idx[s.Keep]}
			for _, k := range s.Skip {
				set.Skip = append(set.Skip, idx[k])
				set.Bytes += rest[k].Size
			}
			found.Sets = append(found.Sets, set)
		}
		for i, d := range res.Dims {
			found.Dims[idx[i]] = d
		}
		found.Unreadable = append(found.Unreadable, res.Unreadable...)
	}
	if r.j.Finds(job.Documents) {
		r.h.Stage(Scanning)
		sets, err := r.documentDuplicates(items, found.Sets)
		if err != nil {
			return nil, err
		}
		found.Sets = append(found.Sets, sets...)
	}
	if len(found.Sets) == 0 {
		return items, nil
	}

	chosen, err := r.h.Duplicates(found)
	if err != nil {
		return nil, err
	}
	var skip []int
	for _, s := range chosen {
		for _, i := range s.Skip {
			skip = append(skip, i)
			r.gone = append(r.gone, row{
				status: "skipped", src: items[i].Src, rel: items[i].Rel, bytes: items[i].Size,
				note: kindPhrase(s.Kind) + " of " + items[s.Keep].Rel,
			})
		}
	}
	r.sum.Skipped = chosen
	out := dedup.Drop(items, skip)
	flatten.Assign(out) // survivors reclaim the plain names the skipped files held
	return out, nil
}

func kindPhrase(kind string) string {
	switch kind {
	case job.Pictures:
		return "smaller copy"
	case job.Documents:
		return "near-duplicate document"
	}
	return "identical copy"
}

// remaining is the plan without the files the sets skip, and each remaining file's
// index in the full plan.
func remaining(items []flatten.Item, sets []DupSet) ([]flatten.Item, []int) {
	gone := map[int]bool{}
	for _, s := range sets {
		for _, i := range s.Skip {
			gone[i] = true
		}
	}
	var rest []flatten.Item
	var idx []int
	for i, it := range items {
		if !gone[i] {
			rest = append(rest, it)
			idx = append(idx, i)
		}
	}
	return rest, idx
}

// checkSpace compares what the copy will write with the free space where it goes.
// Clones on one volume take no space, so a same-volume copy is not checked; nor
// is anything a resumed run finds already in place.
func (r *run) checkSpace(items []flatten.Item) error {
	if r.clones() {
		return nil
	}
	var need int64
	for _, it := range items {
		if info, err := os.Stat(filepath.Join(r.j.Target, it.Dst)); err == nil && info.Size() == it.Size {
			continue
		}
		need += it.Size
	}
	free, err := freeSpace(r.j.Target)
	if err != nil || need <= free-free/100 { // keep a 1% margin: filesystems round up
		return nil
	}
	ok, err := r.h.Space(need, free)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAbandoned
	}
	return nil
}

// clones reports whether every source shares the destination's volume, where the
// copier can clone instead of copying.
func (r *run) clones() bool {
	for _, s := range r.j.Sources {
		if !flatten.SameVolume(s, r.j.Target) {
			return false
		}
	}
	return true
}

// copy runs one pass of the copy through the front end.
func (r *run) copy(items []flatten.Item, attempt int) flatten.Result {
	var total int64
	for _, it := range items {
		total += it.Size
	}
	c := &CopyRun{
		Target: r.j.Target, Files: len(items), Bytes: total, Retry: attempt,
		Options: flatten.Options{
			StallTimeout: flatten.DefaultStallTimeout,
			Cancel:       r.stop,
			Resume:       true, // a destination is new or a run being resumed; either way this is safe
			Verify:       r.j.Verify,
			Clone:        r.clones(),
		},
		Run: func(opts flatten.Options) flatten.Result { return flatten.Execute(r.j.Target, items, opts) },
	}
	r.h.Stage(Copying)
	return r.h.Copy(c)
}

// retryable lists the plan indices that failed for a reason other than a stop.
func retryable(items []flatten.Item, res flatten.Result) []int {
	failed := map[string]bool{}
	for _, f := range res.Failures {
		if !errors.Is(f.Err, flatten.ErrCanceled) {
			failed[f.Src] = true
		}
	}
	var out []int
	for i, it := range items {
		if res.Outcomes[i] == flatten.Failed && failed[it.Src] {
			out = append(out, i)
		}
	}
	return out
}

func failuresOf(items []flatten.Item, res flatten.Result, idx []int) []flatten.Failure {
	want := map[string]bool{}
	for _, i := range idx {
		want[items[i].Src] = true
	}
	var out []flatten.Failure
	for _, f := range res.Failures {
		if want[f.Src] {
			out = append(out, f)
		}
	}
	return out
}

func pick(items []flatten.Item, idx []int) []flatten.Item {
	out := make([]flatten.Item, len(idx))
	for k, i := range idx {
		out[k] = items[i]
	}
	return out
}

// mergeResults folds a retry pass into the full result: retried files take their new
// outcome, and their earlier failures give way to whatever the retry reported.
func mergeResults(items []flatten.Item, res, again flatten.Result, idx []int) flatten.Result {
	out := flatten.Result{
		Outcomes:    append([]flatten.Outcome(nil), res.Outcomes...),
		Bytes:       res.Bytes + again.Bytes,
		ClonedBytes: res.ClonedBytes + again.ClonedBytes,
	}
	retried := map[string]bool{}
	for k, i := range idx {
		out.Outcomes[i] = again.Outcomes[k]
		retried[items[i].Src] = true
	}
	for _, f := range res.Failures {
		if !retried[f.Src] {
			out.Failures = append(out.Failures, f)
		}
	}
	out.Failures = append(out.Failures, again.Failures...)
	for _, o := range out.Outcomes {
		switch o {
		case flatten.Copied:
			out.Copied++
		case flatten.Cloned:
			out.Cloned++
		case flatten.Existing:
			out.Existing++
		}
	}
	return out
}

// freeSpace is flatten.FreeSpace, replaceable in tests.
var freeSpace = flatten.FreeSpace

// loadTable reads the user's category table, if they wrote one.
func loadTable() (*organize.Table, error) {
	dir, err := job.ConfigDir()
	if err != nil {
		return organize.DefaultTable(), nil
	}
	return organize.LoadTable(filepath.Join(dir, "categories.json"))
}

// closed reports whether c has been closed, without blocking.
func closed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
