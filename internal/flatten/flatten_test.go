package flatten

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// buildTree writes a small nested fixture and returns its root.
func buildTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"README.md":         "top",
		"src/main.go":       "a",
		"src/util.go":       "b",
		"tests/main.go":     "c", // collides with src/main.go after prefixing? no: src_main.go vs tests_main.go
		"docs/api/index.md": "d",
		"skipme/secret.txt": "e",
		"skipme/deep/x.txt": "f",
	}
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func planNames(items []Item) []string {
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.Dst
	}
	sort.Strings(names)
	return names
}

func TestPlanPrefixingAndRootNames(t *testing.T) {
	root := buildTree(t)
	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := planNames(items)

	set := map[string]bool{}
	for _, n := range got {
		set[n] = true
	}
	for _, expect := range []string{
		"README.md",   // root keeps name
		"src_main.go", // subdir prefixed by parent
		"src_util.go",
		"tests_main.go", // no collision with src_main.go thanks to prefix
		"api_index.md",  // prefixed by immediate parent, not full path
		"skipme_secret.txt",
		"deep_x.txt",
	} {
		if !set[expect] {
			t.Errorf("expected planned name %q, missing from %v", expect, got)
		}
	}
	if len(items) != 7 {
		t.Errorf("expected 7 files, got %d: %v", len(items), got)
	}
}

func TestPlanExclusionSkipsSubtree(t *testing.T) {
	root := buildTree(t)
	excluded := map[string]bool{filepath.Join(root, "skipme"): true}
	items, err := Plan(root, excluded)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if filepath.Base(filepath.Dir(it.Src)) == "skipme" || it.Dst == "deep_x.txt" {
			t.Errorf("excluded subtree leaked into plan: %q", it.Src)
		}
	}
	if len(items) != 5 { // 7 total minus the 2 under skipme/
		t.Errorf("expected 5 files after exclusion, got %d", len(items))
	}
}

func TestUniqueCollisionSuffix(t *testing.T) {
	used := map[string]bool{}
	a := Unique(used, "report.txt")
	b := Unique(used, "report.txt")
	c := Unique(used, "report.txt")
	if a != "report.txt" || b != "report_1.txt" || c != "report_2.txt" {
		t.Errorf("collision suffixing wrong: %q %q %q", a, b, c)
	}
}

func TestCopyProducesFiles(t *testing.T) {
	root := buildTree(t)
	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	failures := Copy(target, items, nil)
	if len(failures) != 0 {
		t.Fatalf("unexpected copy failures: %v", failures)
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != len(items) {
		t.Errorf("expected %d copied files, found %d", len(items), len(entries))
	}
}
