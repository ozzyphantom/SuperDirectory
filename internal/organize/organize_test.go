package organize

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

// buildTree writes a fixture exercising every classification branch: a root
// file, a nested file, a name collision across two subdirectories, an
// uppercase extension, a compound extension, an extensionless file, a dotfile,
// and a subtree to exclude.
func buildTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"README.md",
		"photo.JPG",
		"LICENSE",
		".gitignore",
		"archive.tar.gz",
		"src/main.go",
		"tests/main.go", // collides with src/main.go once flattened by type
		"docs/api/index.md",
		"skipme/secret.txt",
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
	return root
}

func dsts(items []flatten.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = filepath.ToSlash(it.Dst)
	}
	sort.Strings(out)
	return out
}

func TestExtension(t *testing.T) {
	cases := map[string]string{
		"notes.md":       "md",
		"photo.JPG":      "jpg", // case folded, so jpg/ and JPG/ never split
		"IMG_0001.jpeg":  "jpeg",
		"archive.tar.gz": "tar.gz", // compound suffix kept whole
		"backup.TAR.BZ2": "tar.bz2",
		"data.gz":        "gz", // plain gz is still gz
		"README":         "",   // no extension
		"LICENSE":        "",
		".gitignore":     "", // dotfile: a name, not an extension
		".env":           "",
		"file.":          "", // trailing dot is not an extension
	}
	for name, want := range cases {
		if got := Extension(name); got != want {
			t.Errorf("Extension(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCategory(t *testing.T) {
	cases := map[string]string{
		"pdf":    "Documents",
		"md":     "Documents",
		"jpg":    "Images",
		"heic":   "Images",
		"mp4":    "Video",
		"flac":   "Audio",
		"go":     "Code",
		"tar.gz": "Archives",
		"gz":     "Archives",
		"xlsx":   "Spreadsheets",
		"woff2":  "Fonts",
		"xyzzy":  CategoryOther, // unknown
		"":       CategoryOther, // extensionless
	}
	for ext, want := range cases {
		if got := Category(ext); got != want {
			t.Errorf("Category(%q) = %q, want %q", ext, got, want)
		}
	}
}

func TestPlanFlatGroupsByCategoryAndExtension(t *testing.T) {
	root := buildTree(t)
	items, err := Plan(root, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := dsts(items)

	want := []string{
		"Archives/tar.gz/archive.tar.gz",
		"Code/go/main.go",   // one of src/ or tests/
		"Code/go/main_1.go", // the other, suffixed
		"Documents/md/README.md",
		"Documents/md/index.md",
		"Documents/txt/secret.txt",
		"Images/jpg/photo.JPG", // original filename case preserved
		"Other/no-extension/.gitignore",
		"Other/no-extension/LICENSE",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("planned %d items, want %d:\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPlanKeepSourceTreeRecreatesNesting(t *testing.T) {
	root := buildTree(t)
	items, err := Plan(root, nil, Options{KeepSourceTree: true})
	if err != nil {
		t.Fatal(err)
	}
	got := dsts(items)

	want := []string{
		"Archives/tar.gz/archive.tar.gz",
		"Code/go/src/main.go",   // no collision: original paths differ
		"Code/go/tests/main.go", // ...so neither gets a _1 suffix
		"Documents/md/README.md",
		"Documents/md/docs/api/index.md",
		"Documents/txt/skipme/secret.txt",
		"Images/jpg/photo.JPG",
		"Other/no-extension/.gitignore",
		"Other/no-extension/LICENSE",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("planned %d items, want %d:\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPlanExclusionSkipsSubtree(t *testing.T) {
	root := buildTree(t)
	excluded := map[string]bool{filepath.Join(root, "skipme"): true}
	items, err := Plan(root, excluded, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if filepath.Base(it.Dst) == "secret.txt" {
			t.Errorf("excluded subtree leaked into plan: %q -> %q", it.Src, it.Dst)
		}
	}
	if len(items) != 8 { // 9 total minus the 1 under skipme/
		t.Errorf("expected 8 files after exclusion, got %d", len(items))
	}
}

// TestCopyCreatesNestedDirs proves the planner and flatten.Copy compose: the
// nested destination paths are materialized on disk with their contents intact.
func TestCopyCreatesNestedDirs(t *testing.T) {
	root := buildTree(t)
	items, err := Plan(root, nil, Options{KeepSourceTree: true})
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if failures := flatten.Copy(target, items, nil); len(failures) != 0 {
		t.Fatalf("unexpected copy failures: %v", failures)
	}

	// buildTree writes each file's source-relative path as its content, so the
	// content proves the right source landed at the right destination.
	want := map[string]string{
		"Code/go/src/main.go":            "src/main.go",
		"Code/go/tests/main.go":          "tests/main.go",
		"Documents/md/docs/api/index.md": "docs/api/index.md",
		"Other/no-extension/LICENSE":     "LICENSE",
	}
	for rel, content := range want {
		got, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("expected %s on disk: %v", rel, err)
			continue
		}
		if string(got) != content {
			t.Errorf("%s holds %q, want the file from %q", rel, got, content)
		}
	}
}
