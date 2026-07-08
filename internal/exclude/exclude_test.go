package exclude

import (
	"os"
	"path/filepath"
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
