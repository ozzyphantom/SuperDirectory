// Package wizard is the interactive layer. It walks the user through source,
// destination, exclusions, and a final confirm, and returns a plain Result. It
// does no file copying — that belongs to package flatten.
//
// The flow is a small step machine: every screen can move forward or step back,
// so the user is never trapped. Directory selection uses package pick (a
// keyboard browser) rather than raw text prompts; the final confirm uses huh.
package wizard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"superdirectory-spike/internal/exclude"
	"superdirectory-spike/internal/pick"
)

// Result is the fully-resolved plan input the core needs.
type Result struct {
	Source   string
	Target   string
	Excluded map[string]bool // absolute paths of subdirectories to skip
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
		source   string
		target   string
		excluded = map[string]bool{}
		hasSubs  bool
	)

	const (
		stepSource = iota
		stepDest
		stepExclude
		stepConfirm
	)

	step := stepSource
	for {
		switch step {
		case stepSource:
			s, err := askSource()
			if err != nil {
				return nil, err // cancel; back on the first step is a cancel too
			}
			if s != source {
				// A new source invalidates choices tied to the old one.
				excluded = map[string]bool{}
				target = ""
			}
			source = s
			hasSubs = topLevelHasSubdirs(source)
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
				step = stepConfirm
				continue
			}
			ex, err := askExclusions(source, excluded)
			if errors.Is(err, errBack) {
				step = stepDest
				continue
			}
			if err != nil {
				return nil, err
			}
			excluded = ex
			step = stepConfirm

		case stepConfirm:
			dec, err := confirm(source, target, excluded)
			if err != nil {
				return nil, err
			}
			switch dec {
			case decCopy:
				return &Result{Source: source, Target: target, Excluded: excluded}, nil
			case decBack:
				if hasSubs {
					step = stepExclude
				} else {
					step = stepDest
				}
			case decCancel:
				return nil, huh.ErrUserAborted
			}
		}
	}
}

func askSource() (string, error) {
	s, err := pick.Run(pick.Options{
		Title: "Choose the folder to flatten  (open with →, then press enter)",
		Start: home(),
	})
	if err != nil {
		// Both a step-back and a quit end the wizard here: source is the first
		// screen, so there is nothing behind it.
		if errors.Is(err, pick.ErrBack) || errors.Is(err, pick.ErrCanceled) {
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
		Title:       "Choose where to save the flattened copy  (open with →, then press enter)",
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

func askExclusions(source string, current map[string]bool) (map[string]bool, error) {
	subs, _ := topLevelSubdirs(source)
	empty := map[string]bool{}

	for {
		var choice string
		form := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Exclude any subdirectories?").
				Description(fmt.Sprintf("%d at the top level. You can descend to any depth.", len(subs))).
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

func confirm(source, target string, excluded map[string]bool) (decision, error) {
	summary := fmt.Sprintf("From:  %s\nTo:    %s\n", source, target)
	switch n := len(excluded); {
	case n == 0:
		summary += "Excluding: nothing"
	case n == 1:
		summary += "Excluding: 1 directory"
	default:
		summary += fmt.Sprintf("Excluding: %d directories", n)
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

type subdir struct {
	name    string
	path    string
	files   int
	subdirs int
}

func topLevelSubdirs(source string) ([]subdir, error) {
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, err
	}
	var out []subdir
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		full := filepath.Join(source, e.Name())
		f, d := countChildren(full)
		out = append(out, subdir{name: e.Name(), path: full, files: f, subdirs: d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func topLevelHasSubdirs(source string) bool {
	entries, err := os.ReadDir(source)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			return true
		}
	}
	return false
}

func countChildren(dir string) (files, dirs int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs++
		} else {
			files++
		}
	}
	return files, dirs
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
