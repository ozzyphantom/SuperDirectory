package flatten

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	if err := streamCopy(io.Discard, src, buf, func(n int64) { reports = append(reports, n) }, nil, nil); err != nil {
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

// TestCancelBeforeStartCopiesNothing: a stop that arrives before the first file means
// no file is started, nothing is reported as failed, and the target stays empty.
func TestCancelBeforeStartCopiesNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel := make(chan struct{})
	close(cancel)

	target := t.TempDir()
	if f := Copy(target, items, Options{Cancel: cancel}); len(f) != 0 {
		t.Fatalf("failures = %v, want none: nothing was in flight", f)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Errorf("target holds %d entries, want none", len(entries))
	}
}

// TestAssignLetsASurvivorReclaimItsName: when a duplicate earlier in the plan is
// dropped, the file it collided with must get the plain name back, not keep the
// "_1" it was given while the duplicate held "beach.jpg".
func TestAssignLetsASurvivorReclaimItsName(t *testing.T) {
	items := []Item{
		{Src: "/a/beach.jpg", Want: "beach.jpg"},
		{Src: "/b/beach.jpg", Want: "beach.jpg"},
		{Src: "/c/other.jpg", Want: "other.jpg"},
	}
	Assign(items)
	if items[0].Dst != "beach.jpg" || items[1].Dst != "beach_1.jpg" {
		t.Fatalf("first pass: %q, %q", items[0].Dst, items[1].Dst)
	}

	survivors := []Item{items[1], items[2]} // the first beach.jpg was a duplicate
	Assign(survivors)
	if survivors[0].Dst != "beach.jpg" {
		t.Errorf("survivor kept %q; it should reclaim beach.jpg", survivors[0].Dst)
	}
	if survivors[1].Dst != "other.jpg" {
		t.Errorf("an unrelated name changed: %q", survivors[1].Dst)
	}
}

// TestAssignFallsBackToDst: an item built without a Want keeps its Dst as the name it
// wants, so a hand-made plan survives Assign unchanged.
func TestAssignFallsBackToDst(t *testing.T) {
	items := []Item{{Src: "/x", Dst: "x.txt"}}
	Assign(items)
	if items[0].Dst != "x.txt" {
		t.Errorf("Dst = %q, want x.txt", items[0].Dst)
	}
}

func writeTree(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestScanSeveralSourcesKeepsThemApart: two sources each with a README must not
// fight over one name, and every planner sees which source a file came from.
func TestScanSeveralSourcesKeepsThemApart(t *testing.T) {
	a := filepath.Join(t.TempDir(), "Manuals")
	b := filepath.Join(t.TempDir(), "Manuals") // same folder name, different drive
	writeTree(t, a, "README.md", "net/setup.pdf")
	writeTree(t, b, "README.md")

	files, err := Scan{Sources: []string{a, b}}.Files()
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range files {
		rels = append(rels, filepath.ToSlash(f.Rel))
		if f.Size <= 0 || f.ModTime.IsZero() {
			t.Errorf("%s was not measured", f.Rel)
		}
	}
	want := []string{"Manuals/README.md", "Manuals/net/setup.pdf", "Manuals_2/README.md"}
	if strings.Join(rels, ",") != strings.Join(want, ",") {
		t.Fatalf("Rel = %v, want %v", rels, want)
	}
	got := map[string]bool{}
	for _, it := range PlanFiles(files) {
		got[it.Dst] = true
	}
	for _, d := range []string{"Manuals_README.md", "net_setup.pdf", "Manuals_2_README.md"} {
		if !got[d] {
			t.Errorf("missing %s in %v", d, got)
		}
	}
}

func TestScanFilters(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "keep.pdf", "big.iso", "node_modules/lib.js", "src/node_modules/x.js", "src/a.go")
	files, err := Scan{
		Sources: []string{root},
		Prune:   func(name string) bool { return name == "node_modules" },
		Keep:    func(f File) bool { return filepath.Ext(f.Path) != ".iso" },
	}.Files()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.ToSlash(f.Rel))
	}
	if strings.Join(names, ",") != "keep.pdf,src/a.go" {
		t.Errorf("kept %v", names)
	}
}

func TestScanStops(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt")
	stop := make(chan struct{})
	close(stop)
	if _, err := (Scan{Sources: []string{root}, Cancel: stop}).Files(); !errors.Is(err, ErrStopped) {
		t.Errorf("err = %v, want ErrStopped", err)
	}
}

func TestPlanDepthKeepsTheTopLevels(t *testing.T) {
	mk := func(rel string) File { return File{Path: "/s/" + rel, Rel: filepath.FromSlash(rel)} }
	files := []File{mk("top.txt"), mk("Docs/one.pdf"), mk("Docs/A/B/deep.pdf"), mk("Docs/A/x.pdf")}
	var got []string
	for _, it := range PlanDepth(files, 1) {
		got = append(got, filepath.ToSlash(it.Dst))
	}
	want := []string{"top.txt", "Docs/one.pdf", "Docs/B_deep.pdf", "Docs/A_x.pdf"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("depth 1: %v, want %v", got, want)
	}
}

func TestLabelsAreDistinctIgnoringCase(t *testing.T) {
	got := Labels([]string{"/a/Photos", "/b/photos", "/", "/c/Photos"})
	want := []string{"Photos", "photos_2", "root", "Photos_3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Labels = %v, want %v", got, want)
	}
}

