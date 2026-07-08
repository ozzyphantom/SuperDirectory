// SuperDirectory builds a "superdirectory" from a nested tree: either one flat
// folder holding every file, or a folder per file type.
//
// The code is layered in three parts:
//  1. The functional core: a shared walk plus two planners — package flatten
//     (one folder, every file) and package organize (a folder per file type) —
//     both executed by the same collision-free copier.
//  2. A Charm/huh interactive wizard (package wizard), which knows nothing about
//     copying and returns a plain Result.
//  3. The content-inspection seam (package extract) where a future Python
//     backend can plug in without touching the Go core.
//
// Run the wizard:      go run .
// Try the extractor:   go run . inspect <dir>
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/ozzyphantom/SuperDirectory/internal/dedup"
	"github.com/ozzyphantom/SuperDirectory/internal/extract"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/organize"
	"github.com/ozzyphantom/SuperDirectory/internal/wizard"
)

var (
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("#2ecc71"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("#e74c3c"))
	orange = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	bold   = lipgloss.NewStyle().Bold(true)
	key    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "inspect" {
		runInspect(os.Args[2:])
		return
	}

	printIntro()
	for {
		target, completed := runOnce()
		if !completed {
			fmt.Println("\n  Exiting.")
			return
		}
		if !postCompletion(target) {
			return
		}
		fmt.Println()
	}
}

func printIntro() {
	fmt.Println()
	fmt.Println("  " + cyan.Render("SuperDirectory"))
	fmt.Println("  " + dim.Render("Flatten a nested tree, or sort it by file type.  ") + key.Render("Ctrl+C") + dim.Render(" exits anytime."))
}

// runOnce drives one full run. Returns the created target directory and
// whether it completed (false means the user aborted).
func runOnce() (string, bool) {
	res, err := wizard.Run()
	if err != nil {
		if wizard.IsAbort(err) {
			return "", false
		}
		fmt.Fprintln(os.Stderr, "\n  "+red.Render("Error: ")+err.Error())
		return "", false
	}

	// Both planners emit []flatten.Item; only the destination layout differs.
	var items []flatten.Item
	if res.Mode == wizard.ModeOrganize {
		items, err = organize.Plan(res.Source, res.Excluded, organize.Options{
			KeepSourceTree: res.KeepSourceTree,
		})
	} else {
		items, err = flatten.Plan(res.Source, res.Excluded)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "  "+red.Render("Error building plan: ")+err.Error())
		return "", false
	}
	if err := os.MkdirAll(res.Target, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "  "+red.Render("Error creating target: ")+err.Error())
		return "", false
	}

	fmt.Println()
	if len(items) == 0 {
		fmt.Println("  " + dim.Render("No files found to copy."))
		return res.Target, true
	}

	if res.FindDuplicates {
		kept, ok := resolveDuplicates(items)
		if !ok {
			return "", false // user cancelled at the duplicates prompt
		}
		items = kept
		if len(items) == 0 {
			fmt.Println("  " + dim.Render("Nothing left to copy."))
			return res.Target, true
		}
	}

	total := len(items)
	fmt.Printf("  Copying %s file(s) into %s\n\n", bold.Render(fmt.Sprintf("%d", total)), orange.Render(res.Target))

	drawer := &progressDrawer{}
	var last flatten.Progress
	failures := flatten.Copy(res.Target, items, flatten.Options{
		StallTimeout: flatten.DefaultStallTimeout,
		OnProgress: func(p flatten.Progress) {
			last = p
			drawer.draw(p)
		},
	})
	if len(failures) > 0 {
		fmt.Printf("\n  %s %d file(s) could not be copied:\n\n", red.Render("⚠"), len(failures))
		for _, f := range failures {
			fmt.Printf("    %s  %s\n       %s\n", red.Render("✗"), f.Src, dim.Render(f.Err.Error()))
		}
	}
	fmt.Printf("\n  %s  %s in %s  ·  %s average\n",
		green.Render(bold.Render("Finished!")),
		bold.Render(humanBytes(last.Bytes)),
		humanDuration(last.Elapsed),
		bold.Render(humanRate(last.Rate())))
	return res.Target, true
}

