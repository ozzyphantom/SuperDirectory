package hint

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var treeKeys = []Pair{
	{"↑↓", "move"}, {"→", "open"}, {"←", "collapse"}, {"space", "exclude"},
	{"p", "preview"}, {"enter", "done"}, {"esc", "back"}, {"q", "quit"},
}

func TestLinesWrapToWidth(t *testing.T) {
	for _, width := range []int{30, 50, 79, 120} {
		lines := Lines(treeKeys, width)
		joined := ansi.Strip(strings.Join(lines, " "))
		for _, p := range treeKeys {
			if !strings.Contains(joined, p.Key+" "+p.Action) {
				t.Errorf("width %d: lost hint %q", width, p.Key)
			}
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: hint line is %d columns: %q", width, w, ansi.Strip(l))
			}
		}
	}
	if n := len(Lines(treeKeys, 79)); n != 2 {
		t.Errorf("80 columns: %d lines, want 2", n)
	}
	if n := len(Lines(treeKeys, 0)); n != 1 {
		t.Errorf("unknown width: %d lines, want 1", n)
	}
}

func TestFitPathKeepsTheSpecificEnd(t *testing.T) {
	p := "/Volumes/Archive/Photos/2019/Summer"
	if got := FitPath(p, 100); got != p {
		t.Errorf("a path that fits changed: %q", got)
	}
	got := FitPath(p, 20)
	if ansi.StringWidth(got) > 20 || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "2019/Summer") {
		t.Errorf("FitPath(…, 20) = %q", got)
	}
}

func TestFitTruncatesWithAnEllipsis(t *testing.T) {
	if got := Fit("short", 10); got != "short" {
		t.Errorf("Fit changed a fitting line: %q", got)
	}
	got := Fit("a line that is far too long", 10)
	if ansi.StringWidth(got) > 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("Fit = %q", got)
	}
}
