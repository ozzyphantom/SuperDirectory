package ui

import (
	"bytes"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ozzyphantom/SuperDirectory/internal/dedup"
	"github.com/ozzyphantom/SuperDirectory/internal/engine"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
)

// wide is a terminal with room for every part of the progress line.
const wide = 200

func TestHumanFormats(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 1_500_000: "1.5 MB", 4_200_000_000: "4.2 GB"} {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, want)
		}
	}
	if !strings.HasPrefix(humanRate(0), "—") || humanRate(58_300_000) != "58.3 MB/s" || humanRate(1_606_500_000) != "1.61 GB/s" {
		t.Error("humanRate")
	}
	for d, want := range map[time.Duration]string{120 * time.Millisecond: "0.1s", 59 * time.Second: "59s", 130 * time.Second: "2m10s", 2*time.Hour + 30*time.Minute: "2h30m"} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
	if thousands(11041) != "11,041" || thousands(999) != "999" || count(1, "file") != "1 file" || count(2041, "file") != "2,041 files" {
		t.Error("thousands/count")
	}
	long := "a-very-long-photograph-filename-from-2019.NEF"
	if got := truncateMiddle(long, 20); len([]rune(got)) != 20 || !strings.HasSuffix(got, ".NEF") {
		t.Errorf("truncateMiddle = %q", got)
	}
}

// TestRateMeterFollowsASlowdown is the point of the meter: a drive that throttles
// mid-copy must show a falling rate, not a comfortable lifetime average.
func TestRateMeterFollowsASlowdown(t *testing.T) {
	m := &rateMeter{}
	fast, _ := m.observe(1_000_000_000, 10*time.Second)
	if fast < 90e6 || fast > 110e6 {
		t.Fatalf("initial rate %.0f B/s, want ~100 MB/s", fast)
	}
	var slow float64
	for i := 0; i < 5; i++ {
		slow, _ = m.observe(int64(1_000_000_000+(i+1)*100_000_000/5), time.Duration(10+2*(i+1))*time.Second)
	}
	if slow > 40e6 {
		t.Errorf("smoothed rate %.0f B/s did not follow the slowdown", slow)
	}
}

func TestRateMeterDetectsAStall(t *testing.T) {
	m := &rateMeter{}
	m.observe(5_000_000, 2*time.Second)
	if _, stalled := m.observe(5_000_000, 20*time.Second); stalled != 18*time.Second {
		t.Errorf("stalled = %v, want 18s", stalled)
	}
	if _, stalled := m.observe(5_000_001, 21*time.Second); stalled != 0 {
		t.Errorf("stalled = %v after bytes moved", stalled)
	}
}

// TestETASaysHowSureItIs: the estimate states its stage — estimating, then rough
// while the rate moves, then plain once it has held — so a guess is never shown
// as a promise.
func TestETASaysHowSureItIs(t *testing.T) {
	const total = 100_000_000_000 // 100 GB at 100 MB/s: about 1000 s
	e := &estimator{}
	p := flatten.Progress{Done: 10, Total: 1000, Bytes: 100_000_000}
	if _, stage := e.eta(p, 100e6, total, 2*time.Second); stage != etaEstimating {
		t.Errorf("after 2s: stage %d, want estimating", stage)
	}
	if _, stage := e.eta(p, 100e6, total, 6*time.Second); stage != etaEstimating {
		t.Error("under 1% done is still estimating")
	}

	p.Bytes = 2_000_000_000
	var left time.Duration
	var stage etaStage
	for s := 8; s <= 40; s += 2 {
		left, stage = e.eta(p, 100e6, total, time.Duration(s)*time.Second)
	}
	if stage != etaSteady {
		t.Errorf("a steady 100 MB/s for 30s: stage %d, want steady", stage)
	}
	if left < 970*time.Second || left > 990*time.Second {
		t.Errorf("left = %v, want ~980s", left)
	}

	// The drive slows: the estimate goes back to rough.
	for s := 42; s <= 50; s += 2 {
		rate := 100e6
		if s%4 == 0 {
			rate = 30e6
		}
		_, stage = e.eta(p, rate, total, time.Duration(s)*time.Second)
	}
	if stage != etaRough {
		t.Errorf("a swinging rate: stage %d, want rough", stage)
	}
	if got := etaText(5*time.Minute, etaRough); !strings.Contains(got, "rough") {
		t.Errorf("rough text = %q", got)
	}
}

func frameFor(p flatten.Progress, stalled, paused time.Duration) frame {
	return frame{p: p, totalBytes: 50_000_000_000, rate: 38_000_000, stalled: stalled, paused: paused, left: 18 * time.Minute, stage: etaSteady}
}

