// Package wizard is the interactive layer. It walks the user through mode,
// source, destination, exclusions, layout, and a final confirm, and returns a
// plain Result. It does no file copying — that belongs to package flatten.
//
// The flow is a small step machine: every screen can move forward or step back,
// so the user is never trapped. Two screens are conditional — exclusions appear
// only when the source has subdirectories, and layout only in ModeOrganize — so
// stepping backwards must skip whatever was never shown.
//
// Directory selection uses package pick (a keyboard browser) rather than raw
// text prompts; every other screen uses huh.
package wizard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/ozzyphantom/SuperDirectory/internal/exclude"
	"github.com/ozzyphantom/SuperDirectory/internal/fsmeta"
	"github.com/ozzyphantom/SuperDirectory/internal/organize"
	"github.com/ozzyphantom/SuperDirectory/internal/pick"
)

// Mode selects which planner builds the superdirectory.
type Mode int

const (
	// ModeFlatten collapses the tree into a single folder (package flatten).
	ModeFlatten Mode = iota
	// ModeOrganize sorts files into Category/extension folders (package organize).
	ModeOrganize
)

// Result is the fully-resolved plan input the core needs.
type Result struct {
	Source   string
	Target   string
	Excluded map[string]bool // absolute paths of subdirectories to skip
	Mode     Mode

	// KeepSourceTree applies to ModeOrganize only: recreate each file's
	// original folder nesting inside its extension folder.
	KeepSourceTree bool

	// FindDuplicates asks the copier's caller to look for byte-identical files in
	// the source before copying, and offer to skip the copies.
	FindDuplicates bool
}

// errBack is an internal sentinel: a step is asking to return to the previous
// one. It never escapes Run.
var errBack = errors.New("back a step")

// IsAbort reports whether err is a user-initiated cancel (Ctrl+C / q).
func IsAbort(err error) bool {
	return errors.Is(err, huh.ErrUserAborted)
}

// Run drives the full interactive flow and returns the resolved Result, or an
// error (use IsAbort to detect a clean cancel).
func Run() (*Result, error) {
	var (
		mode           Mode
		source         string
		target         string
		excluded       = map[string]bool{}
		hasSubs        bool
		subCount       int
		keepSourceTree bool
		findDuplicates bool
	)

	const (
		stepMode = iota
		stepSource
		stepDest
		stepExclude
		stepLayout
		stepDuplicates
		stepConfirm
	)

	// prev returns the step to land on when moving backwards from stepConfirm or
	// stepLayout, skipping the screens this run never showed.
	prevBeforeLayout := func() int {
		if hasSubs {
			return stepExclude
		}
		return stepDest
	}

	step := stepMode
	for {
		switch step {
		case stepMode:
			m, err := askMode(mode)
			if err != nil {
				return nil, err // first screen: back and cancel are both a cancel
			}
			mode = m
			step = stepSource

		case stepSource:
			s, err := askSource()
			if errors.Is(err, errBack) {
				step = stepMode
				continue
			}
			if err != nil {
				return nil, err
			}
			if s != source {
				// A new source invalidates choices tied to the old one.
				excluded = map[string]bool{}
				target = ""
			}
			source = s
			subCount = topLevelSubdirCount(source)
			hasSubs = subCount > 0
			step = stepDest

		case stepDest:
			t, err := askDestination(source, target)
			if errors.Is(err, errBack) {
				step = stepSource
				continue
			}
			if err != nil {
				return nil, err
			}
			target = t
			step = stepExclude

		case stepExclude:
			if !hasSubs {
				excluded = map[string]bool{}
				step = stepLayout
				continue
			}
			ex, err := askExclusions(source, excluded, subCount)
			if errors.Is(err, errBack) {
				step = stepDest
				continue
			}
			if err != nil {
				return nil, err
			}
			excluded = ex
			step = stepLayout

		case stepLayout:
			if mode != ModeOrganize {
				step = stepDuplicates
				continue
			}
			keep, err := askLayout(keepSourceTree)
			if errors.Is(err, errBack) {
				step = prevBeforeLayout()
				continue
			}
			if err != nil {
				return nil, err
			}
			keepSourceTree = keep
			step = stepDuplicates

		case stepDuplicates:
			find, err := askDuplicates(findDuplicates)
			if errors.Is(err, errBack) {
				if mode == ModeOrganize {
					step = stepLayout
				} else {
					step = prevBeforeLayout()
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			findDuplicates = find
			step = stepConfirm

		case stepConfirm:
			dec, err := confirm(mode, source, target, excluded, keepSourceTree, findDuplicates)
			if err != nil {
				return nil, err
			}
			switch dec {
			case decCopy:
				return &Result{
					Source:         source,
					Target:         target,
					Excluded:       excluded,
					Mode:           mode,
					KeepSourceTree: mode == ModeOrganize && keepSourceTree,
					FindDuplicates: findDuplicates,
				}, nil
			case decBack:
				step = stepDuplicates
			case decCancel:
				return nil, huh.ErrUserAborted
			}
		}
	}
}

// askMode is the first screen: which kind of superdirectory to build.
func askMode(current Mode) (Mode, error) {
	choice := "flatten"
	if current == ModeOrganize {
		choice = "organize"
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("What kind of superdirectory?").
			Description("Flatten pools every file in one folder.\nOrganize sorts them into "+strings.Join(organize.Categories(), ", ")+", and Other.").
			Options(
				huh.NewOption("Flatten — one folder, every file", "flatten"),
				huh.NewOption("Organize by file type — a folder per type", "organize"),
			).
			Value(&choice),
	)).WithTheme(Theme())
	if err := form.Run(); err != nil {
		return current, huh.ErrUserAborted // ctrl+c
	}
	if choice == "organize" {
		return ModeOrganize, nil
	}
	return ModeFlatten, nil
}

// askLayout asks whether the original folder nesting survives inside each
// extension folder. Organize mode only.
func askLayout(current bool) (bool, error) {
	choice := "pool"
	if current {
		choice = "keep"
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Inside each type folder, keep the original folders?").
			Description("No:   Documents/pdf/q3.pdf\nYes:  Documents/pdf/Work/Invoices/q3.pdf").
			Options(
				huh.NewOption("No — pool every file of a type together", "pool"),
				huh.NewOption("Yes — group by the folder it came from", "keep"),
				huh.NewOption("Go back", "back"),
			).
			Value(&choice),
	)).WithTheme(Theme())
	if err := form.Run(); err != nil {
		return current, huh.ErrUserAborted // ctrl+c
	}
	switch choice {
	case "back":
		return current, errBack
	case "keep":
		return true, nil
	default:
		return false, nil
	}
}

