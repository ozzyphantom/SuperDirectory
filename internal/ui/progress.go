package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

// stallAfter is how long the byte counter may sit still before the display says so.
// A copy blocked on an unreadable sector can hold the kernel in a retry loop for
// minutes; without this the user sees only a progress bar that stopped, and no way
// to tell a broken file from a big one.
const stallAfter = 5 * time.Second

// rateMeter smooths throughput for display, and notices when it stops entirely.
//
// A lifetime average hides exactly what you want to see on an external drive — the
// moment it slows down, whether from thermal throttling, a run of small files, or a
// full write cache. It reports a recent rate instead.
type rateMeter struct {
	lastAt    time.Duration
	lastBytes int64
	rate      float64 // exponentially-smoothed bytes/sec

	// Stall tracking is deliberately separate from rate smoothing. The rate is only
	// resampled every 300ms, but a stall must be measured from the last byte that
	// actually moved, however long ago that was.
	seenBytes    int64
	lastMovement time.Duration
}

// observe folds one progress report into the meter, returning the smoothed rate and
// how long the byte counter has been frozen. at is the copy's active time, which
// stops while it is paused.
//
// Samples closer than resampleAfter are accumulated rather than measured, because a
// burst of tiny files arriving within a millisecond of each other produces a
// meaningless instantaneous rate.
func (m *rateMeter) observe(bytes int64, at time.Duration) (rate float64, stalled time.Duration) {
	const resampleAfter = 300 * time.Millisecond

	if bytes != m.seenBytes {
		m.seenBytes, m.lastMovement = bytes, at
	}
	stalled = at - m.lastMovement

	dt := at - m.lastAt
	if dt < resampleAfter && m.rate != 0 {
		return m.rate, stalled
	}
	if secs := dt.Seconds(); secs > 0 {
		instant := float64(bytes-m.lastBytes) / secs
		if m.rate == 0 {
			m.rate = instant
		} else {
			m.rate = 0.6*m.rate + 0.4*instant // favor history, follow real changes
		}
	}
	m.lastAt, m.lastBytes = at, bytes
	return m.rate, stalled
}

// etaStage is how much an estimate of the time left can be trusted, said on screen
// so a number is never presented as surer than it is.
type etaStage int

const (
	etaHidden     etaStage = iota // nothing worth showing
	etaEstimating                 // too early: the first seconds, the first 1%
	etaRough                      // the rate is still moving
	etaSteady                     // the rate has held for a while
)

// estimator turns the smoothed rate into a time left, and judges its stage.
//
// It estimates from bytes when the plan's size is known — the walk measures every
// file, so it nearly always is — which stays honest on a mixed tree where one
// video outweighs a thousand documents. The stage comes from how much the rate has
// varied: steady once it has held within 20% for ten seconds.
type estimator struct {
	rates  []float64 // recent smoothed rates, one per sampleEvery
	lastAt time.Duration
}

const (
	sampleEvery = 2 * time.Second
	samplesKept = 5
)

func (e *estimator) eta(p flatten.Progress, rate float64, totalBytes int64, active time.Duration) (time.Duration, etaStage) {
	if rate > 0 && active-e.lastAt >= sampleEvery {
		e.rates = append(e.rates, rate)
		if len(e.rates) > samplesKept {
			e.rates = e.rates[1:]
		}
		e.lastAt = active
	}
	if p.Done >= p.Total {
		return 0, etaHidden
	}
	if totalBytes <= 0 {
		// Sizes unknown: extrapolate from files done, and never call it steady.
		if p.Done < 3 || active <= 0 {
			return 0, etaEstimating
		}
		return active / time.Duration(p.Done) * time.Duration(p.Total-p.Done), etaRough
	}
	done := p.Bytes + p.ExistingBytes
	remaining := totalBytes - done
	if remaining <= 0 {
		return 0, etaHidden
	}
	if rate <= 0 || active < 5*time.Second || done < totalBytes/100 {
		return 0, etaEstimating
	}
	left := time.Duration(float64(remaining) / rate * float64(time.Second))
	if left < time.Second {
		return 0, etaHidden
	}
	if active < 20*time.Second || len(e.rates) < samplesKept || spread(e.rates) > 0.2 {
		return left, etaRough
	}
	return left, etaSteady
}

// spread is the coefficient of variation: standard deviation over mean.
func spread(xs []float64) float64 {
	var mean float64
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	if mean == 0 {
		return math.Inf(1)
	}
	var v float64
	for _, x := range xs {
		v += (x - mean) * (x - mean)
	}
	return math.Sqrt(v/float64(len(xs))) / mean
}

