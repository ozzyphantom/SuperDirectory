package flatten

import (
	"bytes"
	"io"
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
	if f := Copy(target, items, Options{}); len(f) != 0 {
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
	failures := Copy(target, items, Options{})
	if len(failures) != 0 {
		t.Fatalf("unexpected copy failures: %v", failures)
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != len(items) {
		t.Errorf("expected %d copied files, found %d", len(items), len(entries))
	}
}

// TestCopyAnnouncesEachFileBeforeReadingIt. A file that blocks forever in read
// produces no completion report, so if the name were only sent afterwards the user
// would stare at a frozen bar naming nothing. The announce must come first.
func TestCopyAnnouncesEachFileBeforeReadingIt(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "only.bin"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	var seen []Progress
	Copy(t.TempDir(), items, Options{OnProgress: func(p Progress) { seen = append(seen, p) }})
	if len(seen) < 2 {
		t.Fatalf("expected at least an announce and a completion, got %d", len(seen))
	}

	first := seen[0]
	if first.Current != "only.bin" {
		t.Errorf("first report should name the file: Current=%q", first.Current)
	}
	if first.Done != 0 {
		t.Errorf("first report is an announce, not a completion: Done=%d, want 0", first.Done)
	}
	if first.Bytes != 0 {
		t.Errorf("nothing is copied yet: Bytes=%d, want 0", first.Bytes)
	}

	last := seen[len(seen)-1]
	if last.Done != 1 || last.Total != 1 || last.Bytes != 100 {
		t.Errorf("final report wrong: %+v", last)
	}
}

// TestCopyReportsBytesAndProgress: the throughput shown to the user is derived from
// these numbers, so they must count only what actually reached the disk.
func TestCopyReportsBytesAndProgress(t *testing.T) {
	root := t.TempDir()
	for i, n := range []int{100, 250, 650} { // 1000 bytes total
		name := filepath.Join(root, string(rune('a'+i))+".bin")
		if err := os.WriteFile(name, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	var seen []Progress
	failures := Copy(t.TempDir(), items, Options{OnProgress: func(p Progress) { seen = append(seen, p) }})
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %v", failures)
	}

	for _, p := range seen {
		if p.Total != 3 {
			t.Errorf("Total=%d, want 3", p.Total)
		}
		if p.Elapsed < 0 {
			t.Errorf("Elapsed went backwards: %v", p.Elapsed)
		}
	}
	// Bytes accumulate monotonically and finish at the true total.
	for i := 1; i < len(seen); i++ {
		if seen[i].Bytes < seen[i-1].Bytes {
			t.Errorf("Bytes went backwards: %d -> %d", seen[i-1].Bytes, seen[i].Bytes)
		}
	}
	last := seen[len(seen)-1]
	if last.Bytes != 1000 || last.Done != 3 {
		t.Errorf("final report: Bytes=%d Done=%d, want 1000 and 3", last.Bytes, last.Done)
	}
}

// TestCopyDoesNotCountFailedBytes: a file that could not be opened must not
// contribute to throughput, or a failing copy would look fast.
func TestCopyDoesNotCountFailedBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.bin"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	items = append(items, Item{Src: filepath.Join(root, "does-not-exist.bin"), Dst: "gone.bin"})

	var last Progress
	failures := Copy(t.TempDir(), items, Options{OnProgress: func(p Progress) { last = p }})
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(failures))
	}
	if last.Bytes != 500 {
		t.Errorf("Bytes = %d, want 500 — a failed file inflated the throughput", last.Bytes)
	}
	if last.Done != 2 || last.Total != 2 {
		t.Errorf("attempted files still advance the counter: Done=%d Total=%d", last.Done, last.Total)
	}
}

// TestStreamCopyReportsEveryChunk pins the mechanism that lets a multi-gigabyte
// file show movement: each chunk written is reported as it lands.
func TestStreamCopyReportsEveryChunk(t *testing.T) {
	const chunks = 5
	src := bytes.NewReader(make([]byte, chunks*streamChunk))
	buf := make([]byte, streamChunk)

	var reports []int64
	if err := streamCopy(io.Discard, src, buf, func(n int64) { reports = append(reports, n) }); err != nil {
		t.Fatal(err)
	}
	if len(reports) != chunks {
		t.Fatalf("got %d chunk reports, want %d", len(reports), chunks)
	}
	for i, n := range reports {
		if n != streamChunk {
			t.Errorf("chunk %d reported %d bytes, want %d", i, n, streamChunk)
		}
	}
}

// TestCopyPollsProgressThroughALargeFile: Copy samples the in-flight file's byte
// counter, so a single big file moves on screen instead of freezing.
func TestCopyPollsProgressThroughALargeFile(t *testing.T) {
	defer swapPollInterval(t, time.Millisecond)()

	root := t.TempDir()
	size := 64 << 20 // large enough that copying outlasts several 1ms polls
	if err := os.WriteFile(filepath.Join(root, "big.bin"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	var midFile []int64
	Copy(t.TempDir(), items, Options{OnProgress: func(p Progress) {
		if p.Done == 0 && p.Bytes > 0 { // in flight, not yet completed
			midFile = append(midFile, p.Bytes)
		}
	}})

	if len(midFile) == 0 {
		t.Fatal("no mid-file progress: a big file still looks like a hang")
	}
	for i := 1; i < len(midFile); i++ {
		if midFile[i] < midFile[i-1] {
			t.Errorf("mid-file bytes went backwards: %d then %d", midFile[i-1], midFile[i])
		}
	}
	if last := midFile[len(midFile)-1]; last > int64(size) {
		t.Errorf("reported %d bytes for a %d byte file", last, size)
	}
}

// swapPollInterval sets the poll interval for one test and returns a restore func.
func swapPollInterval(t *testing.T, d time.Duration) func() {
	t.Helper()
	old := pollInterval
	pollInterval = d
	return func() { pollInterval = old }
}
