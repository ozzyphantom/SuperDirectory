// Package wizard is the interactive way to describe a run. It walks the user
// through the steps below and returns a job.Job; it copies nothing itself.
//
//  1. Preset — only when presets exist: start fresh or from a saved one.
//  2. Layout — flat, by type, by date taken, or keep the top folders.
//  3. Sources — one folder or several.
//  4. Destination — a new folder, or an unfinished superdirectory to resume.
//  5. Exclusions — only when a source has subfolders.
//  6. Layout detail — the type layout's folders, or the depth to keep.
//  7. Filters — every file, or types, sizes, dates and name patterns.
//  8. Duplicates — which kinds to look for, and whether to review them.
//  9. Documents — titles, content types, archives, merging, batches.
//  10. Options — report, retries, verification, notification.
//  11. Confirm — copy, save as a preset, go back, or cancel.
//
// Every screen can step back with esc. The steps form a history stack, so going
// back always returns to the screen last shown, whichever ones were skipped.
package wizard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/ozzyphantom/SuperDirectory/internal/engine"
	"github.com/ozzyphantom/SuperDirectory/internal/exclude"
	"github.com/ozzyphantom/SuperDirectory/internal/fsmeta"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
	"github.com/ozzyphantom/SuperDirectory/internal/organize"
	"github.com/ozzyphantom/SuperDirectory/internal/pick"
)

// errBack is an internal sentinel: a step is asking to return to the previous
// one. It never escapes Run.
var errBack = errors.New("back a step")

// IsAbort reports whether err is a user-initiated cancel (Ctrl+C / q).
func IsAbort(err error) bool {
	return errors.Is(err, huh.ErrUserAborted)
}

type step int

const (
	stepPreset step = iota
	stepLayout
	stepSources
	stepDest
	stepExclude
	stepDetail
	stepFilters
	stepDuplicates
	stepDocuments
	stepOptions
	stepConfirm
	stepDone
)

// Run drives the interactive flow and returns the job, or an error: use IsAbort
// to tell a cancel from a failure.
func Run() (job.Job, error) {
	w := &flow{j: job.Job{Retries: 1}}
	cur := stepPreset
	var history []step
	for cur != stepDone {
		next, err := w.do(cur)
		switch {
		case errors.Is(err, errBack):
			if len(history) == 0 {
				continue // the first screen has nothing behind it
			}
			cur, history = history[len(history)-1], history[:len(history)-1]
			continue
		case errors.Is(err, errSkip):
			cur = next // a step with nothing to ask leaves no history
			continue
		case err != nil:
			return job.Job{}, err
		}
		history = append(history, cur)
		cur = next
	}
	return w.j, nil
}

// errSkip marks a step that had nothing to ask this time.
var errSkip = errors.New("skip")

// flow is the job being built, and what the steps learned along the way.
type flow struct {
	j    job.Job
	note string // shown once on the confirm screen, e.g. "Saved preset X"
}

func (w *flow) do(s step) (step, error) {
	switch s {
	case stepPreset:
		return w.preset()
	case stepLayout:
		return w.layout()
	case stepSources:
		return w.sources()
	case stepDest:
		return w.destination()
	case stepExclude:
		return w.exclusions()
	case stepDetail:
		return w.detail()
	case stepFilters:
		return w.filters()
	case stepDuplicates:
		return w.duplicates()
	case stepDocuments:
		return w.documents()
	case stepOptions:
		return w.options()
	default:
		return w.confirm()
	}
}

// preset offers the saved presets. A preset holds a whole job, so choosing one
// goes straight to the confirm screen, with a fresh destination name beside the
// last one when that folder is already full.
func (w *flow) preset() (step, error) {
	names, _ := job.Presets()
	if len(names) == 0 {
		return stepLayout, errSkip
	}
	choice := "fresh"
	opts := []huh.Option[string]{huh.NewOption("Start fresh", "fresh")}
	for _, n := range names {
		opts = append(opts, huh.NewOption("Preset: "+n, n))
	}
	if err := Menu(huh.NewSelect[string]().Title("Start from a preset?").Options(opts...).Value(&choice), false); err != nil {
		return 0, huh.ErrUserAborted
	}
	if choice == "fresh" {
		return stepLayout, nil
	}
	j, err := job.LoadPreset(choice)
	if err != nil {
		return 0, err
	}
	if j.Target == "" || checkTarget(j.Target) != nil {
		parent := filepath.Dir(j.Target)
		if j.Target == "" && len(j.Sources) > 0 {
			parent = filepath.Dir(j.Sources[0])
		}
		base := filepath.Base(j.Target)
		if j.Target == "" {
			base = safeBase(j.Sources[0]) + "-super"
		}
		j.Target = filepath.Join(parent, freeName(parent, base))
	}
	w.j = j
	w.note = "Loaded preset " + choice + "."
	return stepConfirm, nil
}