// frame is everything one drawing of the progress line shows.
type frame struct {
	p          flatten.Progress
	totalBytes int64
	rate       float64
	stalled    time.Duration
	paused     time.Duration // how long the copy has been paused; 0 when running
	left       time.Duration
	stage      etaStage
}

// fraction is how far along the copy is: by bytes when the size is known, which is
// what the time left is based on, else by files.
func (f frame) fraction() float64 {
	if f.p.Total <= 0 {
		return 0
	}
	if f.p.Done >= f.p.Total {
		return 1
	}
	if f.totalBytes > 0 {
		return math.Min(0.999, float64(f.p.Bytes+f.p.ExistingBytes)/float64(f.totalBytes))
	}
	return float64(f.p.Done) / float64(f.p.Total)
}

// progressLine builds the progress display, fitted to width columns.
//
// Fitting matters because a line drawn in place with a carriage return wraps when
// it is wider than the terminal, the return lands on the wrapped half, and every
// frame strands a stale copy of the bar above it — hundreds over a long copy in a
// default 80-column window. So the line degrades in order: the bar shrinks first
// (the percentage beside it carries the same information), then the least useful
// parts go. A stall keeps its warning and the name of the file it is stuck on to
// the last, because those are the whole message.
func progressLine(f frame, width int) string {
	p := f.p
	if p.Total <= 0 {
		return ""
	}
	isStalled := f.paused == 0 && f.stalled >= stallAfter
	name := ""
	if p.Current != "" && p.Done < p.Total {
		name = p.Current
	}
	frac := f.fraction()

	render := func(l lineLayout) string {
		filled := int(float64(l.bar) * frac)
		bar := green.Render(strings.Repeat("█", filled)) + dim.Render(strings.Repeat("░", l.bar-filled))
		line := fmt.Sprintf("  [%s] %3d%%  %s/%s", bar, int(frac*100), thousands(p.Done), thousands(p.Total))
		if l.bytes {
			line += "  " + dim.Render(HumanBytes(p.Bytes+p.ExistingBytes))
		}
		if l.rate {
			if f.paused > 0 {
				line += "  " + bold.Render(humanRate(0))
			} else {
				line += "  " + bold.Render(humanRate(f.rate))
			}
		}
		switch {
		case f.paused > 0:
			line += orange.Render("  paused " + humanDuration(f.paused))
		case isStalled:
			// Say it loudly, and say what it is stuck on. This is the difference
			// between "the app hung" and "this one file will not read".
			line += red.Render(fmt.Sprintf("  ⚠ no data for %s", humanDuration(f.stalled)))
		case l.eta:
			line += dim.Render(etaText(f.left, f.stage))
		}
		if l.name > 0 && name != "" {
			line += "  " + dim.Render(truncateMiddle(name, l.name))
		}
		return line
	}

	for _, l := range progressLayouts(isStalled) {
		if line := render(l); ansi.StringWidth(line) <= width {
			return line
		}
	}
	return ansi.Truncate(render(lineLayout{bar: 5}), width, "")
}

// etaText words an estimate with its stage, so a guess never looks like a promise.
func etaText(left time.Duration, stage etaStage) string {
	switch stage {
	case etaEstimating:
		return "  estimating…"
	case etaRough:
		return "  ~" + humanDuration(left) + " left, rough"
	case etaSteady:
		return "  ~" + humanDuration(left) + " left"
	}
	return ""
}

// lineLayout is one way to draw the progress line: how many bar cells, how many
// runes of the file name (0 hides it), and which optional parts to show.
type lineLayout struct {
	bar, name        int
	eta, bytes, rate bool
}

// progressLayouts lists the ways to draw the line, from the full display to the
// barest, in the order they are tried.
func progressLayouts(stalled bool) []lineLayout {
	var out []lineLayout
	for bar := 30; bar >= 12; bar -= 6 {
		out = append(out, lineLayout{bar: bar, name: 28, eta: true, bytes: true, rate: true})
	}
	if stalled {
		// The rate and byte count are frozen anyway; the file name is the message.
		return append(out,
			lineLayout{bar: 12, name: 28, rate: true},
			lineLayout{bar: 12, name: 28},
			lineLayout{bar: 12, name: 16},
			lineLayout{bar: 5, name: 12},
		)
	}
	return append(out,
		lineLayout{bar: 12, name: 16, eta: true, bytes: true, rate: true},
		lineLayout{bar: 12, eta: true, bytes: true, rate: true},
		lineLayout{bar: 12, eta: true, rate: true},
		lineLayout{bar: 12, rate: true},
		lineLayout{bar: 5},
	)
}
