// Package ui is how SuperDirectory talks to a person: the progress lines, the copy
// screen with its pause key, the questions a run asks, and the summary at the end.
// The wizard's front end and the command line's both live here, over the same
// progress line, so a copy looks the same however it was started.
package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

var (
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("#2ecc71"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("#e74c3c"))
	orange = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	bold   = lipgloss.NewStyle().Bold(true)
	keyFmt = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
)

// Styles the main package shares for its few lines of its own.
var (
	Cyan = cyan
	Dim  = dim
	Key  = keyFmt
	Red  = red
)

// termWidth is the usable width of the terminal: one column short of the edge, so a
// full line never leaves the cursor in the wrap position. 79 when it cannot tell.
func termWidth() int {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil || w <= 1 {
		return 79
	}
	return w - 1
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

// HumanBytes formats a byte count in decimal units, matching how drive and
// transfer speeds are quoted.
func HumanBytes(n int64) string {
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

// count renders n with its noun, agreeing in number: "1 file", "3 files".
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%s %ss", thousands(n), noun)
}

// thousands groups digits so 11041 reads as 11,041.
func thousands(n int) string {
	s := fmt.Sprint(n)
	if n < 0 || len(s) <= 3 {
		return s
	}
	var b []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b = append(b, ',')
		}
		b = append(b, c)
	}
	return string(b)
}
