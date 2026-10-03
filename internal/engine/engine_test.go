package engine

import (
	"errors"
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

	src := tree(t, map[string]string{"big.bin": strings.Repeat("x", 5000)})
	// A different "volume" is needed for the check to apply; make clones look
	// impossible by pointing the target at a path the test claims is elsewhere.
	target := filepath.Join(filepath.Dir(src), "Out")
	h := &fakeHooks{space: false}
	r := &run{j: job.Job{Sources: []string{src}, Target: target}, h: h, notes: map[string]string{}}
	items := []flatten.Item{{Src: filepath.Join(src, "big.bin"), Dst: "big.bin", Size: 5000}}
	if r.clones() {
		t.Skip("source and target share a volume here, where clones make the check moot")
	}
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
