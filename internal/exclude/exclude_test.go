package exclude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"a/a1", "a/a2", "b", "c/c1"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func (m *model) find(name string) *node {
	for _, n := range m.visible {
		if n.name == name {
			return n
		}
	}
	return nil
}

func TestTopLevelLoadsSorted(t *testing.T) {
	m := newModel(fixture(t), nil)
	got := []string{}
	for _, n := range m.visible {
		got = append(got, n.name)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected top level %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestToggleAddsAndRemoves(t *testing.T) {
	m := newModel(fixture(t), nil)
	a := m.find("a")
	m.toggle(a)
	if !m.excluded[a.path] {
		t.Fatal("toggle should have excluded a")
	}
	m.toggle(a)
	if m.excluded[a.path] {
		t.Fatal("second toggle should have removed a")
	}
}

func TestExpandRevealsChildrenAndAncestorExclusion(t *testing.T) {
	m := newModel(fixture(t), nil)
	a := m.find("a")
	m.cursor = 0 // a is first
	m.expandCurrent()

	a1 := m.find("a1")
	if a1 == nil {
		t.Fatal("expand should have revealed a1")
	}
	m.toggle(a)
	if !m.effectivelyExcluded(a1) {
		t.Error("a1 should be effectively excluded via ancestor a")
	}
	// Toggling inside an excluded subtree is a no-op.
	m.toggle(a1)
	if _, ok := m.excluded[a1.path]; ok {
		t.Error("toggling a1 under excluded ancestor should be a no-op")
	}
}

func TestExcludingParentDropsRedundantChild(t *testing.T) {
	m := newModel(fixture(t), nil)
	a := m.find("a")
	m.cursor = 0
	m.expandCurrent()
	a1 := m.find("a1")

	m.toggle(a1) // exclude child first
	m.toggle(a)  // now exclude parent — child becomes redundant
	if m.excluded[a1.path] {
		t.Error("excluding parent a should drop the redundant child a1 exclusion")
	}
	if !m.excluded[a.path] {
		t.Error("a should remain excluded")
	}
	if len(m.excluded) != 1 {
		t.Errorf("expected exactly 1 explicit exclusion, got %d", len(m.excluded))
	}
}

// buildWide makes a source with n subdirectories, each containing a file and a
// nested folder, so counts and hasSubdirs are both non-trivial.
func buildWide(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		d := filepath.Join(root, fmt.Sprintf("Vendor-%03d", i))
		if err := os.MkdirAll(filepath.Join(d, "archive"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "manual.pdf"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func countedNodes(nodes []*node) int {
	n := 0
	for _, c := range nodes {
		if c.counted {
			n++
		}
	}
	return n
}

// TestLoadChildrenDefersCounts: expanding a wide folder must not read every child
// directory up front. On an external drive that was one bus round trip per child
// before a single row appeared.
func TestLoadChildrenDefersCounts(t *testing.T) {
	const total = 80
	root := &node{path: buildWide(t, total), depth: -1, expanded: true}
	loadChildren(root)

	if len(root.children) != total {
		t.Fatalf("expected %d children, got %d", total, len(root.children))
	}
	if got := countedNodes(root.children); got != 0 {
		t.Errorf("loadChildren counted %d children eagerly, want 0", got)
	}
}

// TestEnsureCountedIsIdempotent: the count is established once and reused.
func TestEnsureCountedIsIdempotent(t *testing.T) {
	root := &node{path: buildWide(t, 1), depth: -1, expanded: true}
	loadChildren(root)
	n := root.children[0]

	ensureCounted(n)
	if !n.counted || n.fileCount != 1 || n.subdirCount != 1 || !n.hasSubdirs {
		t.Fatalf("first count wrong: counted=%v files=%d dirs=%d hasSubdirs=%v",
			n.counted, n.fileCount, n.subdirCount, n.hasSubdirs)
	}
	// Poison the cached values; a second call must not re-read and overwrite them.
	n.fileCount = 99
	ensureCounted(n)
	if n.fileCount != 99 {
		t.Error("ensureCounted re-read a directory it had already counted")
	}
}

// TestPreviewFilesReadOnce: previewHeight and renderPreview both need the listing
// on every frame. Reading twice per keystroke is imperceptible locally and painful
// over USB.
func TestPreviewFilesReadOnce(t *testing.T) {
	root := &node{path: buildWide(t, 1), depth: -1, expanded: true}
	loadChildren(root)
	n := root.children[0]

	first := previewFilesOf(n)
	if len(first) != 1 || first[0] != "manual.pdf" {
		t.Fatalf("preview listing wrong: %v", first)
	}
	// Poison the cache; a second call must return it untouched rather than re-read.
	n.previewFiles = []string{"sentinel"}
	if got := previewFilesOf(n); len(got) != 1 || got[0] != "sentinel" {
		t.Errorf("previewFilesOf re-read the directory, got %v", got)
	}
}

// TestPreviewHeightMatchesRender: the reserved height must equal the lines drawn,
// or the help line falls off the bottom of the screen.
func TestPreviewHeightMatchesRender(t *testing.T) {
	root := &node{path: buildWide(t, 1), depth: -1, expanded: true}
	loadChildren(root)
	n := root.children[0]

	m := &model{root: root, source: root.path, height: 15, preview: n}
	want := m.previewHeight()
	got := strings.Count(m.renderPreview(n), "\n")
	if got != want {
		t.Errorf("previewHeight reserved %d lines, renderPreview drew %d", want, got)
	}
}
