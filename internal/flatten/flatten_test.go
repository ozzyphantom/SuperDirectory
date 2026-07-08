package flatten

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
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

// TestUniqueIsCaseInsensitive guards against silent data loss. macOS, Windows,
// and every exFAT/FAT32 external drive fold case, so two reservations differing
// only in case name one file. The second must be suffixed, and both must keep
// the case they came in with.
func TestUniqueIsCaseInsensitive(t *testing.T) {
	used := map[string]bool{}
	a := Unique(used, "beach.JPG")
	b := Unique(used, "Beach.jpg")
	c := Unique(used, "BEACH.JPG")

	if a != "beach.JPG" {
		t.Errorf("first reservation should pass through unchanged, got %q", a)
	}
	if b != "Beach_1.jpg" {
		t.Errorf("case-only collision must be suffixed, got %q", b)
	}
	// _1 is already taken case-insensitively by Beach_1.jpg, so this must reach _2.
	if c != "BEACH_2.JPG" {
		t.Errorf("suffixed names must also collide case-insensitively, got %q", c)
	}
}

// TestWalkSkipsFilesystemMetadata models the root of a Mac-formatted external
// drive: AppleDouble sidecars beside every real file, plus the hidden service
// directories macOS and Windows leave behind. None of it should reach the plan.
func TestWalkSkipsFilesystemMetadata(t *testing.T) {
	root := t.TempDir()
	files := []string{
		"receipt.pdf", // real
		"notes.txt",   // real
		"Trip/beach.jpg",
		"._receipt.pdf", // AppleDouble sidecar
		"._notes.txt",
		".DS_Store",
		"Trip/._beach.jpg",
		"Trip/.DS_Store",
		".fseventsd/fseventsd-uuid",
		".Spotlight-V100/store.db",
		".Trashes/deleted.pdf",
		"$RECYCLE.BIN/gone.doc",
		"System Volume Information/tracking.log",
		"Thumbs.db",
	}
	for _, rel := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := planNames(items)

	want := []string{"Trip_beach.jpg", "notes.txt", "receipt.pdf"}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("planned %d files, want %d — metadata leaked:\n got: %v\nwant: %v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestWalkDoesNotSkipTheSourceItself: pointing deliberately at a metadata
// directory must still copy what is inside it.
func TestWalkDoesNotSkipTheSourceItself(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, ".Trashes")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "recovered.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := Plan(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Dst != "recovered.pdf" {
		t.Errorf("choosing a metadata dir as source should copy its contents, got %v", planNames(items))
	}
}

// TestCopyPreservesModTime: a superdirectory built to archive should not claim
// every file was written today.
func TestCopyPreservesModTime(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "old.txt")
	if err := os.WriteFile(src, []byte("vintage"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := time.Date(1999, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(src, time.Time{}, want); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f := Copy(target, items, nil); len(f) != 0 {
		t.Fatalf("unexpected failures: %v", f)
	}

	info, err := os.Stat(filepath.Join(target, "old.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// FAT32 stores mtime with two-second granularity, so compare with tolerance
	// rather than demanding an exact instant.
	if delta := info.ModTime().Sub(want); delta > 2*time.Second || delta < -2*time.Second {
		t.Errorf("copy has mtime %v, want %v (delta %v)", info.ModTime().UTC(), want, delta)
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
