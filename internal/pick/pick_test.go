package pick

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"apple/a1", "apple/a2", "banana", "cherry/c1"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A stray file at the top level must be ignored by the browser.
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestModel(dir string, opts Options) *model {
	opts.Start = dir
	m := &model{opts: opts, dir: dir, height: 15}
	m.load()
	return m
}

func send(m *model, msg tea.Msg) *model {
	next, _ := m.Update(msg)
	return next.(*model)
}

func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }
func runeKey(r rune) tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func names(m *model) []string {
	out := make([]string, len(m.items))
	for i, it := range m.items {
		out[i] = it.name
	}
	return out
}

func TestLoadsSortedDirsOnly(t *testing.T) {
	m := newTestModel(fixture(t), Options{})
	got := names(m)
	want := []string{"apple", "banana", "cherry"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v (files must be excluded)", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestDescendAndAscend(t *testing.T) {
	root := fixture(t)
	m := newTestModel(root, Options{})

	m.cursor = 0 // apple
	m = send(m, key(tea.KeyRight))
	if filepath.Base(m.dir) != "apple" {
		t.Fatalf("right should descend into apple, dir=%s", m.dir)
	}
	if got := names(m); len(got) != 2 || got[0] != "a1" || got[1] != "a2" {
		t.Fatalf("inside apple want [a1 a2], got %v", got)
	}

	m = send(m, key(tea.KeyLeft))
	if m.dir != root {
		t.Fatalf("left should return to root, dir=%s", m.dir)
	}
	if m.items[m.cursor].name != "apple" {
		t.Errorf("cursor should land back on apple, got %q", m.items[m.cursor].name)
	}
}

func TestTypeToJump(t *testing.T) {
	m := newTestModel(fixture(t), Options{})
	m = send(m, runeKey('c'))
	if m.items[m.cursor].name != "cherry" {
		t.Errorf("typing c should jump to cherry, got %q", m.items[m.cursor].name)
	}
	m = send(m, runeKey('a')) // wraps back around to apple
	if m.items[m.cursor].name != "apple" {
		t.Errorf("typing a should wrap to apple, got %q", m.items[m.cursor].name)
	}
	// Typing must never leave browse mode for a text field.
	if m.naming {
		t.Error("type-to-jump must not enter name-entry mode")
	}
}

func TestChooseCurrentFolder(t *testing.T) {
	root := fixture(t)
	m := newTestModel(root, Options{}) // no name entry
	m = send(m, key(tea.KeyEnter))
	if m.result != root {
		t.Errorf("enter should choose the current folder %q, got %q", root, m.result)
	}
}

func TestNameEntryFlow(t *testing.T) {
	root := fixture(t)
	m := newTestModel(root, Options{NameEntry: true, NameDefault: "out-super"})

	m = send(m, key(tea.KeyEnter)) // choose parent -> enter naming
	if !m.naming {
		t.Fatal("enter with NameEntry should open the name field")
	}
	m = send(m, key(tea.KeyEnter)) // accept default name
	want := filepath.Join(root, "out-super")
	if m.result != want {
		t.Errorf("want result %q, got %q", want, m.result)
	}
}

func TestNameEntryValidationBlocks(t *testing.T) {
	root := fixture(t)
	blocked := true
	m := newTestModel(root, Options{
		NameEntry:   true,
		NameDefault: "bad",
		Validate: func(parent, name string) error {
			if blocked {
				return os.ErrInvalid
			}
			return nil
		},
	})
	m = send(m, key(tea.KeyEnter)) // enter naming
	m = send(m, key(tea.KeyEnter)) // try to confirm -> rejected
	if m.result != "" {
		t.Errorf("validation failure should not set a result, got %q", m.result)
	}
	if m.errMsg == "" {
		t.Error("validation failure should surface an error message")
	}
	if !m.naming {
		t.Error("should stay in name entry after a validation failure")
	}
}

func TestEscBacksOutOfNamingThenPicker(t *testing.T) {
	m := newTestModel(fixture(t), Options{NameEntry: true, NameDefault: "x"})
	m = send(m, key(tea.KeyEnter)) // into naming
	m = send(m, key(tea.KeyEsc))   // esc returns to browsing, not out
	if m.naming {
		t.Error("esc in name entry should return to browsing")
	}
	if m.back || m.canceled {
		t.Error("esc in name entry should not leave the picker")
	}
	m = send(m, key(tea.KeyEsc)) // esc in browse steps back
	if !m.back {
		t.Error("esc in browse should request a step back")
	}
}

func TestQDoesNotQuitAndCtrlCDoes(t *testing.T) {
	// q must stay free for type-to-jump, not abort the wizard.
	m := newTestModel(fixture(t), Options{})
	m = send(m, runeKey('q'))
	if m.canceled {
		t.Error("q must not cancel; it is a jump key")
	}

	m2 := newTestModel(fixture(t), Options{})
	m2 = send(m2, key(tea.KeyCtrlC))
	if !m2.canceled {
		t.Error("ctrl+c should cancel")
	}
}

func TestUnreadableDirCannotBeChosen(t *testing.T) {
	m := newTestModel(fixture(t), Options{})
	m.dir = filepath.Join(m.dir, "does-not-exist")
	m.load()
	if m.loadErr == nil {
		t.Fatal("load should record an error for an unreadable directory")
	}
	m = send(m, key(tea.KeyEnter))
	if m.result != "" {
		t.Errorf("enter must not choose an unreadable folder, got %q", m.result)
	}
}

func TestEmptyNameRejectedWithoutValidate(t *testing.T) {
	root := fixture(t)
	m := newTestModel(root, Options{NameEntry: true, NameDefault: "x"})
	m = send(m, key(tea.KeyEnter))     // into naming
	m = send(m, key(tea.KeyBackspace)) // delete the single default char -> empty
	m = send(m, key(tea.KeyEnter))     // try to commit an empty name
	if m.result != "" {
		t.Errorf("empty name must not commit, got %q", m.result)
	}
	if m.errMsg == "" {
		t.Error("empty name should surface an error")
	}
}

// buildWideTree makes a directory with n subdirectories, each holding one
// sub-subdirectory so that a subfolder count is non-zero once taken.
func buildWideTree(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("dir-%03d", i), "child"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func counted(m *model) int {
	n := 0
	for _, it := range m.items {
		if it.subdirs != subdirsUnknown {
			n++
		}
	}
	return n
}

// TestLoadCountsOnlyVisibleRows is a performance regression test with teeth.
// load() used to read every child directory to label it "(N subfolders)": entering
// a folder with 120 children issued 121 directory reads. Locally that is
// microseconds; over USB each read is a bus round trip, and navigation crawled.
//
// Work must now be bounded by the terminal height, not the directory size.
func TestLoadCountsOnlyVisibleRows(t *testing.T) {
	const total, height = 120, 15
	root := buildWideTree(t, total)

	m := &model{dir: root, height: height}
	m.load()

	if len(m.items) != total {
		t.Fatalf("expected %d entries, got %d", total, len(m.items))
	}
	if got := counted(m); got != height {
		t.Errorf("counted %d rows after load, want exactly the %d visible ones "+
			"— a directory read per child is what made USB drives crawl", got, height)
	}
	// Rows below the fold must still be unknown, not silently zero: a zero would
	// render as "no subfolders" for a folder that has them.
	if m.items[total-1].subdirs != subdirsUnknown {
		t.Errorf("offscreen row was counted eagerly: %d", m.items[total-1].subdirs)
	}
	// The visible ones must carry the real count, not a placeholder.
	if m.items[0].subdirs != 1 {
		t.Errorf("visible row should report its 1 subfolder, got %d", m.items[0].subdirs)
	}
}

// TestScrollingFillsAndMemoizes: revealed rows get counted once, and re-visiting
// a directory costs nothing.
func TestScrollingFillsAndMemoizes(t *testing.T) {
	const total, height = 60, 10
	root := buildWideTree(t, total)

	m := &model{dir: root, height: height}
	m.load()
	if got := counted(m); got != height {
		t.Fatalf("after load: counted %d, want %d", got, height)
	}

	// Scroll to the bottom; newly revealed rows fill in.
	m.cursor = total - 1
	m.clampScroll()
	if m.items[total-1].subdirs != 1 {
		t.Errorf("row scrolled into view was not counted")
	}

	// Every path we ever counted is memoized, so re-entering is free.
	if len(m.counts) == 0 {
		t.Fatal("expected the count cache to be populated")
	}
	before := len(m.counts)
	m.load() // re-enter the same directory
	if got := counted(m); got != height {
		t.Errorf("re-entry counted %d rows, want %d", got, height)
	}
	if len(m.counts) != before {
		t.Errorf("re-entry re-read directories: cache grew %d -> %d", before, len(m.counts))
	}
}
