package dedup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

// write creates a file with exact contents and returns its path.
func write(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func plan(paths ...string) []flatten.Item {
	items := make([]flatten.Item, len(paths))
	for i, p := range paths {
		items[i] = flatten.Item{Src: p, Dst: filepath.Base(p)}
	}
	return items
}

func kept(items []flatten.Item, res Result) []string {
	var out []string
	for _, it := range Filter(items, res) {
		out = append(out, filepath.Base(it.Src))
	}
	return out
}

// TestFindIdentifiesByContentNotName is the whole point: a renamed copy is a
// duplicate, and a same-named different file is not.
func TestFindIdentifiesByContentNotName(t *testing.T) {
	dir := t.TempDir()
	photo := bytes.Repeat([]byte("photo-bytes"), 500)
	other := bytes.Repeat([]byte("other-bytes"), 500) // same length, different content

	a := write(t, dir, "Trip/beach.jpg", photo)
	b := write(t, dir, "Backup/beach copy.jpg", photo) // renamed duplicate
	c := write(t, dir, "Old/beach.jpg", other)         // same name, different photo

	items := plan(a, b, c)
	res := Find(items, Options{})

	if res.Files != 1 {
		t.Fatalf("expected 1 file to skip, got %d (sets=%d)", res.Files, len(res.Sets))
	}
	if len(res.Sets) != 1 {
		t.Fatalf("expected 1 duplicate set, got %d", len(res.Sets))
	}
	set := res.Sets[0]
	if set.Keep != 0 {
		t.Errorf("should keep the earliest in plan order (index 0), kept %d", set.Keep)
	}
	if len(set.Skip) != 1 || set.Skip[0] != 1 {
		t.Errorf("should skip the renamed copy at index 1, got %v", set.Skip)
	}
	if res.Bytes != int64(len(photo)) {
		t.Errorf("Bytes = %d, want %d (one copy's worth)", res.Bytes, len(photo))
	}

	got := kept(items, res)
	if len(got) != 2 || got[0] != "beach.jpg" || got[1] != "beach.jpg" {
		t.Errorf("kept %v — the distinct same-named file must survive", got)
	}
}

// TestFindSkipsFilesWithUniqueSize proves the gate: files nothing can match are
// never opened.
func TestFindSkipsFilesWithUniqueSize(t *testing.T) {
	dir := t.TempDir()
	a := write(t, dir, "a.bin", []byte("1"))
	b := write(t, dir, "b.bin", []byte("22"))
	c := write(t, dir, "c.bin", []byte("333"))

	res := Find(plan(a, b, c), Options{})
	if res.Hashed != 0 {
		t.Errorf("hashed %d files, want 0 — every size is unique", res.Hashed)
	}
	if res.Files != 0 {
		t.Errorf("found %d duplicates among distinct files", res.Files)
	}
}

// TestFindNeedsTheFullHash: two files sharing a size AND a leading block, differing
// only near the end. The partial hash must not be trusted on its own.
func TestFindNeedsTheFullHash(t *testing.T) {
	dir := t.TempDir()
	head := bytes.Repeat([]byte("A"), partialHashBytes)

	same := append(append([]byte{}, head...), []byte("tail-one")...)
	diff := append(append([]byte{}, head...), []byte("tail-two")...)

	a := write(t, dir, "a.bin", same)
	b := write(t, dir, "b.bin", diff)
	c := write(t, dir, "c.bin", same)

	res := Find(plan(a, b, c), Options{})
	if res.Files != 1 {
		t.Fatalf("expected exactly 1 duplicate (a==c), got %d", res.Files)
	}
	set := res.Sets[0]
	if set.Keep != 0 || len(set.Skip) != 1 || set.Skip[0] != 2 {
		t.Errorf("wrong set: keep=%d skip=%v; a and c are identical, b is not", set.Keep, set.Skip)
	}
	// All three shared a leading block, so all three were read twice: once partial,
	// once full.
	if res.Hashed != 6 {
		t.Errorf("hashed %d times, want 6 (3 partial + 3 full)", res.Hashed)
	}
}

// TestFindStopsAtThePartialHashWhenItSuffices: three same-size files with different
// leading blocks are never fully read.
func TestFindStopsAtThePartialHashWhenItSuffices(t *testing.T) {
	dir := t.TempDir()
	n := partialHashBytes * 4
	mk := func(fill byte) []byte { return bytes.Repeat([]byte{fill}, n) }

	a := write(t, dir, "a.bin", mk('a'))
	b := write(t, dir, "b.bin", mk('b'))
	c := write(t, dir, "c.bin", mk('c'))

	res := Find(plan(a, b, c), Options{})
	if res.Files != 0 {
		t.Errorf("found %d duplicates among distinct files", res.Files)
	}
	if res.Hashed != 3 {
		t.Errorf("hashed %d times, want 3 — the leading block already separated them", res.Hashed)
	}
}

// TestFindKeepsTheUnsuffixedName. The plan gives the first occurrence the clean name
// and later ones "_1", "_2". Keeping the earliest means the survivor is "beach.jpg",
// not "beach_1.jpg".
func TestFindKeepsTheUnsuffixedName(t *testing.T) {
	dir := t.TempDir()
	photo := bytes.Repeat([]byte("x"), 4096)
	a := write(t, dir, "one/beach.jpg", photo)
	b := write(t, dir, "two/beach.jpg", photo)

	items := []flatten.Item{
		{Src: a, Dst: "beach.jpg"},
		{Src: b, Dst: "beach_1.jpg"},
	}
	res := Find(items, Options{})
	out := Filter(items, res)
	if len(out) != 1 {
		t.Fatalf("expected 1 item after filtering, got %d", len(out))
	}
	if out[0].Dst != "beach.jpg" {
		t.Errorf("survivor is %q, want the unsuffixed beach.jpg", out[0].Dst)
	}
}

// TestFindIgnoresEmptyFiles: every zero-byte file is "identical" to every other, which
// is true and useless.
func TestFindIgnoresEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	a := write(t, dir, "a.txt", nil)
	b := write(t, dir, "b.txt", nil)

	res := Find(plan(a, b), Options{})
	if res.Files != 0 {
		t.Errorf("empty files were proposed for skipping: %+v", res.Sets)
	}
}

// TestFilterIsIdentityWhenNothingFound guards the fast path.
func TestFilterIsIdentityWhenNothingFound(t *testing.T) {
	items := plan("/a", "/b")
	out := Filter(items, Result{})
	if len(out) != 2 {
		t.Fatalf("Filter dropped items with an empty Result: %v", out)
	}
}

func TestProgressCountsOnlyCandidates(t *testing.T) {
	dir := t.TempDir()
	photo := bytes.Repeat([]byte("p"), 2048)
	a := write(t, dir, "a.bin", photo)
	b := write(t, dir, "b.bin", photo)
	c := write(t, dir, "c.bin", []byte("unique size"))

	var totals []int
	Find(plan(a, b, c), Options{OnProgress: func(p Progress) { totals = append(totals, p.Total) }})
	if len(totals) == 0 {
		t.Fatal("no progress reported")
	}
	for _, total := range totals {
		if total != 2 {
			t.Errorf("progress Total = %d, want 2 — only same-size files are read", total)
		}
	}
}
