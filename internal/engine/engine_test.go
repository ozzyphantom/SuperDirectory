package engine

import (
	"archive/zip"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/dedup"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
)

// fakeHooks is a front end with no screen: it applies every duplicate set, answers
// questions from its fields, and runs the copy plainly unless told otherwise.
type fakeHooks struct {
	stages   []Stage
	space    bool
	asked    bool
	retry    func(attempt int) bool
	onCopy   func(c *CopyRun) flatten.Result
	choose   func(f *Found) ([]DupSet, error)
	lastSeen *Found
}

func (h *fakeHooks) Stage(s Stage)                    { h.stages = append(h.stages, s) }
func (h *fakeHooks) Progress(Stage, int, int, string) {}
func (h *fakeHooks) Scan(dedup.Progress)              {}
func (h *fakeHooks) Duplicates(f *Found) ([]DupSet, error) {
	h.lastSeen = f
	if h.choose != nil {
		return h.choose(f)
	}
	return f.Sets, nil
}
func (h *fakeHooks) Space(need, free int64) (bool, error) { h.asked = true; return h.space, nil }
func (h *fakeHooks) Copy(c *CopyRun) flatten.Result {
	if h.onCopy != nil {
		return h.onCopy(c)
	}
	return c.Run(c.Options)
}
func (h *fakeHooks) Retry(_ []flatten.Failure, attempt, _ int) bool {
	return h.retry != nil && h.retry(attempt)
}

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Source")
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func listTarget(t *testing.T, target string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(target, func(p string, d os.DirEntry, err error) error {
		if d.IsDir() && d.Name() == StateDirName {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(target, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// elsewhere makes every destination look like another volume, where files are
// copied rather than cloned and the space check applies.
func elsewhere(t *testing.T) {
	t.Helper()
	old := sameVolume
	sameVolume = func(string, string) bool { return false }
	t.Cleanup(func() { sameVolume = old })
}

func usePrivateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("APPDATA", filepath.Join(dir, "AppData"))
}

func TestRunWithDuplicatesBatchesAndReport(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{
		"Trip/beach.jpg":        "the same photograph",
		"Backup/beach copy.jpg": "the same photograph",
		"notes.txt":             "notes",
		"Work/report.pdf":       "report",
		"Work/plan.pdf":         "plan",
	})
	target := filepath.Join(filepath.Dir(src), "Out")
	h := &fakeHooks{}
	sum, err := Run(job.Job{Sources: []string{src}, Target: target, Duplicates: []string{job.Identical}, Batch: 2}, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Planned != 4 || len(sum.Skipped) != 1 || sum.Batches != 2 {
		t.Fatalf("summary: planned %d, skipped %d sets, %d batches", sum.Planned, len(sum.Skipped), sum.Batches)
	}
	got := strings.Join(listTarget(t, target), ",")
	want := "Batch 01/Trip_beach.jpg,Batch 01/Work_plan.pdf,Batch 02/Work_report.pdf,Batch 02/notes.txt"
	if got != want {
		t.Errorf("target holds %s\nwant %s", got, want)
	}
	csv, err := os.ReadFile(filepath.Join(target, StateDirName, "report.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(csv), "skipped,") || !strings.Contains(string(csv), "identical copy of Trip/beach.jpg") {
		t.Errorf("report.csv does not account for the skipped duplicate:\n%s", csv)
	}
	md, _ := os.ReadFile(filepath.Join(target, StateDirName, "report.md"))
	if !strings.Contains(string(md), "## Skipped duplicates (1)") {
		t.Errorf("report.md:\n%s", md)
	}
	if Interrupted(target) {
		t.Error("a finished run reads as interrupted")
	}
}

// TestStoppedRunResumes is the point of the run record: a copy stopped part way
// is finished by running it again, without copying what already arrived.
func TestStoppedRunResumes(t *testing.T) {
	usePrivateConfig(t)
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		files[n+".txt"] = strings.Repeat(n, 100)
	}
	src := tree(t, files)
	target := filepath.Join(filepath.Dir(src), "Out")

	stop := make(chan struct{})
	h := &fakeHooks{onCopy: func(c *CopyRun) flatten.Result {
		opts := c.Options
		opts.OnProgress = func(p flatten.Progress) {
			if p.Done == 3 {
				select {
				case <-stop:
				default:
					close(stop)
				}
			}
		}
		return c.Run(opts)
	}}
	sum, err := Run(job.Job{Sources: []string{src}, Target: target}, h, stop)
	if !errors.Is(err, ErrStopped) || !sum.Stopped {
		t.Fatalf("err = %v, stopped = %v", err, sum.Stopped)
	}
	if !Interrupted(target) {
		t.Fatal("a stopped run should read as interrupted")
	}

	state, err := LoadState(target)
	if err != nil {
		t.Fatal(err)
	}
	sum, err = Run(state.Job, &fakeHooks{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// On one APFS volume the engine clones rather than copies; both count.
	written := sum.Result.Copied + sum.Result.Cloned
	if sum.Result.Existing < 3 || written+sum.Result.Existing != 6 {
		t.Errorf("resume: %d already there, %d written; want at least 3 and 6 in all", sum.Result.Existing, written)
	}
	if Interrupted(target) {
		t.Error("the resumed run did not mark itself complete")
	}
}

func TestRunByDateUsesModificationTimeWithoutMetadata(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{"a.txt": "a", "b/c.txt": "c"})
	march := time.Date(2021, 3, 9, 12, 0, 0, 0, time.Local)
	os.Chtimes(filepath.Join(src, "a.txt"), march, march)
	os.Chtimes(filepath.Join(src, "b", "c.txt"), march.AddDate(1, 0, 0), march.AddDate(1, 0, 0))
	target := filepath.Join(filepath.Dir(src), "Out")
	if _, err := Run(job.Job{Sources: []string{src}, Target: target, Layout: job.ByDate}, &fakeHooks{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listTarget(t, target), ","); got != "2021/03/a.txt,2022/03/c.txt" {
		t.Errorf("target holds %s", got)
	}
}

func TestRunAsksBeforeOverfillingTheDestination(t *testing.T) {
	usePrivateConfig(t)
	old := freeSpace
	freeSpace = func(string) (int64, error) { return 10, nil }
	defer func() { freeSpace = old }()

	elsewhere(t)

	src := tree(t, map[string]string{"big.bin": strings.Repeat("x", 5000)})
	target := filepath.Join(filepath.Dir(src), "Out")
	h := &fakeHooks{space: false}
	r := &run{j: job.Job{Sources: []string{src}, Target: target}, h: h, notes: map[string]string{}}
	items := []flatten.Item{{Src: filepath.Join(src, "big.bin"), Dst: "big.bin", Size: 5000}}
	if err := r.checkSpace(items); !errors.Is(err, ErrAbandoned) || !h.asked {
		t.Errorf("err = %v, asked = %v", err, h.asked)
	}
}

func TestRunRetriesFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	usePrivateConfig(t)
	src := tree(t, map[string]string{"ok.txt": "ok", "locked.txt": "locked"})
	locked := filepath.Join(src, "locked.txt")
	os.Chmod(locked, 0o000)
	defer os.Chmod(locked, 0o644)

	target := filepath.Join(filepath.Dir(src), "Out")
	h := &fakeHooks{retry: func(attempt int) bool {
		os.Chmod(locked, 0o644) // as if the drive had cooled
		return true
	}}
	sum, err := Run(job.Job{Sources: []string{src}, Target: target, Retries: 1}, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	if written := sum.Result.Copied + sum.Result.Cloned; len(sum.Result.Failures) != 0 || written != 2 {
		t.Errorf("after a retry: %d written, failures %v", written, sum.Result.Failures)
	}
}

func TestRunFiltersSeveralSources(t *testing.T) {
	usePrivateConfig(t)
	a := tree(t, map[string]string{"manual.pdf": "m", "node_modules/x.js": "x", "clip.mp4": "v", "big.pdf": strings.Repeat("b", 2000)})
	b := tree(t, map[string]string{"guide.pdf": "g", "old/legacy.pdf": "l"})
	target := filepath.Join(t.TempDir(), "Out")
	j := job.Job{
		Sources:  []string{a, b},
		Target:   target,
		Excluded: []string{filepath.Join(b, "old")},
		Skip:     []string{"node_modules"},
		Only:     []string{"Documents"},
		MaxSize:  1000,
	}
	if _, err := Run(j, &fakeHooks{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listTarget(t, target), ","); got != "Source_2_guide.pdf,Source_manual.pdf" {
		t.Errorf("target holds %s", got)
	}
}

func TestBatchNumbersSortAsText(t *testing.T) {
	items := make([]flatten.Item, 120)
	for i := range items {
		items[i] = flatten.Item{Want: "f.txt"}
	}
	if n := batch(items, 10); n != 12 {
		t.Fatalf("%d batches, want 12", n)
	}
	if !strings.HasPrefix(items[0].Dst, "Batch 01") || !strings.HasPrefix(items[119].Dst, "Batch 12") {
		t.Errorf("first %q, last %q", items[0].Dst, items[119].Dst)
	}
}

// TestExpandPutsArchiveContentsInItsPlace: a zip's files take the zip's place and
// name; the zip is not copied; the staging folder is gone after the run; and the
// report says where each file came from.
func TestExpandPutsArchiveContentsInItsPlace(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{"readme.txt": "hi"})
	zf, _ := os.Create(filepath.Join(src, "Manual.zip"))
	zw := zip.NewWriter(zf)
	for name, body := range map[string]string{"docs/setup.htm": "setup", "docs/faq.htm": "faq", "../escape.htm": "nope"} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	zf.Close()

	target := filepath.Join(filepath.Dir(src), "Out")
	sum, err := Run(job.Job{Sources: []string{src}, Target: target, Expand: true, Layout: job.ByDepth, Depth: 3}, &fakeHooks{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listTarget(t, target), ","); got != "Manual/docs/faq.htm,Manual/docs/setup.htm,readme.txt" {
		t.Errorf("target holds %s", got)
	}
	if sum.Expanded != 2 {
		t.Errorf("Expanded = %d", sum.Expanded)
	}
	if _, err := os.Stat(filepath.Join(target, StateDirName, "expanded")); !os.IsNotExist(err) {
		t.Error("the staging folder was left behind")
	}
	csv, _ := os.ReadFile(filepath.Join(target, StateDirName, "report.csv"))
	for _, want := range []string{"Manual.zip!/docs/setup.htm", "expanded,", "1 entries refused"} {
		if !strings.Contains(string(csv), want) {
			t.Errorf("report.csv missing %q:\n%s", want, csv)
		}
	}
}

// TestAbandonedRunLeavesNothing: canceling at the duplicates prompt, after archives
// were staged, removes the destination the run created.
func TestAbandonedRunLeavesNothing(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{"a.txt": "same", "b.txt": "same"})
	zf, _ := os.Create(filepath.Join(src, "x.zip"))
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("inner.txt")
	w.Write([]byte("inner"))
	zw.Close()
	zf.Close()

	target := filepath.Join(filepath.Dir(src), "Out")
	h := &fakeHooks{choose: func(*Found) ([]DupSet, error) { return nil, ErrAbandoned }}
	_, err := Run(job.Job{Sources: []string{src}, Target: target, Expand: true, Duplicates: []string{job.Identical}}, h, nil)
	if !errors.Is(err, ErrAbandoned) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("an abandoned run left its destination behind")
	}
}

// prose writes n paragraphs of seeded pseudo-English, about 60 words each.
func prose(seed int64, n int) []string {
	words := strings.Fields("the router forwards each packet to the next hop based on its table while the switch learns " +
		"addresses from frames and floods unknown destinations across every port in the vlan so the administrator " +
		"should configure trunks carefully and verify spanning tree priorities before adding new links to the core")
	r := rand.New(rand.NewSource(seed))
	var paras []string
	for p := 0; p < n; p++ {
		var b strings.Builder
		for w := 0; w < 60; w++ {
			b.WriteString(words[r.Intn(len(words))])
			b.WriteByte(' ')
		}
		paras = append(paras, b.String())
	}
	return paras
}

// revise replaces paragraph at with a fresh one, as an edit between revisions does.
func revise(paras []string, at int, seed int64) []string {
	out := append([]string{}, paras...)
	out[at] = prose(seed, 1)[0]
	return out
}

// TestNearDuplicateDocumentsKeepTheNewest: revisions of one manual keep the newest.
// An unrelated document, and two short stub pages that differ only in a line, are
// left alone: stubs are too short to compare.
func TestNearDuplicateDocumentsKeepTheNewest(t *testing.T) {
	usePrivateConfig(t)
	v1 := prose(1, 40)
	v2 := revise(v1, 5, 2)
	v3 := append(revise(v2, 20, 3), prose(4, 1)[0]) // one more edit, one paragraph added
	stub := "home products support contact " + strings.Repeat("menu item ", 20)

	src := tree(t, map[string]string{
		"manual-v1.txt": strings.Join(v1, "\n\n"),
		"manual-v2.txt": strings.Join(v2, "\n\n"),
		"manual-v3.txt": strings.Join(v3, "\n\n"),
		"other.txt":     strings.Join(prose(9, 40), "\n\n"),
		"stub-a.txt":    stub + "page a",
		"stub-b.txt":    stub + "page b",
	})
	for i, name := range []string{"manual-v1.txt", "manual-v2.txt", "manual-v3.txt"} {
		when := time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.Local)
		os.Chtimes(filepath.Join(src, name), when, when)
	}
	target := filepath.Join(filepath.Dir(src), "Out")
	h := &fakeHooks{}
	if _, err := Run(job.Job{Sources: []string{src}, Target: target, Duplicates: []string{job.Documents}}, h, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listTarget(t, target), ","); got != "manual-v3.txt,other.txt,stub-a.txt,stub-b.txt" {
		t.Errorf("target holds %s", got)
	}
	if h.lastSeen == nil || len(h.lastSeen.Sets) != 1 || h.lastSeen.Sets[0].Score < docThreshold {
		t.Fatalf("found %+v", h.lastSeen)
	}
}

// TestDistantRevisionsAreNotChained: v1 matches v2 and v2 matches v3, but v1 and
// v3 share too little. Keeping v3 must not skip v1 on v2's account: a third of
// v1's text is in neither of the files kept.
func TestDistantRevisionsAreNotChained(t *testing.T) {
	usePrivateConfig(t)
	v1 := prose(1, 12)
	v2 := revise(v1, 3, 2)
	v3 := revise(revise(v2, 7, 3), 10, 4)
	src := tree(t, map[string]string{
		"v1.txt": strings.Join(v1, "\n\n"),
		"v2.txt": strings.Join(v2, "\n\n"),
		"v3.txt": strings.Join(v3, "\n\n"),
	})
	for i, name := range []string{"v1.txt", "v2.txt", "v3.txt"} {
		when := time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.Local)
		os.Chtimes(filepath.Join(src, name), when, when)
	}
	target := filepath.Join(filepath.Dir(src), "Out")
	if _, err := Run(job.Job{Sources: []string{src}, Target: target, Duplicates: []string{job.Documents}}, &fakeHooks{}, nil); err != nil {
		t.Fatal(err)
	}
	got := listTarget(t, target)
	if !strings.Contains(strings.Join(got, ","), "v1.txt") || !strings.Contains(strings.Join(got, ","), "v3.txt") {
		t.Errorf("target holds %v; v1 and v3 must both survive", got)
	}
}

func TestMergeTextJoinsEachFolderAndLeavesTheRest(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{
		"Guide/a.html":   "<html><head><title>Start</title></head><body><p>Plug it in.</p></body></html>",
		"Guide/b.md":     "# Setup\n\nTurn the dial.",
		"Guide/c.txt":    "Clean the filter monthly.",
		"Guide/d.pdf":    "%PDF-1.4 not merged",
		"Guide/e.png":    "not text",
		"Guide/empty.md": "",
		"Solo/one.txt":   "The only document here.",
	})
	old := time.Date(2020, 5, 1, 12, 0, 0, 0, time.UTC)
	newest := time.Date(2024, 3, 9, 8, 30, 0, 0, time.UTC)
	for _, rel := range []string{"Guide/a.html", "Guide/b.md"} {
		os.Chtimes(filepath.Join(src, rel), old, old)
	}
	os.Chtimes(filepath.Join(src, "Guide/c.txt"), newest, newest)

	target := filepath.Join(filepath.Dir(src), "Out")
	j := job.Job{Sources: []string{src}, Target: target, Layout: job.ByDepth, Depth: 1, MergeText: true}
	sum, err := Run(j, &fakeHooks{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Merged != 3 {
		t.Errorf("merged %d documents, want 3: an empty one has no text to merge", sum.Merged)
	}
	got := strings.Join(listTarget(t, target), ",")
	want := "Guide/Guide 001.md,Guide/d.pdf,Guide/e.png,Guide/empty.md,Solo/one.txt"
	if got != want {
		t.Errorf("target holds %s\nwant %s", got, want)
	}
	body, err := os.ReadFile(filepath.Join(target, "Guide", "Guide 001.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"## Guide/a.html", "# Start", "Plug it in.", "## Guide/b.md", "Turn the dial.", "## Guide/c.txt"} {
		if !strings.Contains(string(body), s) {
			t.Errorf("merged file lacks %q:\n%s", s, body)
		}
	}
	if strings.Index(string(body), "Plug it in.") > strings.Index(string(body), "Turn the dial.") {
		t.Error("documents are not in plan order")
	}
	if info, _ := os.Stat(filepath.Join(target, "Guide", "Guide 001.md")); !info.ModTime().Equal(newest) {
		t.Errorf("merged file dated %v, want the newest document's %v", info.ModTime(), newest)
	}
	if _, err := os.Stat(filepath.Join(target, StateDirName, "merged")); !os.IsNotExist(err) {
		t.Error("the staging folder outlived the run")
	}
	csv, _ := os.ReadFile(filepath.Join(target, StateDirName, "report.csv"))
	if n := strings.Count(string(csv), "merged,"); n != 3 {
		t.Errorf("report.csv has %d merged rows, want 3:\n%s", n, csv)
	}
	if !strings.Contains(string(csv), "Guide/Guide 001.md") {
		t.Errorf("report.csv does not say where the documents went:\n%s", csv)
	}
	if !strings.Contains(string(csv), "not merged: ") {
		t.Errorf("report.csv does not say why the empty document stayed whole:\n%s", csv)
	}

	// Run again into the same folder, as a resume does: the merged file is written
	// the same, dated the same, and found already there.
	sum, err = Run(j, &fakeHooks{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Result.Existing != sum.Planned {
		t.Errorf("second run: %d of %d already there", sum.Result.Existing, sum.Planned)
	}
}

func TestMergeTextFlatUsesTheDestinationName(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{
		"a/one.txt": "First.",
		"b/two.txt": "Second.",
	})
	target := filepath.Join(filepath.Dir(src), "Manuals")
	sum, err := Run(job.Job{Sources: []string{src}, Target: target, MergeText: true, Batch: 50}, &fakeHooks{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listTarget(t, target), ","); got != "Batch 01/Manuals 001.md" || sum.Merged != 2 {
		t.Errorf("target holds %s, merged %d", got, sum.Merged)
	}
	csv, _ := os.ReadFile(filepath.Join(target, StateDirName, "report.csv"))
	if !strings.Contains(string(csv), "Batch 01/Manuals 001.md") {
		t.Errorf("report.csv names the merged file before batching moved it:\n%s", csv)
	}
}

func TestNewerKeepsTheLaterThenTheLargerDocument(t *testing.T) {
	day := time.Date(2025, 6, 29, 12, 0, 0, 0, time.UTC)
	source := flatten.Item{Src: "/d/git-add.adoc", Size: 16198, ModTime: day}
	html := flatten.Item{Src: "/d/git-add.html", Size: 53993, ModTime: day}
	if !newer(html, source) || newer(source, html) {
		t.Error("on one date, the larger rendering should be kept over its source")
	}
	later := flatten.Item{Src: "/d/old/longer/path.txt", Size: 10, ModTime: day.Add(time.Hour)}
	if !newer(later, html) {
		t.Error("a later revision should be kept however small")
	}
	a := flatten.Item{Src: "/d/a.txt", Size: 5, ModTime: day}
	b := flatten.Item{Src: "/d/sub/a.txt", Size: 5, ModTime: day}
	if !newer(a, b) || newer(b, a) {
		t.Error("on one date and size, the shorter path should be kept")
	}
}

func TestDetectTypesRenamesMisnamedFiles(t *testing.T) {
	usePrivateConfig(t)
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00"
	src := tree(t, map[string]string{
		"scrape/report.php": "<!DOCTYPE html><html><head><title>Q3</title></head><body><p>Numbers.</p></body></html>",
		"scrape/manual":     "%PDF-1.4\n%âãÏÓ\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n",
		"photos/IMG_1.JPG":  png,
		"photos/IMG_2.png":  png,
		"notes/README":      "plain words, no extension",
		"tools/setup.dat":   "MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff\x00\x00",
		"site/index.php":    "<?php require 'head.php'; ?>\n<!DOCTYPE html><html><body><?= $body ?></body></html>",
	})
	target := filepath.Join(filepath.Dir(src), "Out")
	sum, err := Run(job.Job{Sources: []string{src}, Target: target, Layout: job.ByDepth, Depth: 1, DetectTypes: true}, &fakeHooks{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(listTarget(t, target), ",")
	want := "notes/README,photos/IMG_1.png,photos/IMG_2.png,scrape/manual.pdf,scrape/report.html,site/index.php,tools/setup.dat"
	if got != want {
		t.Errorf("target holds %s\nwant %s", got, want)
	}
	if sum.Retyped != 3 {
		t.Errorf("retyped %d files, want 3", sum.Retyped)
	}
	csv, _ := os.ReadFile(filepath.Join(target, StateDirName, "report.csv"))
	for _, s := range []string{"type: the content is png, not .jpg", "type: the content is pdf; it had no extension", "type: the content is html, not .php"} {
		if !strings.Contains(string(csv), s) {
			t.Errorf("report.csv lacks %q:\n%s", s, csv)
		}
	}
}

func TestNoReportLeavesNoRecordButStaysResumable(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{"a.txt": "a", "b.txt": "b", "c.txt": "c"})
	target := filepath.Join(filepath.Dir(src), "Out")
	j := job.Job{Sources: []string{src}, Target: target, NoReport: true}

	stop := make(chan struct{})
	h := &fakeHooks{onCopy: func(c *CopyRun) flatten.Result {
		close(stop) // stopped before the first file
		return c.Run(c.Options)
	}}
	if _, err := Run(j, h, stop); !errors.Is(err, ErrStopped) {
		t.Fatalf("stopped run returned %v", err)
	}
	if !Interrupted(target) {
		t.Fatal("a stopped run without a report cannot be resumed")
	}

	sum, err := Run(j, &fakeHooks{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Report != "" {
		t.Errorf("summary names a report: %q", sum.Report)
	}
	if _, err := os.Stat(StateDir(target)); !os.IsNotExist(err) {
		t.Error("a finished run without a report left its record folder")
	}
	if got := strings.Join(listTarget(t, target), ","); got != "a.txt,b.txt,c.txt" {
		t.Errorf("target holds %s", got)
	}
}

func TestDecliningTheSpaceCheckLeavesNoDestination(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{"big.bin": strings.Repeat("x", 4096)})
	target := filepath.Join(filepath.Dir(src), "Out")
	old := freeSpace
	freeSpace = func(string) (int64, error) { return 100, nil }
	t.Cleanup(func() { freeSpace = old })
	elsewhere(t)
	h := &fakeHooks{space: false, onCopy: func(c *CopyRun) flatten.Result {
		t.Fatal("copied after the space check was declined")
		return flatten.Result{}
	}}
	j := job.Job{Sources: []string{src}, Target: target}
	if _, err := Run(j, h, nil); !errors.Is(err, ErrAbandoned) {
		t.Fatalf("declined run returned %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("the destination this run created is still there")
	}
}

func TestRenameTitlesNamesDocumentsAfterThemselves(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{
		"doc_4417.html": "<html><head><title>Configuring VLANs</title></head><body>…</body></html>",
		"a/index.md":    "# Getting Started\n\nPlug it in.",
		"b/index.md":    "# Getting Started\n\nThe other one.",
		"blank.html":    "<html><head><title>Untitled</title></head><body></body></html>",
		"report.php":    "<!DOCTYPE html><html><head><title>Q3 Numbers</title></head><body></body></html>",
		"photo.jpg":     "not a document",
	})
	target := filepath.Join(filepath.Dir(src), "Out")
	sum, err := Run(job.Job{Sources: []string{src}, Target: target, Layout: job.ByDepth, Depth: 1, RenameTitles: true, DetectTypes: true}, &fakeHooks{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(listTarget(t, target), ",")
	// photo.jpg holds text, and a picture's extension says otherwise: it is renamed.
	want := "Configuring VLANs.html,Q3 Numbers.html,a/Getting Started.md,b/Getting Started.md,blank.html,photo.txt"
	if got != want {
		t.Errorf("target holds %s\nwant %s", got, want)
	}
	if sum.Renamed != 4 {
		t.Errorf("renamed %d, want 4", sum.Renamed)
	}
	csv, _ := os.ReadFile(filepath.Join(target, StateDirName, "report.csv"))
	if !strings.Contains(string(csv), "renamed to its title; type: the content is html, not .php") {
		t.Errorf("report.csv does not record both changes to report.php:\n%s", csv)
	}
	md, _ := os.ReadFile(filepath.Join(target, StateDirName, "report.md"))
	if !strings.Contains(string(md), "`doc_4417.html` → `Configuring VLANs.html`") {
		t.Errorf("report.md does not list the rename:\n%s", md)
	}
}

func TestRenameTitlesCollideIntoSuffixes(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{
		"one.html":   "<title>Release Notes</title>",
		"two.html":   "<title>Release Notes</title>",
		"three.html": "<title>Home</title>",
	})
	target := filepath.Join(filepath.Dir(src), "Out")
	if _, err := Run(job.Job{Sources: []string{src}, Target: target, RenameTitles: true}, &fakeHooks{}, nil); err != nil {
		t.Fatal(err)
	}
	// "Home" names nothing, so three.html keeps its name.
	if got := strings.Join(listTarget(t, target), ","); got != "Release Notes.html,Release Notes_1.html,three.html" {
		t.Errorf("target holds %s", got)
	}
}

func TestMergedArchivePagesNameTheArchive(t *testing.T) {
	usePrivateConfig(t)
	src := tree(t, map[string]string{"Guides/intro.txt": "Read this first."})
	zf, _ := os.Create(filepath.Join(src, "Guides", "Manual.zip"))
	zw := zip.NewWriter(zf)
	for name, body := range map[string]string{"docs/setup.htm": "<title>Setup</title><p>Plug it in.</p>", "docs/faq.htm": "<p>Ask.</p>"} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	zf.Close()

	target := filepath.Join(filepath.Dir(src), "Out")
	if _, err := Run(job.Job{Sources: []string{src}, Target: target, Expand: true, MergeText: true}, &fakeHooks{}, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(target, "Out 001.md"))
	if err != nil {
		t.Fatalf("%v; target holds %s", err, strings.Join(listTarget(t, target), ","))
	}
	for _, s := range []string{"## Guides/Manual.zip!/docs/setup.htm", "## Guides/Manual.zip!/docs/faq.htm", "## Guides/intro.txt"} {
		if !strings.Contains(string(body), s) {
			t.Errorf("merged file lacks %q:\n%s", s, body)
		}
	}
	if strings.Contains(string(body), src) {
		t.Errorf("a heading names the absolute source path:\n%s", body)
	}
}
