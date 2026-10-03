package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"

	"github.com/ozzyphantom/SuperDirectory/internal/dedup"
	"github.com/ozzyphantom/SuperDirectory/internal/engine"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
	"github.com/ozzyphantom/SuperDirectory/internal/wizard"
)

// Interactive is the wizard's front end: status lines while the run prepares,
// menus for its questions, and the copy screen.
type Interactive struct {
	Stop *Stopper

	// Review, when set, shows duplicate sets one by one and returns them as the
	// user left them.
	Review func(f *engine.Found, sets []engine.DupSet) ([]engine.DupSet, error)

	pending  bool // a status line is drawn with \r and not yet ended
	lastDraw time.Time
	forced   bool
}

var _ engine.Hooks = (*Interactive)(nil)

// Forced reports whether the user quit with a second Ctrl+C during the copy.
func (u *Interactive) Forced() bool { return u.forced }

// status redraws the single status line in place, at most every 60 ms.
func (u *Interactive) status(text string, final bool) {
	now := time.Now()
	if !final && now.Sub(u.lastDraw) < 60*time.Millisecond {
		return
	}
	u.lastDraw = now
	fmt.Printf("\r%s\033[K", ansi.Truncate("  "+dim.Render(text), termWidth(), "…"))
	u.pending = true
}

// endStatus clears the status line, so the next line starts clean.
func (u *Interactive) endStatus() {
	if u.pending {
		fmt.Print("\r\033[K")
		u.pending = false
	}
}

func (u *Interactive) Stage(s engine.Stage) {
	u.endStatus()
	switch s {
	case engine.Measuring:
		fmt.Println("  " + dim.Render("Measuring files…"))
	case engine.Expanding:
		fmt.Println("  " + dim.Render("Expanding archives…"))
	case engine.Inspecting:
		fmt.Println("  " + dim.Render("Reading types, titles and dates…"))
	case engine.Scanning:
		fmt.Println("  " + dim.Render("Looking for duplicates…"))
	case engine.Merging:
		fmt.Println("  " + dim.Render("Merging text documents…"))
	case engine.Finishing:
		fmt.Println("  " + dim.Render("Writing the report…"))
	}
}

func (u *Interactive) Progress(s engine.Stage, done, total int, current string) {
	switch {
	case total == 0:
		u.status(fmt.Sprintf("%s  %s", s, count(done, "file")), false)
	default:
		u.status(fmt.Sprintf("%s %s/%s  %s", s, thousands(done), thousands(total), truncateMiddle(current, 28)), done >= total)
	}
}

func (u *Interactive) Scan(p dedup.Progress) {
	if p.Total == 0 {
		return
	}
	u.status(scanStatus(p), p.Done >= p.Total)
}

// scanStatus describes a scan's progress in one line.
func scanStatus(p dedup.Progress) string {
	var s string
	switch p.Phase {
	case dedup.Sizing:
		return fmt.Sprintf("checking sizes  %d/%d", p.Done, p.Total)
	case dedup.ReadingHeaders:
		return fmt.Sprintf("reading picture headers  %d/%d", p.Done, p.Total)
	case dedup.Fingerprinting:
		s = fmt.Sprintf("fingerprinting %d/%d  %s", p.Done, p.Total, truncateMiddle(p.Current, 28))
	case dedup.Confirming:
		s = fmt.Sprintf("confirming %d/%d  %s", p.Done, p.Total, truncateMiddle(p.Current, 28))
	default:
		s = fmt.Sprintf("hashing %d/%d  %s", p.Done, p.Total, truncateMiddle(p.Current, 28))
	}
	// A large file's byte count shows a long read moving; on a small file it would
	// only flicker.
	if p.Size >= 8<<20 {
		s += fmt.Sprintf("  %s of %s", HumanBytes(p.Read), HumanBytes(p.Size))
	}
	return s
}

// Duplicates shows what the scans found, with examples, and asks what to skip.
func (u *Interactive) Duplicates(f *engine.Found) ([]engine.DupSet, error) {
	u.endStatus()
	if len(f.Unreadable) > 0 {
		fmt.Printf("  %s %s could not be read and %s treated as unique.\n", orange.Render("!"),
			count(len(f.Unreadable), "file"), map[bool]string{true: "is", false: "are"}[len(f.Unreadable) == 1])
	}
	title, desc, kinds := describeFound(f)

	options := []huh.Option[string]{huh.NewOption("Skip them all", "all")}
	if len(kinds) > 1 {
		options = append(options, huh.NewOption("Choose which kinds to skip…", "choose"))
	}
	if u.Review != nil {
		options = append(options, huh.NewOption("Review them one by one…", "review"))
	}
	options = append(options, huh.NewOption("Copy everything", "none"), huh.NewOption("Cancel", "cancel"))

	for {
		var choice string
		if err := wizard.Menu(huh.NewSelect[string]().Title(title).Description(desc).Options(options...).Value(&choice), false); err != nil {
			return nil, engine.ErrAbandoned
		}
		switch choice {
		case "all":
			return f.Sets, nil
		case "none":
			return nil, nil
		case "review":
			sets, err := u.Review(f, f.Sets)
			if errors.Is(err, ErrReviewBack) {
				continue // esc in the review returns to this prompt
			}
			return sets, err
		case "choose":
			return u.chooseKinds(f, kinds)
		}
		return nil, engine.ErrAbandoned
	}
}