// resolveDuplicates scans the plan for byte-identical files and, if any are found,
// asks whether to skip them. It returns the plan to copy, and false if the user
// cancelled outright.
func resolveDuplicates(items []flatten.Item) ([]flatten.Item, bool) {
	fmt.Printf("  %s\n", dim.Render("Looking for duplicate files…"))

	var lastDraw time.Time
	res := dedup.Find(items, dedup.Options{
		// The scan reads files. On the drive that motivated this, one of them may
		// never read at all; the scan must not hang where the copy no longer does.
		StallTimeout: flatten.DefaultStallTimeout,
		OnProgress: func(p dedup.Progress) {
			if p.Total == 0 {
				return
			}
			if now := time.Now(); now.Sub(lastDraw) < 60*time.Millisecond && p.Done < p.Total {
				return
			} else {
				lastDraw = now
			}
			fmt.Printf("\r  %s\033[K", dim.Render(fmt.Sprintf(
				"hashing %d/%d candidates  %s", p.Done, p.Total, truncateMiddle(p.Current, 28))))
		},
	})
	fmt.Print("\r\033[K")

	if len(res.Unreadable) > 0 {
		fmt.Printf("  %s %d file(s) could not be read and are treated as unique.\n",
			orange.Render("!"), len(res.Unreadable))
	}
	if res.Files == 0 {
		fmt.Printf("  %s\n\n", dim.Render(fmt.Sprintf(
			"No duplicates found (%d file(s) read).", res.Hashed)))
		return items, true
	}

	var choice string
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(fmt.Sprintf("Found %d duplicate file(s), %s, across %d set(s)",
				res.Files, humanBytes(res.Bytes), len(res.Sets))).
			Description("Duplicates are byte-for-byte identical, whatever they are named.\nSkipping copies the first of each set and leaves the rest.").
			Options(
				huh.NewOption("Skip duplicates — copy one of each set", "skip"),
				huh.NewOption("Copy everything", "all"),
				huh.NewOption("Cancel", "cancel"),
			).
			Value(&choice),
	)).WithTheme(wizard.Theme())
	if err := form.Run(); err != nil {
		return nil, false // ctrl+c
	}

	switch choice {
	case "skip":
		out := dedup.Filter(items, res)
		fmt.Printf("\n  %s\n", green.Render(fmt.Sprintf(
			"Skipping %d duplicate(s), saving %s.", res.Files, humanBytes(res.Bytes))))
		return out, true
	case "all":
		return items, true
	default:
		return nil, false
	}
}

// postCompletion shows the after-copy menu. Open/reveal loop back to the menu;
// it returns true to flatten another directory, false to quit.
func postCompletion(target string) bool {
	for {
		var action string
		form := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("What next?").
				Options(
					huh.NewOption("Open the folder", "open"),
					huh.NewOption(revealLabel(), "reveal"),
					huh.NewOption("Do another directory", "another"),
					huh.NewOption("Quit", "quit"),
				).
				Value(&action),
		)).WithTheme(wizard.Theme())
		if err := form.Run(); err != nil {
			return false // Ctrl+C at the menu = quit
		}
		switch action {
		case "open":
			openInFileManager(target, false)
		case "reveal":
			openInFileManager(target, true)
		case "another":
			return true
		case "quit":
			return false
		}
	}
}

func revealLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "Reveal in Finder"
	case "windows":
		return "Show in Explorer"
	default:
		return "Show in file manager"
	}
}

// openInFileManager opens target in the OS file manager. When reveal is true it
// selects the folder inside its parent (macOS/Windows) rather than opening it.
func openInFileManager(target string, reveal bool) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		if reveal {
			cmd = exec.Command("open", "-R", target)
		} else {
			cmd = exec.Command("open", target)
		}
	case "windows":
		if reveal {
			cmd = exec.Command("explorer", "/select,", target)
		} else {
			cmd = exec.Command("explorer", target)
		}
	default: // linux, *bsd
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "  "+red.Render("Could not open: ")+err.Error())
		return
	}
	_ = cmd.Process.Release()
}

// stallAfter is how long the byte counter may sit still before the display says so.
// A copy blocked on an unreadable sector can hold the kernel in a retry loop for
// minutes; without this the user sees only a progress bar that stopped, and no way
// to tell a broken file from a big one.
const stallAfter = 5 * time.Second

// rateMeter smooths throughput for display, and notices when it stops entirely.
//
// A lifetime average hides exactly what you want to see on an external drive — the
// moment it slows down, whether from thermal throttling, a run of small files, or a
// full write cache. It reports a recent rate instead.
type rateMeter struct {
	lastAt    time.Duration
	lastBytes int64
	rate      float64 // exponentially-smoothed bytes/sec

	// Stall tracking is deliberately separate from rate smoothing. The rate is only
	// resampled every 300ms, but a stall must be measured from the last byte that
	// actually moved, however long ago that was.
	seenBytes    int64
	lastMovement time.Duration
}

// observe folds one progress report into the meter, returning the smoothed rate and
// how long the byte counter has been frozen.
//
// Samples closer than resampleAfter are accumulated rather than measured, because a
// burst of tiny files arriving within a millisecond of each other produces a
// meaningless instantaneous rate.
func (m *rateMeter) observe(p flatten.Progress) (rate float64, stalled time.Duration) {
	const resampleAfter = 300 * time.Millisecond

	if p.Bytes != m.seenBytes {
		m.seenBytes, m.lastMovement = p.Bytes, p.Elapsed
	}
	stalled = p.Elapsed - m.lastMovement

	dt := p.Elapsed - m.lastAt
	if dt < resampleAfter && m.rate != 0 {
		return m.rate, stalled
	}
	if secs := dt.Seconds(); secs > 0 {
		instant := float64(p.Bytes-m.lastBytes) / secs
		if m.rate == 0 {
			m.rate = instant
		} else {
			m.rate = 0.6*m.rate + 0.4*instant // favor history, follow real changes
		}
	}
	m.lastAt, m.lastBytes = p.Elapsed, p.Bytes
	return m.rate, stalled
}

