package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/ozzyphantom/SuperDirectory/internal/dedup"
	"github.com/ozzyphantom/SuperDirectory/internal/engine"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

// Plain is the command line's front end. It decides from flags instead of asking,
// writes to stderr so stdout stays clean for scripts, and draws in place only when
// stderr is a terminal; otherwise it logs a line every ten seconds.
type Plain struct {
	Out  io.Writer // stderr, usually
	Stop *Stopper
	Yes  bool // copy even when the destination looks too small

	// Review, when set, shows duplicate sets one by one before skipping; the
	// command line sets it for --review on a terminal.
	Review func(f *engine.Found, sets []engine.DupSet) ([]engine.DupSet, error)

	tty      bool
	once     sync.Once
	pending  bool
	lastDraw time.Time
}

var _ engine.Hooks = (*Plain)(nil)

func (u *Plain) init() {
	u.once.Do(func() {
		if u.Out == nil {
			u.Out = os.Stderr
		}
		if f, ok := u.Out.(*os.File); ok {
			u.tty = term.IsTerminal(f.Fd())
		}
	})
}

func (u *Plain) line(s string) {
	u.init()
	if u.pending {
		fmt.Fprint(u.Out, "\r\033[K")
		u.pending = false
	}
	fmt.Fprintln(u.Out, s)
}

// status draws a transient line in place on a terminal, or logs it every ten
// seconds elsewhere.
func (u *Plain) status(s string, final bool) {
	u.init()
	now := time.Now()
	gap := 60 * time.Millisecond
	if !u.tty {
		gap = 10 * time.Second
	}
	if !final && now.Sub(u.lastDraw) < gap {
		return
	}
	u.lastDraw = now
	if u.tty {
		fmt.Fprintf(u.Out, "\r%s\033[K", ansi.Truncate("  "+s, termWidth(), "…"))
		u.pending = true
		return
	}
	fmt.Fprintln(u.Out, "  "+ansi.Strip(s))
}

func (u *Plain) Stage(s engine.Stage) {
	if s != engine.Copying {
		u.line("superdirectory: " + s.String() + "…")
	}
}

func (u *Plain) Progress(s engine.Stage, done, total int, current string) {
	if total == 0 {
		u.status(fmt.Sprintf("%s  %s", s, count(done, "file")), false)
		return
	}
	u.status(fmt.Sprintf("%s %s/%s  %s", s, thousands(done), thousands(total), truncateMiddle(current, 28)), done >= total)
}

func (u *Plain) Scan(p dedup.Progress) {
	if p.Total > 0 {
		u.status(scanStatus(p), p.Done >= p.Total)
	}
}

// Duplicates skips every set the flags asked to look for, after a review when
// asked for one.
func (u *Plain) Duplicates(f *engine.Found) ([]engine.DupSet, error) {
	title, _, _ := describeFound(f)
	u.line("superdirectory: " + strings.TrimPrefix(title, "Found "))
	if u.Review != nil && f.Review {
		sets, err := u.Review(f, f.Sets)
		if errors.Is(err, ErrReviewBack) {
			return nil, engine.ErrAbandoned
		}
		return sets, err
	}
	return f.Sets, nil
}

func (u *Plain) Space(need, free int64) (bool, error) {
	u.line(fmt.Sprintf("superdirectory: the copy needs %s; %s is free on the destination", HumanBytes(need), HumanBytes(free)))
	if !u.Yes {
		u.line("superdirectory: not copying; pass --yes to copy anyway")
	}
	return u.Yes, nil
}

func (u *Plain) Copy(c *engine.CopyRun) flatten.Result {
	u.init()
	u.line(fmt.Sprintf("superdirectory: copying %s (%s) into %s", count(c.Files, "file"), HumanBytes(c.Bytes), c.Target))
	start := time.Now()
	var meter rateMeter
	var est estimator
	opts := c.Options
	opts.OnProgress = func(p flatten.Progress) {
		at := time.Since(start)
		f := frame{p: p, totalBytes: c.Bytes}
		f.rate, f.stalled = meter.observe(p.Bytes, at)
		f.left, f.stage = est.eta(p, f.rate, c.Bytes, at)
		u.status(progressLine(f, termWidth()), p.Done >= p.Total)
	}
	res := c.Run(opts)
	if u.pending {
		fmt.Fprintln(u.Out)
		u.pending = false
	}
	return res
}

