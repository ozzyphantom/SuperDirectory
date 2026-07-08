package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:             "0 B",
		999:           "999 B",
		1000:          "1.0 kB",
		1_500_000:     "1.5 MB",
		52_400_000:    "52.4 MB",
		4_200_000_000: "4.2 GB",
	}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestHumanRate(t *testing.T) {
	// A copy of zero-byte files has no meaningful rate, and must not print "0.0".
	if got := humanRate(0); !strings.HasPrefix(got, "—") {
		t.Errorf("humanRate(0) = %q, want an em-dash placeholder", got)
	}
	if got := humanRate(-5); !strings.HasPrefix(got, "—") {
		t.Errorf("humanRate(negative) = %q, want an em-dash placeholder", got)
	}
	if got := humanRate(58_300_000); got != "58.3 MB/s" {
		t.Errorf("humanRate = %q, want 58.3 MB/s", got)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		120 * time.Millisecond:                       "0.1s",
		3 * time.Second:                              "3s",
		59 * time.Second:                             "59s",
		130 * time.Second:                            "2m10s",
		time.Hour + 5*time.Minute:                    "1h05m",
		2*time.Hour + 30*time.Minute + 9*time.Second: "2h30m",
	}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestEstimateRemaining(t *testing.T) {
	// Too few files: an average drawn from one sample is not an estimate.
	if _, ok := estimateRemaining(flatten.Progress{Done: 1, Total: 100, Elapsed: time.Second}); ok {
		t.Error("should not estimate from a single file")
	}
	// Finished: nothing remains.
	if _, ok := estimateRemaining(flatten.Progress{Done: 10, Total: 10, Elapsed: time.Second}); ok {
		t.Error("should not estimate when the copy is done")
	}
	// 10 files in 10s => 1s each; 90 remain => ~90s.
	eta, ok := estimateRemaining(flatten.Progress{Done: 10, Total: 100, Elapsed: 10 * time.Second})
	if !ok || eta != 90*time.Second {
		t.Errorf("eta = %v (ok=%v), want 90s", eta, ok)
	}
	// A sub-second remainder is noise: "~0s left" tells the user nothing.
	if _, ok := estimateRemaining(flatten.Progress{Done: 11, Total: 12, Elapsed: 110 * time.Millisecond}); ok {
		t.Error("should suppress an ETA under one second")
	}
}

// TestRateMeterFollowsASlowdown is the point of the meter: a drive that throttles
// mid-copy must show a falling rate, not a comfortable lifetime average.
func TestRateMeterFollowsASlowdown(t *testing.T) {
	m := &rateMeter{}

	// Ten seconds at 100 MB/s.
	fast, _ := m.observe(flatten.Progress{Done: 1, Bytes: 1_000_000_000, Elapsed: 10 * time.Second})
	if fast < 90e6 || fast > 110e6 {
		t.Fatalf("initial rate %.0f B/s, want ~100 MB/s", fast)
	}

	// Then ten seconds at 10 MB/s. The reported rate must fall well below the
	// lifetime average, which is still ~55 MB/s.
	var slow float64
	for i := 0; i < 5; i++ {
		bytes := int64(1_000_000_000 + (i+1)*100_000_000/5)
		slow, _ = m.observe(flatten.Progress{
			Done:    2 + i,
			Bytes:   bytes,
			Elapsed: time.Duration(10+2*(i+1)) * time.Second,
		})
	}
	lifetime := float64(1_100_000_000) / 20.0 // ~55 MB/s
	if slow >= lifetime {
		t.Errorf("smoothed rate %.0f B/s did not drop below the lifetime average %.0f B/s "+
			"— a throttling drive would look fine", slow, lifetime)
	}
	if slow > 40e6 {
		t.Errorf("smoothed rate %.0f B/s is still too close to the fast phase", slow)
	}
}

// TestRateMeterIgnoresBursts: many tiny files landing in the same instant must not
// produce a nonsense instantaneous rate.
func TestRateMeterIgnoresBursts(t *testing.T) {
	m := &rateMeter{}
	first, _ := m.observe(flatten.Progress{Done: 1, Bytes: 10_000_000, Elapsed: time.Second})

	// A second sample 1ms later, below the resample interval.
	got, _ := m.observe(flatten.Progress{Done: 2, Bytes: 10_000_100, Elapsed: time.Second + time.Millisecond})
	if got != first {
		t.Errorf("sub-interval sample changed the rate: %.0f -> %.0f", first, got)
	}
}

func TestProgressLineShowsThroughput(t *testing.T) {
	p := flatten.Progress{Done: 105, Total: 250, Bytes: 1_900_000_000, Elapsed: 30 * time.Second}
	line := progressLine(p, 58_300_000, 0)

	for _, want := range []string{"42%", "105/250", "1.9 GB", "58.3 MB/s", "left"} {
		if !strings.Contains(line, want) {
			t.Errorf("progress line missing %q:\n%s", want, line)
		}
	}
	// The bar must never overflow when a copy completes.
	done := progressLine(flatten.Progress{Done: 7, Total: 7, Bytes: 100, Elapsed: time.Second}, 100, 0)
	if !strings.Contains(done, "100%") {
		t.Errorf("completed line should read 100%%:\n%s", done)
	}
	if strings.Contains(done, "left") {
		t.Errorf("completed line should not show an ETA:\n%s", done)
	}
}

// TestRateMeterDetectsAStall is the fix for a copy that looked hung. A file that
// blocks in read reports no new bytes; the meter must say how long it has been
// still, so the display can say so instead of freezing silently.
func TestRateMeterDetectsAStall(t *testing.T) {
	m := &rateMeter{}
	m.observe(flatten.Progress{Done: 1, Bytes: 5_000_000, Elapsed: 2 * time.Second})

	// Progress keeps being reported — Copy announces each file — but no bytes move.
	_, stalled := m.observe(flatten.Progress{Done: 1, Bytes: 5_000_000, Elapsed: 20 * time.Second})
	if stalled != 18*time.Second {
		t.Errorf("stalled = %v, want 18s measured from the last byte that moved", stalled)
	}

	// One byte lands: the stall clock resets.
	_, stalled = m.observe(flatten.Progress{Done: 1, Bytes: 5_000_001, Elapsed: 21 * time.Second})
	if stalled != 0 {
		t.Errorf("stalled = %v after bytes moved, want 0", stalled)
	}
}

// TestProgressLineNamesTheStuckFile: a frozen bar with no filename is what sent the
// user to `lsof`. The name must be on screen, and a stall must be called out.
func TestProgressLineNamesTheStuckFile(t *testing.T) {
	p := flatten.Progress{Done: 1084, Total: 11041, Bytes: 1_200_000_000, Current: "DSC_4417.NEF", Elapsed: time.Minute}

	running := progressLine(p, 38_000_000, 0)
	if !strings.Contains(running, "DSC_4417.NEF") {
		t.Errorf("running line should name the file in flight:\n%s", running)
	}
	if !strings.Contains(running, "left") {
		t.Errorf("running line should show an ETA:\n%s", running)
	}

	stuck := progressLine(p, 38_000_000, 47*time.Second)
	if !strings.Contains(stuck, "DSC_4417.NEF") {
		t.Errorf("stalled line must still name the file:\n%s", stuck)
	}
	if !strings.Contains(stuck, "no data for 47s") {
		t.Errorf("stalled line must say so, not silently freeze:\n%s", stuck)
	}
	if strings.Contains(stuck, "left") {
		t.Errorf("a stalled copy has no meaningful ETA:\n%s", stuck)
	}
}

func TestTruncateMiddle(t *testing.T) {
	// Short names pass through.
	if got := truncateMiddle("DSC_0001.NEF", 28); got != "DSC_0001.NEF" {
		t.Errorf("short name changed: %q", got)
	}
	// Long names keep their extension, which is what identifies the file.
	long := "a-very-long-photograph-filename-from-2019.NEF"
	got := truncateMiddle(long, 20)
	if len([]rune(got)) != 20 {
		t.Errorf("truncateMiddle(%q, 20) = %q (%d runes), want 20", long, got, len([]rune(got)))
	}
	if !strings.HasSuffix(got, ".NEF") {
		t.Errorf("extension lost: %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("no ellipsis: %q", got)
	}
}