// progressDrawer rate-limits terminal writes. Copy now reports before, during, and
// after every file — thousands of times a second on a folder of small files — and
// redrawing that often would make the terminal the bottleneck.
type progressDrawer struct {
	meter    rateMeter
	lastDraw time.Duration
}

func (d *progressDrawer) draw(p flatten.Progress) {
	const minFrameGap = 60 * time.Millisecond
	rate, stalled := d.meter.observe(p)

	final := p.Done == p.Total
	if !final && p.Elapsed-d.lastDraw < minFrameGap {
		return
	}
	d.lastDraw = p.Elapsed

	// Erase to end of line: the text shrinks as the ETA and filename do, and
	// leftovers from a longer previous frame would otherwise linger.
	fmt.Print("\r" + progressLine(p, rate, stalled) + "\033[K")
	if final {
		fmt.Println()
	}
}

// progressLine builds the progress display. Split from the drawer so it can be
// tested without capturing stdout.
//
// The bar tracks files, not bytes: knowing the total byte count in advance would
// cost an lstat per file before the copy began — measured at 43x the cost of the
// walk itself on exFAT — a poor trade on the very drives where progress matters.
func progressLine(p flatten.Progress, rate float64, stalled time.Duration) string {
	const width = 30
	filled := width * p.Done / p.Total
	bar := green.Render(strings.Repeat("█", filled)) + dim.Render(strings.Repeat("░", width-filled))

	line := fmt.Sprintf("  [%s] %3d%%  %d/%d  %s  %s",
		bar, p.Done*100/p.Total, p.Done, p.Total,
		dim.Render(humanBytes(p.Bytes)), bold.Render(humanRate(rate)))

	if stalled >= stallAfter {
		// Say it loudly, and say what it is stuck on. This is the difference
		// between "the app hung" and "this one file will not read".
		line += red.Render(fmt.Sprintf("  ⚠ no data for %s", humanDuration(stalled)))
	} else if eta, ok := estimateRemaining(p); ok {
		line += dim.Render("  ~" + humanDuration(eta) + " left")
	}
	if p.Current != "" && p.Done < p.Total {
		line += "  " + dim.Render(truncateMiddle(p.Current, 28))
	}
	return line
}

// truncateMiddle shortens a filename while keeping its extension visible, because
// the extension is what tells you whether this is the 40 MB raw or the thumbnail.
// It counts runes, not bytes: filenames are UTF-8, and slicing bytes would cut a
// character in half and print a replacement glyph.
func truncateMiddle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max || max < 5 {
		return s
	}
	keep := max - 1 // room for the ellipsis
	head := keep / 2
	tail := keep - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// estimateRemaining extrapolates from files completed, not bytes, because the
// total byte count is deliberately unknown. It is therefore only as good as the
// assumption that the remaining files resemble the ones already copied — fair for
// a folder of photos, poor for a mixed tree. It is suppressed until enough files
// have landed for the average to mean anything.
func estimateRemaining(p flatten.Progress) (time.Duration, bool) {
	remaining := p.Total - p.Done
	if p.Done < 3 || remaining <= 0 || p.Elapsed <= 0 {
		return 0, false
	}
	perFile := p.Elapsed / time.Duration(p.Done)
	eta := perFile * time.Duration(remaining)
	if eta < time.Second {
		return 0, false // "~0s left" tells the user nothing
	}
	return eta, true
}

// humanBytes formats a byte count in decimal units, matching how drive and
// transfer speeds are quoted.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTP"[exp])
}

func humanRate(bytesPerSec float64) string {
	if bytesPerSec <= 0 {
		return "— MB/s"
	}
	return fmt.Sprintf("%.1f MB/s", bytesPerSec/1e6)
}

func humanDuration(d time.Duration) string {
	// A fast copy really did take a fraction of a second; rounding it to "0s"
	// makes the summary look broken.
	if d < time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// runInspect exercises the pure-Go content extractor over one directory,
// proving the seam works end to end with zero external dependencies.
func runInspect(args []string) {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "  "+red.Render("Error: ")+err.Error())
		os.Exit(1)
	}

	ex := extract.MetadataExtractor{}
	fmt.Println()
	fmt.Println("  " + bold.Render("Content inspection") + dim.Render("   pure-Go metadata extractor"))
	fmt.Println()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m, err := ex.Extract(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		fmt.Printf("  %s\n    %s · %s\n", cyan.Render(m.Title), dim.Render(m.MIMEType), dim.Render(fmt.Sprintf("%d bytes", m.Size)))
	}
	fmt.Println()
}