func TestProgressLineShowsWhatMatters(t *testing.T) {
	p := flatten.Progress{Done: 1084, Total: 11041, Bytes: 1_200_000_000, Current: "DSC_4417.NEF", Elapsed: time.Minute}
	running := progressLine(frameFor(p, 0, 0), wide)
	for _, want := range []string{"1,084/11,041", "1.2 GB", "38.0 MB/s", "~18m00s left", "DSC_4417.NEF"} {
		if !strings.Contains(running, want) {
			t.Errorf("running line missing %q:\n%s", want, running)
		}
	}
	stuck := progressLine(frameFor(p, 47*time.Second, 0), wide)
	if !strings.Contains(stuck, "no data for 47s") || strings.Contains(stuck, "left") || !strings.Contains(stuck, "DSC_4417.NEF") {
		t.Errorf("stalled line:\n%s", stuck)
	}
	paused := progressLine(frameFor(p, 47*time.Second, 2*time.Minute+10*time.Second), wide)
	if !strings.Contains(paused, "paused 2m10s") || strings.Contains(paused, "no data") {
		t.Errorf("a paused copy is not a stalled one:\n%s", paused)
	}
}

// TestProgressLineFitsTheTerminal is the fix for a copy that filled an 80-column
// window with stale progress bars.
func TestProgressLineFitsTheTerminal(t *testing.T) {
	p := flatten.Progress{Done: 1084, Total: 11041, Bytes: 1_200_000_000, Elapsed: time.Minute,
		Current: "IMG_2019_summer_vacation_with_family_at_the_lake_house_1084.jpg"}
	for _, width := range []int{20, 40, 59, 79, 99, 139} {
		for _, stalled := range []time.Duration{0, 47 * time.Second} {
			if got := ansi.StringWidth(progressLine(frameFor(p, stalled, 0), width)); got > width {
				t.Errorf("width %d, stalled %v: line is %d columns", width, stalled, got)
			}
		}
	}
	if line := progressLine(frameFor(p, 0, 0), 79); !strings.Contains(line, "MB/s") || !strings.Contains(line, "left") {
		t.Errorf("80 columns should keep the rate and the estimate:\n%s", line)
	}
}

func TestScanStatusNamesEachStage(t *testing.T) {
	cases := map[dedup.Phase]string{
		dedup.Sizing:         "checking sizes  2048/11041",
		dedup.ReadingHeaders: "reading picture headers  2048/11041",
	}
	for phase, want := range cases {
		if got := scanStatus(dedup.Progress{Phase: phase, Done: 2048, Total: 11041}); got != want {
			t.Errorf("phase %d: %q, want %q", phase, got, want)
		}
	}
	big := scanStatus(dedup.Progress{Phase: dedup.Hashing, Done: 3, Total: 4, Current: "GX010042.MP4", Read: 1_200_000_000, Size: 4_000_000_000})
	if !strings.Contains(big, "1.2 GB of 4.0 GB") {
		t.Errorf("large read: %q", big)
	}
}

func TestDescribeFoundShowsEvidence(t *testing.T) {
	f := &engine.Found{
		Items: []flatten.Item{{Src: "/p/IMG_0042.jpg"}, {Src: "/p/web/IMG_0042-small.jpg"}, {Src: "/d/manual-v2.pdf"}, {Src: "/d/manual-v3.pdf"}},
		Sets: []engine.DupSet{
			{Kind: job.Pictures, Keep: 0, Skip: []int{1}, Bytes: 300_000},
			{Kind: job.Documents, Keep: 3, Skip: []int{2}, Bytes: 2_000_000, Score: 0.93},
		},
		Dims: map[int]dedup.Dims{0: {W: 4032, H: 3024}, 1: {W: 1600, H: 1200}},
	}
	title, desc, kinds := describeFound(f)
	if !strings.Contains(title, "1 smaller copies of pictures and 1 near-duplicate document(s)") || len(kinds) != 2 {
		t.Errorf("title %q, kinds %v", title, kinds)
	}
	for _, want := range []string{"IMG_0042-small.jpg 1600×1200  →  IMG_0042.jpg 4032×3024", "manual-v2.pdf  →  manual-v3.pdf  (93% the same)"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q:\n%s", want, desc)
		}
	}
}