func (w *flow) layout() (step, error) {
	choice := string(w.j.LayoutOrFlat())
	err := Menu(huh.NewSelect[string]().
		Title("How should the superdirectory be arranged?").
		Description("Flat pools every file in one folder. By type sorts into "+strings.Join(organize.Categories(), ", ")+",\nand Other. By date taken sorts photos and videos into year/month folders.\nKeep top folders keeps the first levels of folders and flattens below them.").
		Options(
			huh.NewOption("Flat — one folder, every file", string(job.Flat)),
			huh.NewOption("By type — a folder per file type", string(job.ByType)),
			huh.NewOption("By date taken — a folder per year and month", string(job.ByDate)),
			huh.NewOption("Keep top folders — flatten below a chosen depth", string(job.ByDepth)),
		).
		Value(&choice), true)
	if err != nil {
		return 0, err
	}
	w.j.Layout = job.Layout(choice)
	return stepSources, nil
}

// sources gathers one source folder or several. Sources may not nest: a folder
// inside another would be copied twice.
func (w *flow) sources() (step, error) {
	for {
		if len(w.j.Sources) == 0 {
			s, err := askSource()
			if err != nil {
				return 0, err
			}
			w.j.Sources = []string{s}
			continue
		}
		choice := "continue"
		opts := []huh.Option[string]{
			huh.NewOption("Continue", "continue"),
			huh.NewOption("Add another source", "add"),
		}
		if len(w.j.Sources) > 1 {
			opts = append(opts, huh.NewOption("Remove the last one", "remove"))
		}
		opts = append(opts, huh.NewOption("Go back", "back"))
		err := Menu(huh.NewSelect[string]().
			Title(fmt.Sprintf("Copy from %d folder(s)", len(w.j.Sources))).
			Description(strings.Join(w.j.Sources, "\n")).
			Options(opts...).
			Value(&choice), true)
		if err != nil {
			return 0, err
		}
		switch choice {
		case "back":
			w.j.Sources = nil
			return 0, errBack
		case "remove":
			w.j.Sources = w.j.Sources[:len(w.j.Sources)-1]
		case "add":
			s, err := askSource()
			if errors.Is(err, errBack) {
				continue
			}
			if err != nil {
				return 0, err
			}
			if overlapsAny(s, w.j.Sources) {
				fmt.Fprintln(os.Stderr, "  "+lipgloss.NewStyle().Foreground(lipgloss.Color("#e74c3c")).Render("That folder overlaps one already chosen; its files would be copied twice."))
				continue
			}
			w.j.Sources = append(w.j.Sources, s)
		case "continue":
			if w.j.Target != "" && overlapsAny(w.j.Target, w.j.Sources) {
				w.j.Target = ""
			}
			w.j.Excluded = keepUnder(w.j.Excluded, w.j.Sources)
			return stepDest, nil
		}
	}
}

func overlapsAny(p string, list []string) bool {
	for _, x := range list {
		if job.Overlaps(p, x) {
			return true
		}
	}
	return false
}