// chooseKinds asks which kinds of duplicate to skip, all ticked to start.
func (u *Interactive) chooseKinds(f *engine.Found, kinds []string) ([]engine.DupSet, error) {
	var picked []string
	var opts []huh.Option[string]
	for _, k := range kinds {
		n, b := f.Count(k)
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s (%d, %s)", kindLabel(k), n, HumanBytes(b)), k).Selected(true))
	}
	if err := wizard.Form(false, huh.NewMultiSelect[string]().Title("Skip which kinds?").Options(opts...).Value(&picked)); err != nil {
		return nil, engine.ErrAbandoned
	}
	keep := map[string]bool{}
	for _, k := range picked {
		keep[k] = true
	}
	var out []engine.DupSet
	for _, s := range f.Sets {
		if keep[s.Kind] {
			out = append(out, s)
		}
	}
	return out, nil
}

func kindLabel(k string) string {
	switch k {
	case job.Pictures:
		return "Smaller copies of pictures"
	case job.Documents:
		return "Near-duplicate documents"
	}
	return "Identical files"
}

// describeFound words the scans' findings: a title with the counts, and a
// description per kind, with examples where a reader needs evidence.
func describeFound(f *engine.Found) (title, desc string, kinds []string) {
	var found, paras []string
	var total int64
	for _, k := range []string{job.Identical, job.Pictures, job.Documents} {
		n, b := f.Count(k)
		if n == 0 {
			continue
		}
		kinds = append(kinds, k)
		total += b
		switch k {
		case job.Identical:
			found = append(found, fmt.Sprintf("%d identical file(s)", n))
			paras = append(paras, "Identical: the same contents, whatever the name. One of each set is\ncopied: the name that is not a copy, nearest the top of the source.")
		case job.Pictures:
			found = append(found, fmt.Sprintf("%d smaller copies of pictures", n))
			paras = append(paras, "Smaller copies: the same picture at a lower resolution. The largest\nis copied. For example:\n"+examples(f, job.Pictures, 3))
		case job.Documents:
			found = append(found, fmt.Sprintf("%d near-duplicate document(s)", n))
			paras = append(paras, "Near-duplicates: documents that are mostly the same text, such as\nrevisions. The newest is copied. For example:\n"+examples(f, job.Documents, 3))
		}
	}
	return fmt.Sprintf("Found %s, %s", joinAnd(found), HumanBytes(total)), strings.Join(paras, "\n\n"), kinds
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// examples shows the first few sets of a kind: what would be skipped, and what
// is kept instead, so a choice to skip is made on evidence rather than a count.
func examples(f *engine.Found, kind string, n int) string {
	var lines []string
	shown, sets := 0, 0
	for _, s := range f.Sets {
		if s.Kind != kind {
			continue
		}
		sets++
		if shown == n {
			continue
		}
		shown++
		small, keep := s.Skip[0], s.Keep
		a := truncateMiddle(filepath.Base(f.Items[small].Src), 24)
		b := truncateMiddle(filepath.Base(f.Items[keep].Src), 24)
		switch kind {
		case job.Pictures:
			lines = append(lines, fmt.Sprintf("  %s %s  →  %s %s", a, dims(f.Dims[small]), b, dims(f.Dims[keep])))
		default:
			lines = append(lines, fmt.Sprintf("  %s  →  %s  (%d%% the same)", a, b, int(s.Score*100+0.5)))
		}
	}
	if more := sets - shown; more > 0 {
		lines = append(lines, fmt.Sprintf("  … and %d more", more))
	}
	return strings.Join(lines, "\n")
}

func dims(d dedup.Dims) string { return fmt.Sprintf("%d×%d", d.W, d.H) }

func (u *Interactive) Space(need, free int64) (bool, error) {
	u.endStatus()
	var choice string
	err := wizard.Menu(huh.NewSelect[string]().
		Title("The destination may be too small").
		Description(fmt.Sprintf("The copy needs %s; %s is free on that drive.", HumanBytes(need), HumanBytes(free))).
		Options(
			huh.NewOption("Copy anyway — it stops when the drive is full", "yes"),
			huh.NewOption("Cancel", "no"),
		).Value(&choice), false)
	if err != nil {
		return false, engine.ErrAbandoned
	}
	return choice == "yes", nil
}

func (u *Interactive) Copy(c *engine.CopyRun) flatten.Result {
	u.endStatus()
	fmt.Println()
	res, ok := runCopyScreen(c, u.Stop)
	if !ok {
		u.forced = true
		fmt.Println()
		os.Exit(130)
	}
	return res
}

func (u *Interactive) Retry(failures []flatten.Failure, attempt, of int) bool {
	printFailures(os.Stdout, failures)
	var choice string
	err := wizard.Menu(huh.NewSelect[string]().
		Title(fmt.Sprintf("Try the %s again?", count(len(failures), "failed file"))).
		Description("A file that stalled on a hot drive often reads after a rest.").
		Options(
			huh.NewOption(fmt.Sprintf("Try again (attempt %d of %d)", attempt, of), "yes"),
			huh.NewOption("Continue without them", "no"),
		).Value(&choice), false)
	return err == nil && choice == "yes"
}