func (u *Plain) Retry(failures []flatten.Failure, attempt, of int) bool {
	u.init()
	printFailures(u.Out, failures)
	u.line(fmt.Sprintf("superdirectory: retrying %s (attempt %d of %d)", count(len(failures), "failed file"), attempt, of))
	return true
}

// printFailures lists files that could not be copied, with the reason for each.
func printFailures(w io.Writer, failures []flatten.Failure) {
	if len(failures) == 0 {
		return
	}
	fmt.Fprintf(w, "\n  %s %s could not be copied:\n\n", red.Render("⚠"), count(len(failures), "file"))
	for _, f := range failures {
		fmt.Fprintf(w, "    %s  %s\n       %s\n", red.Render("✗"), f.Src, dim.Render(f.Err.Error()))
	}
}

// PrintSummary says what a run did: how much arrived and how fast, what it
// skipped, what failed, and where the report is. A stopped run says how to
// resume.
func PrintSummary(w io.Writer, s engine.Summary) {
	r := s.Result
	var real []flatten.Failure
	var partial string
	for _, f := range r.Failures {
		if f.Err == flatten.ErrCanceled {
			partial = f.Src
			continue
		}
		real = append(real, f)
	}
	printFailures(w, real)

	written := r.Copied + r.Cloned
	if s.Stopped {
		fmt.Fprintf(w, "\n  %s  %s of %s copied into %s\n", orange.Render(bold.Render("Stopped.")),
			thousands(written+r.Existing), count(s.Planned, "file"), orange.Render(s.Job.Target))
		if partial != "" {
			fmt.Fprintln(w, "  "+dim.Render("The partial copy of "+partial+" was removed."))
		}
		fmt.Fprintln(w, "  "+dim.Render("Run it again into the same folder to resume where it stopped."))
		return
	}
	if s.Planned == 0 {
		fmt.Fprintln(w, "  "+dim.Render("No files to copy."))
		return
	}
	rate := 0.0
	if secs := s.Elapsed.Seconds(); secs > 0 {
		rate = float64(r.Bytes) / secs
	}
	switch {
	case r.Copied == 0 && r.Cloned > 0:
		// Clones on one volume move no data; a rate would be a fiction.
		fmt.Fprintf(w, "\n  %s  %s in %s  ·  %s\n", green.Render(bold.Render("Finished!")),
			bold.Render(HumanBytes(r.ClonedBytes)), humanDuration(s.Elapsed), dim.Render("cloned on the same volume, no data copied"))
	default:
		fmt.Fprintf(w, "\n  %s  %s in %s  ·  %s average\n", green.Render(bold.Render("Finished!")),
			bold.Render(HumanBytes(r.Bytes+r.ClonedBytes)), humanDuration(s.Elapsed), bold.Render(humanRate(rate)))
	}

	var parts []string
	if r.Copied > 0 {
		parts = append(parts, thousands(r.Copied)+" copied")
	}
	if r.Cloned > 0 {
		parts = append(parts, thousands(r.Cloned)+" cloned")
	}
	if r.Existing > 0 {
		parts = append(parts, thousands(r.Existing)+" already there")
	}
	if n := skippedFiles(s); n > 0 {
		parts = append(parts, count(n, "duplicate")+" skipped")
	}
	if s.Merged > 0 {
		parts = append(parts, thousands(s.Merged)+" merged")
	}
	if len(real) > 0 {
		parts = append(parts, red.Render(thousands(len(real))+" failed"))
	}
	fmt.Fprintln(w, "  "+strings.Join(parts, " · "))
	if s.Batches > 0 {
		fmt.Fprintf(w, "  %s\n", dim.Render(fmt.Sprintf("In %d batch folders of at most %d files.", s.Batches, s.Job.Batch)))
	}
	if s.Report != "" {
		fmt.Fprintln(w, "  "+dim.Render("Report: "+s.Report+"/report.md"))
	}
}

func skippedFiles(s engine.Summary) int {
	n := 0
	for _, set := range s.Skipped {
		n += len(set.Skip)
	}
	return n
}