// keepUnder drops exclusions that no longer sit under any source.
func keepUnder(excluded, sources []string) []string {
	var out []string
	for _, e := range excluded {
		for _, s := range sources {
			if rel, err := filepath.Rel(s, e); err == nil && !strings.HasPrefix(rel, "..") {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// destination asks where the superdirectory goes. Naming a superdirectory whose
// copy never finished offers to resume it, with the settings it started with.
func (w *flow) destination() (step, error) {
	for {
		t, err := askDestination(w.j.Sources, w.j.Target)
		if err != nil {
			return 0, err
		}
		if !engine.Interrupted(t) {
			w.j.Target = t
			return stepExclude, nil
		}
		choice := "resume"
		err = Menu(huh.NewSelect[string]().
			Title(filepath.Base(t)+" holds a copy that did not finish").
			Description("Resuming copies only what is missing, with the settings it started with.").
			Options(
				huh.NewOption("Resume it", "resume"),
				huh.NewOption("Choose another name", "other"),
			).Value(&choice), true)
		if errors.Is(err, errBack) || choice == "other" {
			continue
		}
		if err != nil {
			return 0, err
		}
		state, err := engine.LoadState(t)
		if err != nil {
			return 0, err
		}
		w.j = state.Job
		w.note = "Resuming the copy into " + t + "."
		return stepConfirm, nil
	}
}

// exclusions offers the exclusion tree for each source that has subfolders.
func (w *flow) exclusions() (step, error) {
	var withSubs []string
	total := 0
	for _, s := range w.j.Sources {
		if n := topLevelSubdirCount(s); n > 0 {
			withSubs = append(withSubs, s)
			total += n
		}
	}
	if len(withSubs) == 0 {
		w.j.Excluded = nil
		return stepDetail, errSkip
	}
	for {
		choice := "no"
		if len(w.j.Excluded) > 0 {
			choice = "yes"
		}
		err := Menu(huh.NewSelect[string]().
			Title("Exclude any subfolders?").
			Description(fmt.Sprintf("%d at the top level. You can descend to any depth.", total)).
			Options(
				huh.NewOption("No — copy everything", "no"),
				huh.NewOption("Yes — choose what to skip", "yes"),
				huh.NewOption("Go back", "back"),
			).
			Value(&choice), true)
		if err != nil {
			return 0, err
		}
		switch choice {
		case "back":
			return 0, errBack
		case "no":
			w.j.Excluded = nil
			return stepDetail, nil
		}
		chosen := map[string]bool{}
		for _, e := range w.j.Excluded {
			chosen[e] = true
		}
		backed := false
		for _, s := range withSubs {
			ex, err := exclude.Run(s, chosen)
			if errors.Is(err, exclude.ErrBack) {
				backed = true
				break
			}
			if errors.Is(err, exclude.ErrCanceled) {
				return 0, huh.ErrUserAborted
			}
			if err != nil {
				return 0, err
			}
			for p := range chosen {
				if strings.HasPrefix(p, s+string(filepath.Separator)) {
					delete(chosen, p)
				}
			}
			for p := range ex {
				chosen[p] = true
			}
		}
		if backed {
			continue // esc in a tree returns to this menu
		}
		w.j.Excluded = nil
		for p := range chosen {
			w.j.Excluded = append(w.j.Excluded, p)
		}
		sort.Strings(w.j.Excluded)
		return stepDetail, nil
	}
}

// detail asks what the chosen layout needs: whether the type layout keeps the
// original folders, or how many levels the depth layout keeps.
func (w *flow) detail() (step, error) {
	switch w.j.LayoutOrFlat() {
	case job.ByType:
		keep, err := askLayout(w.j.KeepFolders)
		if err != nil {
			return 0, err
		}
		w.j.KeepFolders = keep
		return stepFilters, nil
	case job.ByDepth:
		choice := strconv.Itoa(max(1, w.j.Depth))
		var opts []huh.Option[string]
		for n := 1; n <= 5; n++ {
			opts = append(opts, huh.NewOption(fmt.Sprintf("%d — %s", n, depthExample(n)), strconv.Itoa(n)))
		}
		if err := Menu(huh.NewSelect[string]().
			Title("How many levels of folders to keep?").
			Description("Files deeper than this are flattened into the folder at that level,\nnamed with their own folder as a prefix.").
			Options(opts...).Value(&choice), true); err != nil {
			return 0, err
		}
		w.j.Depth, _ = strconv.Atoi(choice)
		return stepFilters, nil
	}
	return stepFilters, errSkip
}

func depthExample(n int) string {
	parts := []string{"Docs", "Net", "Cisco", "IOS", "v15", "Guides"}
	kept := strings.Join(parts[:n], "/")
	return kept + "/" + parts[n] + "_setup.pdf"
}

// filters asks whether to copy every file or only some: by type, then by name,
// size and date, on two screens so each fits a small window.
func (w *flow) filters() (step, error) {
	for {
		choice := "all"
		if len(w.j.Only) > 0 || len(w.j.Not) > 0 || len(w.j.Skip) > 0 || w.j.MinSize > 0 || w.j.MaxSize > 0 || w.j.Since != "" || w.j.Until != "" {
			choice = "some"
		}
		err := Menu(huh.NewSelect[string]().
			Title("Copy every file?").
			Description("Filters choose files by type, name, size, or date modified.").
			Options(
				huh.NewOption("Yes — every file", "all"),
				huh.NewOption("No — choose filters…", "some"),
				huh.NewOption("Go back", "back"),
			).Value(&choice), true)
		if err != nil {
			return 0, err
		}
		switch choice {
		case "back":
			return 0, errBack
		case "all":
			w.j.Only, w.j.Not, w.j.Skip = nil, nil, nil
			w.j.MinSize, w.j.MaxSize, w.j.Since, w.j.Until = 0, 0, "", ""
			return stepDuplicates, nil
		}
		if err := w.typeFilters(); errors.Is(err, errBack) {
			continue
		} else if err != nil {
			return 0, err
		}
		if err := w.otherFilters(); errors.Is(err, errBack) {
			continue
		} else if err != nil {
			return 0, err
		}
		return stepDuplicates, nil
	}
}

// typeFilters is a checklist of categories, all ticked to start: untick what is
// not wanted. Ticking only Documents copies only documents; unticking Video drops
// video. Specific extensions can narrow it further.
func (w *flow) typeFilters() error {
	cats := append(organize.Categories(), organize.CategoryOther)
	on := map[string]bool{}
	switch {
	case len(w.j.Only) > 0:
		for _, c := range w.j.Only {
			on[strings.ToLower(c)] = true
		}
	default:
		for _, c := range cats {
			on[strings.ToLower(c)] = true
		}
		for _, c := range w.j.Not {
			delete(on, strings.ToLower(c))
		}
	}
	var picked []string
	var opts []huh.Option[string]
	for _, c := range cats {
		opts = append(opts, huh.NewOption(c, c).Selected(on[strings.ToLower(c)]))
	}
	exts := strings.Join(onlyExtensions(w.j.Only, cats), ", ")
	err := Form(true,
		huh.NewMultiSelect[string]().Title("Which types?").Description("Untick what you do not want copied.").Options(opts...).Value(&picked),
		huh.NewInput().Title("Only these extensions (optional)").Placeholder("e.g. pdf, docx").Value(&exts),
	)
	if err != nil {
		return err
	}
	w.j.Only, w.j.Not = nil, nil
	if e := splitList(exts); len(e) > 0 {
		w.j.Only = e
		return nil
	}
	if len(picked) == len(cats) || len(picked) == 0 {
		return nil
	}
	if len(picked) <= len(cats)/2 {
		w.j.Only = picked
		return nil
	}
	chosen := map[string]bool{}
	for _, p := range picked {
		chosen[p] = true
	}
	for _, c := range cats {
		if !chosen[c] {
			w.j.Not = append(w.j.Not, c)
		}
	}
	return nil
}

// onlyExtensions picks the extensions out of an Only list, leaving categories.
func onlyExtensions(only, cats []string) []string {
	isCat := map[string]bool{}
	for _, c := range cats {
		isCat[strings.ToLower(c)] = true
	}
	var out []string
	for _, o := range only {
		if !isCat[strings.ToLower(o)] {
			out = append(out, o)
		}
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// otherFilters asks for name patterns, sizes and dates, each optional and each
// checked as it is typed, with its format shown up front.
func (w *flow) otherFilters() error {
	skip := strings.Join(w.j.Skip, ", ")
	minS, maxS := sizeText(w.j.MinSize), sizeText(w.j.MaxSize)
	since, until := w.j.Since, w.j.Until
	sizeOK := func(s string) error { _, err := job.ParseSize(s); return err }
	dateOK := func(s string) error {
		if s == "" {
			return nil
		}
		return (&job.Job{Sources: []string{"/"}, Target: "/x", Since: s}).ValidateDates()
	}
	err := Form(true,
		huh.NewInput().Title("Skip names matching").Description("Comma-separated patterns, for files and folders.").Placeholder("e.g. *.tmp, node_modules, .git").Value(&skip),
		huh.NewInput().Title("Smallest size").Placeholder("e.g. 10KB (empty for no limit)").Validate(sizeOK).Value(&minS),
		huh.NewInput().Title("Largest size").Placeholder("e.g. 200MB (empty for no limit)").Validate(sizeOK).Value(&maxS),
		huh.NewInput().Title("Modified on or after").Placeholder("YYYY-MM-DD (empty for any)").Validate(dateOK).Value(&since),
		huh.NewInput().Title("Modified on or before").Placeholder("YYYY-MM-DD (empty for any)").Validate(dateOK).Value(&until),
	)
	if err != nil {
		return err
	}
	w.j.Skip = splitList(skip)
	w.j.MinSize, _ = job.ParseSize(minS)
	w.j.MaxSize, _ = job.ParseSize(maxS)
	w.j.Since, w.j.Until = strings.TrimSpace(since), strings.TrimSpace(until)
	return nil
}

func sizeText(n int64) string {
	if n <= 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

func (w *flow) duplicates() (step, error) {
	var picked []string
	opts := []huh.Option[string]{
		huh.NewOption("Identical files — the same contents, whatever the name", job.Identical).Selected(w.j.Finds(job.Identical)),
		huh.NewOption("Smaller copies of pictures — the largest is kept", job.Pictures).Selected(w.j.Finds(job.Pictures)),
		huh.NewOption("Near-duplicate documents — revisions; the newest is kept", job.Documents).Selected(w.j.Finds(job.Documents)),
		huh.NewOption("Let me review the sets before anything is skipped", "review").Selected(w.j.Review),
	}
	err := Form(true, huh.NewMultiSelect[string]().
		Title("Look for duplicates?").
		Description("Nothing chosen copies everything. Pictures: JPEG, PNG, GIF, BMP, TIFF,\nWebP, and HEIC on a Mac. RAW files are only ever matched when identical.").
		Options(opts...).Value(&picked))
	if err != nil {
		return 0, err
	}
	w.j.Duplicates, w.j.Review = nil, false
	for _, p := range picked {
		if p == "review" {
			w.j.Review = true
		} else {
			w.j.Duplicates = append(w.j.Duplicates, p)
		}
	}
	return stepDocuments, nil
}

func (w *flow) documents() (step, error) {
	var picked []string
	err := Form(true, huh.NewMultiSelect[string]().
		Title("Anything to do with documents?").
		Description("For documentation scrapes and NotebookLM. Nothing chosen copies files as they are.").
		Options(
			huh.NewOption("Name documents after their titles (HTML, PDF, Word, EPUB, Markdown)", "titles").Selected(w.j.RenameTitles),
			huh.NewOption("Detect types by content, and fix wrong extensions", "types").Selected(w.j.DetectTypes),
			huh.NewOption("Expand .zip, .tar and .chm files found in the source", "expand").Selected(w.j.Expand),
			huh.NewOption("Merge text documents into large files (at most 400,000 words each)", "merge").Selected(w.j.MergeText),
			huh.NewOption("Split the result into batch folders", "batch").Selected(w.j.Batch > 0),
		).Value(&picked))
	if err != nil {
		return 0, err
	}
	on := map[string]bool{}
	for _, p := range picked {
		on[p] = true
	}
	w.j.RenameTitles, w.j.DetectTypes, w.j.Expand, w.j.MergeText = on["titles"], on["types"], on["expand"], on["merge"]
	if !on["batch"] {
		w.j.Batch = 0
		return stepOptions, nil
	}
	size := strconv.Itoa(max(w.j.Batch, 50))
	if err := Menu(huh.NewSelect[string]().
		Title("How many files per batch?").
		Description("A NotebookLM notebook holds 50 sources on the free plan, more on paid plans.").
		Options(
			huh.NewOption("50 — NotebookLM's free plan", "50"),
			huh.NewOption("100", "100"),
			huh.NewOption("300", "300"),
			huh.NewOption("600", "600"),
		).Value(&size), true); err != nil {
		return 0, err
	}
	w.j.Batch, _ = strconv.Atoi(size)
	return stepOptions, nil
}

func (w *flow) options() (step, error) {
	var picked []string
	err := Form(true, huh.NewMultiSelect[string]().
		Title("Options").
		Options(
			huh.NewOption("Save a report in the superdirectory's .superdirectory folder", "report").Selected(!w.j.NoReport),
			huh.NewOption("Retry failed files once at the end", "retry").Selected(w.j.Retries > 0),
			huh.NewOption("Verify every copy by reading it back (slower)", "verify").Selected(w.j.Verify),
			huh.NewOption("Notify me when the copy finishes", "notify").Selected(w.j.Notify),
		).Value(&picked))
	if err != nil {
		return 0, err
	}
	on := map[string]bool{}
	for _, p := range picked {
		on[p] = true
	}
	w.j.NoReport, w.j.Verify, w.j.Notify = !on["report"], on["verify"], on["notify"]
	w.j.Retries = 0
	if on["retry"] {
		w.j.Retries = 1
	}
	return stepConfirm, nil
}

// confirm shows the whole job and copies, saves it as a preset, steps back, or
// cancels.
func (w *flow) confirm() (step, error) {
	for {
		choice := "copy"
		desc := Summary(w.j)
		if w.note != "" {
			desc = w.note + "\n\n" + desc
			w.note = ""
		}
		err := Menu(huh.NewSelect[string]().
			Title("Ready to copy?").
			Description(desc).
			Options(
				huh.NewOption("Copy", "copy"),
				huh.NewOption("Save these settings as a preset…", "save"),
				huh.NewOption("Go back", "back"),
				huh.NewOption("Cancel", "cancel"),
			).Value(&choice), true)
		if err != nil {
			return 0, err
		}
		switch choice {
		case "copy":
			return stepDone, nil
		case "back":
			return 0, errBack
		case "cancel":
			return 0, huh.ErrUserAborted
		case "save":
			name := ""
			err := Form(true, huh.NewInput().
				Title("Name the preset").
				Description("Letters, digits, spaces, '-', '_' and '.'.").
				Validate(job.ValidPresetName).
				Value(&name))
			if errors.Is(err, errBack) {
				continue
			}
			if err != nil {
				return 0, err
			}
			if err := job.SavePreset(name, w.j); err != nil {
				w.note = "Could not save the preset: " + err.Error()
			} else {
				w.note = "Saved preset " + strings.TrimSpace(name) + "."
			}
		}
	}
}

// Summary describes a job in a few lines, for the confirm screen.
func Summary(j job.Job) string {
	var b strings.Builder
	line := func(label, value string) { fmt.Fprintf(&b, "%-11s %s\n", label+":", value) }
	from := j.Sources[0]
	if len(j.Sources) > 1 {
		from += fmt.Sprintf(" and %d more", len(j.Sources)-1)
	}
	line("From", from)
	line("To", j.Target)
	switch j.LayoutOrFlat() {
	case job.ByType:
		if j.KeepFolders {
			line("Layout", "by type, keeping the original folders")
		} else {
			line("Layout", "by type")
		}
	case job.ByDate:
		line("Layout", "by date taken (year/month)")
	case job.ByDepth:
		line("Layout", fmt.Sprintf("keep the top %d level(s) of folders", j.Depth))
	default:
		line("Layout", "flat")
	}
	switch n := len(j.Excluded); n {
	case 0:
		line("Excluding", "nothing")
	case 1:
		line("Excluding", "1 folder")
	default:
		line("Excluding", fmt.Sprintf("%d folders", n))
	}
	var filters []string
	if len(j.Only) > 0 {
		filters = append(filters, "only "+strings.Join(j.Only, ", "))
	}
	if len(j.Not) > 0 {
		filters = append(filters, "not "+strings.Join(j.Not, ", "))
	}
	if len(j.Skip) > 0 {
		filters = append(filters, "skip "+strings.Join(j.Skip, ", "))
	}
	if j.MinSize > 0 || j.MaxSize > 0 {
		filters = append(filters, "size limits")
	}
	if j.Since != "" || j.Until != "" {
		filters = append(filters, "date range")
	}
	if len(filters) > 0 {
		line("Filters", strings.Join(filters, "; "))
	}
	if len(j.Duplicates) > 0 {
		d := strings.Join(j.Duplicates, ", ")
		if j.Review {
			d += " (review first)"
		}
		line("Duplicates", d)
	}
	var docs []string
	if j.RenameTitles {
		docs = append(docs, "titles")
	}
	if j.DetectTypes {
		docs = append(docs, "detect types")
	}
	if j.Expand {
		docs = append(docs, "expand archives")
	}
	if j.MergeText {
		docs = append(docs, "merge text")
	}
	if j.Batch > 0 {
		docs = append(docs, fmt.Sprintf("batches of %d", j.Batch))
	}
	if len(docs) > 0 {
		line("Documents", strings.Join(docs, ", "))
	}
	var opts []string
	if !j.NoReport {
		opts = append(opts, "report")
	}
	if j.Retries > 0 {
		opts = append(opts, "retry once")
	}
	if j.Verify {
		opts = append(opts, "verify")
	}
	if j.Notify {
		opts = append(opts, "notify")
	}
	if len(opts) > 0 {
		line("Options", strings.Join(opts, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// askLayout asks whether the original folder nesting survives inside each
// extension folder. Type layout only.
func askLayout(current bool) (bool, error) {
	choice := "pool"
	if current {
		choice = "keep"
	}
	err := Menu(huh.NewSelect[string]().
		Title("Inside each type folder, keep the original folders?").
		Description("No:   Documents/pdf/q3.pdf\nYes:  Documents/pdf/Work/Invoices/q3.pdf").
		Options(
			huh.NewOption("No — pool every file of a type together", "pool"),
			huh.NewOption("Yes — group by the folder it came from", "keep"),
			huh.NewOption("Go back", "back"),
		).
		Value(&choice), true)
	if errors.Is(err, errBack) || choice == "back" {
		return current, errBack
	}
	if err != nil {
		return current, huh.ErrUserAborted // ctrl+c
	}
	return choice == "keep", nil
}

func askSource() (string, error) {
	s, err := pick.Run(pick.Options{
		Title: "Choose a folder to copy from  (open with →, then press enter)",
		Start: home(),
	})
	if err != nil {
		if errors.Is(err, pick.ErrBack) {
			return "", errBack
		}
		if errors.Is(err, pick.ErrCanceled) {
			return "", huh.ErrUserAborted
		}
		return "", err
	}
	return s, nil
}

func askDestination(sources []string, prevTarget string) (string, error) {
	// Default to saving alongside the first source. If the user already chose a
	// target and stepped back, reopen where they left off instead of resetting.
	start := filepath.Dir(sources[0])
	nameDefault := freeName(start, safeBase(sources[0])+"-super")
	if prevTarget != "" {
		start = filepath.Dir(prevTarget)
		nameDefault = filepath.Base(prevTarget)
	}
	target, err := pick.Run(pick.Options{
		Title:       "Choose where to save the superdirectory  (open with →, then press enter)",
		Start:       start,
		NameEntry:   true,
		NameDefault: nameDefault,
		Validate: func(parent, name string) error {
			if err := validName(name); err != nil {
				return err
			}
			target := filepath.Join(parent, strings.TrimSpace(name))
			// Refuse a destination that overlaps a source, which would copy the
			// tree into itself.
			if overlapsAny(target, sources) {
				return errors.New("that overlaps a source folder; pick another spot")
			}
			return checkTarget(target)
		},
	})
	if errors.Is(err, pick.ErrBack) {
		return "", errBack
	}
	if errors.Is(err, pick.ErrCanceled) {
		return "", huh.ErrUserAborted
	}
	if err != nil {
		return "", err
	}
	return target, nil
}

// Theme is the shared huh theme: Charm's layout with the app's cyan accent
// (#00b4d8) and green confirmations in place of the default indigo/fuchsia,
// which read poorly on many terminals.
func Theme() *huh.Theme {
	t := huh.ThemeCharm()

	cyan := lipgloss.Color("#00b4d8")
	green := lipgloss.Color("#2ecc71")
	cream := lipgloss.Color("#FFFDF5")
	desc := lipgloss.Color("245")

	f := &t.Focused
	f.Title = f.Title.Foreground(cyan).Bold(true)
	f.NoteTitle = f.NoteTitle.Foreground(cyan).Bold(true)
	f.Directory = f.Directory.Foreground(cyan)
	f.Description = f.Description.Foreground(desc)
	f.SelectSelector = f.SelectSelector.Foreground(cyan)
	f.NextIndicator = f.NextIndicator.Foreground(cyan)
	f.PrevIndicator = f.PrevIndicator.Foreground(cyan)
	f.MultiSelectSelector = f.MultiSelectSelector.Foreground(cyan)
	f.SelectedOption = f.SelectedOption.Foreground(green)
	f.SelectedPrefix = f.SelectedPrefix.Foreground(green)
	f.FocusedButton = f.FocusedButton.Foreground(cream).Background(cyan).Bold(true)
	f.Next = f.FocusedButton
	f.TextInput.Cursor = f.TextInput.Cursor.Foreground(green)
	f.TextInput.Prompt = f.TextInput.Prompt.Foreground(cyan)

	// Mirror the focused styles onto blurred, keeping the hidden border.
	t.Blurred = t.Focused
	t.Blurred.Base = t.Blurred.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()

	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description
	return t
}

// ── helpers ──────────────────────────────────────────────────────────────

// topLevelSubdirCount reports how many copyable subdirectories source has at its
// top level: enough to decide whether the exclusion screen is worth showing, and
// what number to put in its description.
//
// It reads source once and opens nothing beneath it. The version this replaced
// built a []subdir carrying per-directory file and folder counts that no caller
// ever read, at the price of one directory read per subdirectory — on an external
// drive, a bus round trip each, paid before the exclusion screen would even
// appear. A drive root with 120 folders cost 121 reads to render one integer.
func topLevelSubdirCount(source string) int {
	entries, err := os.ReadDir(source)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		// A drive root whose only subdirectory is .fseventsd has nothing worth
		// excluding, so the exclusion screen must not appear for it.
		if e.IsDir() && e.Type()&os.ModeSymlink == 0 && !fsmeta.IsMetadata(e.Name()) {
			n++
		}
	}
	return n
}

// checkTarget refuses a destination that already holds files. The screen promises
// a new folder ("Name the new folder", "Creates …"). Copying into a populated one
// would replace, without a word, every file there that shares a name with one in the
// plan. An empty folder is fine, and so is one holding only filesystem bookkeeping
// such as .DS_Store.
func checkTarget(target string) error {
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("a file already has that name here; choose another name")
	}
	if engine.Interrupted(target) {
		return nil // a copy that never finished: the destination step offers to resume it
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !fsmeta.IsMetadata(e.Name()) {
			return errors.New("that folder already exists and is not empty; choose another name")
		}
	}
	return nil
}

// freeName returns base, or base-2, base-3, … — the first that checkTarget accepts
// inside dir. Running the same source twice then offers "photos-super-2" rather
// than a name that is refused on enter.
func freeName(dir, base string) string {
	name := base
	for i := 2; i < 1000; i++ {
		if checkTarget(filepath.Join(dir, name)) == nil {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return base // give up suggesting; the check on enter still applies
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func validName(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." || strings.ContainsRune(s, filepath.Separator) {
		return errors.New("invalid name: avoid empty, '.', '..', or path separators")
	}
	return nil
}

// safeBase returns a usable folder-name stem for dir, falling back when dir is
// the filesystem root (filepath.Base("/") == "/") or otherwise nameless.
func safeBase(dir string) string {
	b := filepath.Base(dir)
	if b == "." || b == ".." || b == string(filepath.Separator) || strings.TrimSpace(b) == "" {
		return "flattened"
	}
	return b
}

// overlaps reports whether a and b are the same directory or one contains the
// other. The comparison is case-insensitive: on macOS (APFS) and Windows two
// paths that differ only in case name the same directory, and treating them as
// distinct would let the copy write into its own source and clobber files. On a
// truly case-sensitive volume this only ever over-blocks two same-named-but-
// different-cased siblings, which is a safe direction for this guard.
func overlaps(a, b string) bool {
	return within(a, b) || within(b, a)
}

// within reports whether child is at or beneath parent.
func within(child, parent string) bool {
	child = strings.ToLower(filepath.Clean(child))
	parent = strings.ToLower(filepath.Clean(parent))
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	// rel escapes parent only if it is "..", or starts with "../".
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