// TestResumeSkipsWhatIsAlreadyThere is the point of resume: a copy stopped at file
// 1084 of 11041 picks up at 1084, and a file changed since is copied again.
func TestResumeSkipsWhatIsAlreadyThere(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "b.txt", "c.txt")
	items, err := Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if res := Execute(target, items, Options{}); res.Copied != 3 {
		t.Fatalf("first run copied %d", res.Copied)
	}

	// Change one source file, as an edit between runs would.
	changed := filepath.Join(root, "b.txt")
	os.WriteFile(changed, []byte("b, edited and longer"), 0o644)
	items, _ = Plan(root, nil)

	res := Execute(target, items, Options{Resume: true})
	if res.Existing != 2 || res.Copied != 1 {
		t.Errorf("resume: existing %d, copied %d; want 2 and 1", res.Existing, res.Copied)
	}
	if res.Outcomes[1] != Copied || res.Outcomes[0] != Existing {
		t.Errorf("outcomes %v", res.Outcomes)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "b.txt")); string(got) != "b, edited and longer" {
		t.Errorf("the edited file was not copied again: %q", got)
	}
}

// TestPauseIsNotAStall: a copy paused for longer than the stall timeout must not
// abandon anything, between files or in the middle of one.
func TestPauseIsNotAStall(t *testing.T) {
	defer swapPollInterval(t, 5*time.Millisecond)()

	root := t.TempDir()
	writeTree(t, root, "a.txt", "b.txt")
	items, _ := Plan(root, nil)
	target := t.TempDir()

	p := &Pauser{}
	p.Toggle()
	done := make(chan Result, 1)
	go func() { done <- Execute(target, items, Options{StallTimeout: 30 * time.Millisecond, Pause: p}) }()

	time.Sleep(150 * time.Millisecond)
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatalf("copied %d files while paused", len(entries))
	}
	if !p.Paused() || p.Since().IsZero() {
		t.Error("Paused/Since")
	}
	p.Toggle()
	select {
	case res := <-done:
		if len(res.Failures) != 0 || res.Copied != 2 {
			t.Errorf("after resuming: %d copied, failures %v", res.Copied, res.Failures)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the copy never resumed")
	}
}

func TestVerifyCatchesABadCopy(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "copy.bin")
	os.WriteFile(dst, []byte("not what was written"), 0o644)
	j := &copyJob{dst: dst, stopped: make(chan struct{})}
	sum := sha256.Sum256([]byte("what was written"))
	var ve VerifyError
	if err := j.check(sum[:]); !errors.As(err, &ve) {
		t.Errorf("err = %v, want VerifyError", err)
	}
	good := sha256.Sum256([]byte("not what was written"))
	if err := j.check(good[:]); err != nil {
		t.Errorf("a matching copy failed: %v", err)
	}

	// End to end: a verified copy of real files succeeds and says so.
	root := t.TempDir()
	writeTree(t, root, "a.txt", "deep/b.txt")
	items, _ := Plan(root, nil)
	if res := Execute(t.TempDir(), items, Options{Verify: true, StallTimeout: time.Minute}); res.Copied != 2 || len(res.Failures) != 0 {
		t.Errorf("verified copy: %+v", res)
	}
}

// TestCloneOrCopy: where the filesystem clones (APFS here, Btrfs/XFS on Linux) the
// file is cloned; elsewhere it is copied. Either way the copy is exact and keeps
// its time.
func TestCloneOrCopy(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "photo.jpg")
	old := time.Date(2019, 7, 14, 10, 0, 0, 0, time.Local)
	os.Chtimes(filepath.Join(root, "photo.jpg"), old, old)
	items, _ := Plan(root, nil)
	target := t.TempDir()

	res := Execute(target, items, Options{Clone: true})
	if len(res.Failures) != 0 || res.Copied+res.Cloned != 1 {
		t.Fatalf("result %+v", res)
	}
	t.Logf("outcome: %v (cloned=%d)", res.Outcomes[0], res.Cloned)
	got, _ := os.ReadFile(filepath.Join(target, "photo.jpg"))
	if string(got) != "photo.jpg" {
		t.Errorf("contents %q", got)
	}
	if info, _ := os.Stat(filepath.Join(target, "photo.jpg")); !info.ModTime().Equal(old) {
		t.Errorf("time %v, want %v", info.ModTime(), old)
	}
}

func TestFreeSpaceAndSameVolume(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	free, err := FreeSpace(filepath.Join(a, "not", "yet", "made"))
	if err != nil || free <= 0 {
		t.Errorf("FreeSpace = %d, %v", free, err)
	}
	if !SameVolume(a, filepath.Join(b, "new")) {
		t.Error("two temporary folders should share a volume")
	}
}

func TestExecuteStopsWhenTheDestinationIsFull(t *testing.T) {
	src, target := t.TempDir(), t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		os.WriteFile(filepath.Join(src, n), []byte(n), 0o644)
	}
	// A file where b's folder should be makes b fail; isFull treats that as full.
	os.WriteFile(filepath.Join(target, "blocked"), nil, 0o644)
	old := isFull
	isFull = func(err error) bool { return err != nil }
	t.Cleanup(func() { isFull = old })

	items := []Item{
		{Src: filepath.Join(src, "a.txt"), Dst: "a.txt"},
		{Src: filepath.Join(src, "b.txt"), Dst: "blocked/b.txt"},
		{Src: filepath.Join(src, "c.txt"), Dst: "c.txt"},
	}
	res := Execute(target, items, Options{})
	if !res.Full {
		t.Fatal("Full not set")
	}
	want := []Outcome{Copied, Failed, NotReached}
	for i, o := range res.Outcomes {
		if o != want[i] && !(i == 0 && o == Cloned) {
			t.Errorf("item %d: outcome %v, want %v", i, o, want[i])
		}
	}
	if _, err := os.Stat(filepath.Join(target, "c.txt")); !os.IsNotExist(err) {
		t.Error("a file after the full disk was copied")
	}
}