func askSource() (string, error) {
	s, err := pick.Run(pick.Options{
		Title: "Choose the folder to use as the source  (open with →, then press enter)",
		Start: home(),
	})
	if err != nil {
		if errors.Is(err, pick.ErrBack) {
			return "", errBack // esc returns to the mode screen
		}
		if errors.Is(err, pick.ErrCanceled) {
			return "", huh.ErrUserAborted
		}
		return "", err
	}
	return s, nil
}

func askDestination(source, prevTarget string) (string, error) {
	// Default to saving alongside the source. If the user already chose a
	// target and stepped back, reopen where they left off instead of resetting.
	start := filepath.Dir(source)
	nameDefault := safeBase(source) + "-super"
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
			// Guard the Python version lacks: refuse a destination that
			// overlaps the source, which would copy the tree into itself.
			if overlaps(target, source) {
				return errors.New("that sits inside the source folder; pick another spot")
			}
			return nil
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

func askExclusions(source string, current map[string]bool, subCount int) (map[string]bool, error) {
	empty := map[string]bool{}

	for {
		var choice string
		form := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Exclude any subdirectories?").
				Description(fmt.Sprintf("%d at the top level. You can descend to any depth.", subCount)).
				Options(
					huh.NewOption("No — copy everything", "no"),
					huh.NewOption("Yes — choose what to skip", "yes"),
					huh.NewOption("Go back", "back"),
				).
				Value(&choice),
		)).WithTheme(Theme())
		if err := form.Run(); err != nil {
			return nil, err // ctrl+c
		}

		switch choice {
		case "no":
			return empty, nil
		case "back":
			return nil, errBack
		case "yes":
			ex, err := exclude.Run(source, current)
			switch {
			case errors.Is(err, exclude.ErrBack):
				continue // esc in the tree returns to this menu
			case errors.Is(err, exclude.ErrCanceled):
				return nil, huh.ErrUserAborted // q / ctrl+c quits
			case err != nil:
				return nil, err
			}
			return ex, nil
		}
	}
}

type decision int

const (
	decCopy decision = iota
	decBack
	decCancel
)

func confirm(mode Mode, source, target string, excluded map[string]bool, keepSourceTree, findDuplicates bool) (decision, error) {
	summary := fmt.Sprintf("From:  %s\nTo:    %s\n", source, target)
	if mode == ModeOrganize {
		summary += "Mode:  organize by file type\n"
		if keepSourceTree {
			summary += "       (keeping original folders inside each type)\n"
		}
	} else {
		summary += "Mode:  flatten into one folder\n"
	}
	switch n := len(excluded); {
	case n == 0:
		summary += "Excluding: nothing"
	case n == 1:
		summary += "Excluding: 1 directory"
	default:
		summary += fmt.Sprintf("Excluding: %d directories", n)
	}
	if findDuplicates {
		summary += "\nDuplicates: scan before copying"
	}

	var choice string
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Ready to copy?").
			Description(summary).
			Options(
				huh.NewOption("Copy", "copy"),
				huh.NewOption("Go back", "back"),
				huh.NewOption("Cancel", "cancel"),
			).
			Value(&choice),
	)).WithTheme(Theme())
	if err := form.Run(); err != nil {
		return decCancel, huh.ErrUserAborted // ctrl+c
	}
	switch choice {
	case "copy":
		return decCopy, nil
	case "back":
		return decBack, nil
	default:
		return decCancel, huh.ErrUserAborted
	}
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

// askDuplicates offers the content-identity scan. It is opt-in because it costs
// reads: files sharing a byte size have to be hashed, and on an external drive that
// is the expensive thing. Files with a unique size are never opened.
func askDuplicates(current bool) (bool, error) {
	choice := "no"
	if current {
		choice = "yes"
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Look for duplicate files first?").
			Description("Finds files with identical contents, whatever they are named,\nand offers to copy one of each instead of all of them.\nOnly files sharing a byte size are read.").
			Options(
				huh.NewOption("No — copy everything", "no"),
				huh.NewOption("Yes — find identical files", "yes"),
				huh.NewOption("Go back", "back"),
			).
			Value(&choice),
	)).WithTheme(Theme())
	if err := form.Run(); err != nil {
		return current, huh.ErrUserAborted // ctrl+c
	}
	switch choice {
	case "back":
		return current, errBack
	case "yes":
		return true, nil
	default:
		return false, nil
	}
}