// TestCopyScreenKeys: p pauses and resumes; the first Ctrl+C stops through the
// shared stop, and wakes a paused copy so it can see the stop; the second quits.
func TestCopyScreenKeys(t *testing.T) {
	stop := NewStopper()
	m := newCopyScreen(&engine.CopyRun{Target: "/t", Files: 10, Bytes: 1000}, stop)
	m.Update(progressMsg(flatten.Progress{Done: 2, Total: 10, Bytes: 200}))

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !m.pause.Paused() || !strings.Contains(m.View(), "resume") {
		t.Fatalf("p did not pause:\n%s", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !stop.Stopped() || m.pause.Paused() || !strings.Contains(m.View(), "Stopping") {
		t.Errorf("first ctrl+c: stopped %v, paused %v\n%s", stop.Stopped(), m.pause.Paused(), m.View())
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.forced || cmd == nil {
		t.Error("second ctrl+c should force an exit")
	}
}

func TestSummaryWording(t *testing.T) {
	var b bytes.Buffer
	PrintSummary(&b, engine.Summary{
		Job:     job.Job{Target: "/t/Photos-super"},
		Planned: 6, Stopped: true,
		Result: flatten.Result{Copied: 3, Failures: []flatten.Failure{{Src: "/s/d.jpg", Err: flatten.ErrCanceled}}},
	})
	out := ansi.Strip(b.String())
	for _, want := range []string{"Stopped.", "3 of 6 files copied", "partial copy of /s/d.jpg was removed", "resume"} {
		if !strings.Contains(out, want) {
			t.Errorf("stopped summary missing %q:\n%s", want, out)
		}
	}
	b.Reset()
	PrintSummary(&b, engine.Summary{
		Job: job.Job{Target: "/t", Batch: 50}, Planned: 120, Batches: 3, Elapsed: 2 * time.Second, Report: "/t/.superdirectory",
		Result:  flatten.Result{Copied: 100, Cloned: 10, Existing: 10, Bytes: 1_000_000},
		Skipped: []engine.DupSet{{Skip: []int{1, 2}}},
	})
	out = ansi.Strip(b.String())
	for _, want := range []string{"Finished!", "100 copied", "10 cloned", "10 already there", "2 duplicates skipped", "3 batch folders", "report.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("finished summary missing %q:\n%s", want, out)
		}
	}
}

// TestCloneOnlySummaryClaimsNoRate: a copy made of clones moved no data; it must
// not report a transfer rate, and must count the cloned size as done.
func TestCloneOnlySummaryClaimsNoRate(t *testing.T) {
	var b bytes.Buffer
	PrintSummary(&b, engine.Summary{
		Job: job.Job{Target: "/t"}, Planned: 1504, Elapsed: time.Second,
		Result:  flatten.Result{Cloned: 1504, ClonedBytes: 185_100_000},
		Skipped: []engine.DupSet{{Skip: []int{1}}},
	})
	out := ansi.Strip(b.String())
	for _, want := range []string{"185.1 MB", "cloned on the same volume", "1 duplicate skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "MB/s") {
		t.Errorf("a clone-only run claimed a rate:\n%s", out)
	}
}

// TestFinalFrameShowsEverythingDone: when the copy reports done, the last frame
// reads 100% with every file counted, cloned files included.
func TestFinalFrameShowsEverythingDone(t *testing.T) {
	m := newCopyScreen(&engine.CopyRun{Target: "/t", Files: 3, Bytes: 300}, NewStopper())
	m.Update(doneMsg(flatten.Result{Outcomes: []flatten.Outcome{flatten.Cloned, flatten.Copied, flatten.Existing}, ClonedBytes: 100}))
	if v := ansi.Strip(m.View()); !strings.Contains(v, "100%") || !strings.Contains(v, "3/3") {
		t.Errorf("final frame:\n%s", v)
	}
}

// TestOrientedTurnsWithoutCopying: a portrait stored sideways (orientation 6) must
// read upright through the wrapper: its bounds swap, and its top-left pixel is the
// stored image's bottom-left.
func TestOrientedTurnsWithoutCopying(t *testing.T) {
	stored := image.NewRGBA(image.Rect(0, 0, 4, 2)) // 4 wide, 2 tall
	stored.Set(0, 1, color.RGBA{255, 0, 0, 255})    // bottom-left: red
	stored.Set(3, 0, color.RGBA{0, 0, 255, 255})    // top-right: blue
	up := oriented{stored, 6}
	if b := up.Bounds(); b.Dx() != 2 || b.Dy() != 4 {
		t.Fatalf("bounds %v, want 2x4", b)
	}
	if r, _, _, _ := up.At(0, 0).RGBA(); r != 0xffff {
		t.Error("orientation 6: the top-left should be the stored bottom-left")
	}
	if _, _, b, _ := up.At(1, 3).RGBA(); b != 0xffff {
		t.Error("orientation 6: the bottom-right should be the stored top-right")
	}
	if same := (oriented{stored, 1}); same.Bounds().Dx() != 4 || same.At(3, 0) != stored.At(3, 0) {
		t.Error("orientation 1 must change nothing")
	}
}
