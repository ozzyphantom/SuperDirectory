package dedup

import (
	"bytes"
	"testing"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

func TestLooksLikeCopy(t *testing.T) {
	copies := []string{
		"beach copy", "beach copy 2", "beach - copy", "beach - copy (2)", "beach (1)",
		"beach(12)", "copy of beach", "beach (copy)", "beach (another copy)",
		"beach (3rd copy)", "beach_copy", "beach-copy2", "beach (oscar's conflicted copy 2026-01-02)",
	}
	for _, s := range copies {
		if !looksLikeCopy(s, nil) {
			t.Errorf("%q should read as a copy", s)
		}
	}
	originals := []string{"beach", "img_0001", "dsc_4418", "report (2019)", "copyright", "beachcopy"}
	for _, s := range originals {
		if looksLikeCopy(s, nil) {
			t.Errorf("%q should read as an original", s)
		}
	}
	// A numeric suffix only counts beside the name it numbers.
	if !looksLikeCopy("beach_1", map[string]bool{"beach": true}) {
		t.Error(`"beach_1" beside "beach" should read as a copy`)
	}
	if !looksLikeCopy("img_0042 2", map[string]bool{"img_0042": true}) {
		t.Error(`Finder's "Keep Both" name should read as a copy`)
	}
	if looksLikeCopy("beach_1", map[string]bool{"sunset": true}) {
		t.Error(`"beach_1" alone is an original`)
	}
}

// TestKeeperPrefersTheOriginalName is the case that grated: the walk is lexical, so
// "Backup/beach copy.jpg" came first and was kept over "Trip/beach.jpg".
func TestKeeperPrefersTheOriginalName(t *testing.T) {
	dir := t.TempDir()
	photo := bytes.Repeat([]byte("p"), 4096)
	items := plan(
		write(t, dir, "Backup/beach copy.jpg", photo),
		write(t, dir, "Trip/beach.jpg", photo),
	)
	res := Find(items, Options{})
	if len(res.Sets) != 1 || res.Sets[0].Keep != 1 {
		t.Fatalf("sets = %+v, want Trip/beach.jpg (index 1) kept", res.Sets)
	}
	if got := kept(items, res); len(got) != 1 || got[0] != "beach.jpg" {
		t.Errorf("kept %v, want beach.jpg", got)
	}
}

func TestKeeperPrefersTheShallowestPath(t *testing.T) {
	dir := t.TempDir()
	photo := bytes.Repeat([]byte("p"), 4096)
	items := plan(
		write(t, dir, "Archive/2019/Summer/beach.jpg", photo),
		write(t, dir, "Trip/beach.jpg", photo),
	)
	res := Find(items, Options{})
	if len(res.Sets) != 1 || res.Sets[0].Keep != 1 {
		t.Errorf("sets = %+v, want the shallower Trip/beach.jpg kept", res.Sets)
	}
}

// TestKeeperReclaimsTheCleanNameAfterAssign ties the two halves together: the plan
// gives "beach.jpg" to the copy that sorts first; the keeper is the other one; after
// the copy is dropped and the plan reassigned, the keeper lands as "beach.jpg".
func TestKeeperReclaimsTheCleanNameAfterAssign(t *testing.T) {
	dir := t.TempDir()
	photo := bytes.Repeat([]byte("p"), 4096)
	a := write(t, dir, "A/deep/er/beach.jpg", photo)
	b := write(t, dir, "B/beach.jpg", photo)
	items := []flatten.Item{{Src: a, Want: "beach.jpg"}, {Src: b, Want: "beach.jpg"}}
	flatten.Assign(items)

	out := Filter(items, Find(items, Options{}))
	flatten.Assign(out)
	if len(out) != 1 || out[0].Src != b || out[0].Dst != "beach.jpg" {
		t.Errorf("survivor = %+v, want B/beach.jpg landing as beach.jpg", out)
	}
}
