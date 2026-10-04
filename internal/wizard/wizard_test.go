package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyphantom/SuperDirectory/internal/job"
)

func TestOverlaps(t *testing.T) {
	cases := []struct {
		name   string
		a, b   string
		expect bool
	}{
		{"identical", "/a/b", "/a/b", true},
		{"child under parent", "/a/b/c", "/a/b", true},
		{"parent contains child", "/a/b", "/a/b/c", true},
		{"siblings", "/a/b", "/a/c", false},
		{"disjoint", "/x/y", "/p/q", false},

		// The sensible default (save alongside as <name>-super) must NOT be
		// blocked — "-super" is a sibling, not a containment.
		{"default suffix sibling", "/u/o/photos-super", "/u/o/photos", false},

		// Case-insensitive volumes (APFS/NTFS): same dir, different case.
		{"case-only same dir", "/u/o/photos", "/u/o/Photos", true},
		{"case-only child", "/u/o/Photos/sub", "/u/o/photos", true},

		// Root as source: every child is inside it.
		{"child of root", "/foo-super", "/", true},
		{"root and root", "/", "/", true},
	}
	for _, c := range cases {
		if got := overlaps(c.a, c.b); got != c.expect {
			t.Errorf("%s: overlaps(%q,%q)=%v, want %v", c.name, c.a, c.b, got, c.expect)
		}
	}
}

func TestSafeBase(t *testing.T) {
	cases := map[string]string{
		"/a/b":    "b",
		"/":       "flattened",
		"/photos": "photos",
		"/a/b/":   "b",
	}
	for in, want := range cases {
		if got := safeBase(in); got != want {
			t.Errorf("safeBase(%q)=%q, want %q", in, got, want)
		}
	}
}

// TestCheckTargetRefusesAPopulatedFolder: the destination screen promises a new
// folder. Merging into one that already holds files would silently replace any that
// share a name with the plan.
func TestCheckTargetRefusesAPopulatedFolder(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel string, file bool) string {
		p := filepath.Join(dir, rel)
		if file {
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if err := checkTarget(filepath.Join(dir, "new")); err != nil {
		t.Errorf("a folder that does not exist yet was refused: %v", err)
	}
	if err := checkTarget(mk("empty", false)); err != nil {
		t.Errorf("an empty folder was refused: %v", err)
	}
	mk("bookkeeping/.DS_Store", true)
	if err := checkTarget(filepath.Join(dir, "bookkeeping")); err != nil {
		t.Errorf("a folder holding only .DS_Store was refused: %v", err)
	}
	mk("full/beach.jpg", true)
	if err := checkTarget(filepath.Join(dir, "full")); err == nil {
		t.Error("a folder holding files was accepted")
	}
	if err := checkTarget(mk("a-file", true)); err == nil {
		t.Error("an existing file was accepted as a folder")
	}
}

func TestFreeNameSkipsTakenFolders(t *testing.T) {
	dir := t.TempDir()
	if got := freeName(dir, "photos-super"); got != "photos-super" {
		t.Errorf("freeName = %q, want the base name when it is free", got)
	}
	for _, n := range []string{"photos-super", "photos-super-2"} {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n, "a.jpg"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := freeName(dir, "photos-super"); got != "photos-super-3" {
		t.Errorf("freeName = %q, want photos-super-3", got)
	}
}

func TestSummaryFitsLongPathsFromTheLeft(t *testing.T) {
	j := job.Job{
		Sources: []string{"/Volumes/Archive/clients/2024/very/deep/folder/Photos", "/b"},
		Target:  "/Volumes/Archive/clients/2024/very/deep/folder/Photos-super",
	}
	s := summary(j, 50)
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if w := len([]rune(line)); w > 50 {
			t.Errorf("%d columns: %q", w, line)
		}
	}
	if !strings.Contains(s, "…") || !strings.Contains(s, "folder/Photos and 1 more") || !strings.Contains(s, "folder/Photos-super") {
		t.Errorf("paths not cut from the left:\n%s", s)
	}
	if whole := Summary(j); !strings.Contains(whole, j.Target) {
		t.Errorf("Summary cut a path:\n%s", whole)
	}
}
