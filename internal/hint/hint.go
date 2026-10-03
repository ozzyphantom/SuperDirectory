// Package hint renders the key hints at the foot of each screen, and fits a line of
// text to the terminal.
//
// A line wider than the terminal wraps, and Bubble Tea's renderer counts lines, not
// rows: one over-long hint or path pushes the frame down a row and leaves the screen
// misdrawn. Every screen therefore lays its text out against the width it was given.
package hint

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	keyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	actionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// Pair is one hint: a key, and what it does.
type Pair struct{ Key, Action string }

// Lines lays out the pairs in order, indented two spaces, with keys in the accent
// color and actions dimmed. A pair that would cross width starts a new line. A width
// of zero or less means unknown, and keeps everything on one line.
func Lines(pairs []Pair, width int) []string {
	const indent, gap = "  ", "   "
	var lines []string
	cur := ""
	for _, p := range pairs {
		item := keyStyle.Render(p.Key) + " " + actionStyle.Render(p.Action)
		switch {
		case cur == "":
			cur = indent + item
		case width > 0 && ansi.StringWidth(cur+gap+item) > width:
			lines = append(lines, cur)
			cur = indent + item
		default:
			cur += actionStyle.Render(gap) + item
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// Block is Lines joined into one string, one hint line per row.
func Block(pairs []Pair, width int) string {
	return strings.Join(Lines(pairs, width), "\n")
}

// Fit truncates s to width columns, ending it with an ellipsis when cut. A width
// of zero or less means unknown, and leaves s alone.
func Fit(s string, width int) string {
	if width <= 0 || ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// FitPath shortens a path to width columns by dropping its start, which is the
// least specific part: "…/Photos/2019/Summer" says more than "/Volumes/Archi…".
// A width of zero or less means unknown, and leaves the path alone.
func FitPath(path string, width int) string {
	if width <= 0 || ansi.StringWidth(path) <= width {
		return path
	}
	if width < 2 {
		return "…"
	}
	r := []rune(path)
	for len(r) > 0 && ansi.StringWidth(string(r))+1 > width {
		r = r[1:]
	}
	return "…" + string(r)
}
