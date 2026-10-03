package ui

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ozzyphantom/SuperDirectory/internal/engine"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/hint"
)

// copyScreen is the copy's display: a heading, the progress line, and the keys. It
// is drawn inline rather than on the alternate screen, so its last frame stays in
// the scrollback above the summary.
type copyScreen struct {
	run   *engine.CopyRun
	stop  *Stopper
	pause *flatten.Pauser
	width int

	started     time.Time
	pausedTotal time.Duration // pauses that have ended
	p           flatten.Progress
	meter       rateMeter
	est         estimator
	f           frame

	stopping bool
	done     bool
	forced   bool // a second Ctrl+C: quit now
	result   flatten.Result
}

type (
	progressMsg flatten.Progress
	doneMsg     flatten.Result
	tickMsg     time.Time
)

func newCopyScreen(run *engine.CopyRun, stop *Stopper) *copyScreen {
	return &copyScreen{
		run: run, stop: stop, pause: &flatten.Pauser{}, started: time.Now(), width: termWidth() + 1,
		p: flatten.Progress{Total: run.Files},
	}
}

func tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *copyScreen) Init() tea.Cmd { return tick() }

// active is how long the copy has been running, not counting pauses: the rate and
// the stall clock both measure working time.
func (m *copyScreen) active() time.Duration {
	a := time.Since(m.started) - m.pausedTotal
	if since := m.pause.Since(); !since.IsZero() {
		a -= time.Since(since)
	}
	return a
}

func (m *copyScreen) refresh() {
	at := m.active()
	m.f = frame{p: m.p, totalBytes: m.run.Bytes}
	m.f.rate, m.f.stalled = m.meter.observe(m.p.Bytes, at)
	m.f.left, m.f.stage = m.est.eta(m.p, m.f.rate, m.run.Bytes, at)
	if since := m.pause.Since(); !since.IsZero() {
		m.f.paused = time.Since(since)
	}
}

func (m *copyScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case progressMsg:
		m.p = flatten.Progress(msg)
		m.refresh()
	case doneMsg:
		m.result = flatten.Result(msg)
		m.done = true
		m.p.Done = countNot(m.result.Outcomes, flatten.NotReached)
		m.p.ClonedBytes = m.result.ClonedBytes
		m.refresh()
		return m, tea.Quit
	case tickMsg:
		if m.done {
			return m, nil
		}
		m.refresh()
		return m, tick()
	case tea.KeyMsg:
		switch msg.String() {
		case "p", "P":
			if !m.stopping {
				if since := m.pause.Since(); !since.IsZero() {
					m.pausedTotal += time.Since(since)
				}
				m.pause.Toggle()
				m.refresh()
			}
		case "ctrl+c":
			if m.stopping {
				m.forced = true
				return m, tea.Quit
			}
			m.stopping = true
			if m.pause.Paused() {
				m.pause.Toggle() // a paused copy must wake to see the stop
			}
			m.stop.Stop()
		}
	}
	return m, nil
}

func countNot(outcomes []flatten.Outcome, o flatten.Outcome) int {
	n := 0
	for _, x := range outcomes {
		if x != o {
			n++
		}
	}
	return n
}

func (m *copyScreen) View() string {
	w := m.width - 1
	if w < 20 {
		w = 20
	}
	head := fmt.Sprintf("  Copying %s (%s) into %s", count(m.run.Files, "file"), HumanBytes(m.run.Bytes), m.run.Target)
	if m.run.Retry > 0 {
		head = fmt.Sprintf("  Retrying %s, attempt %d", count(m.run.Files, "failed file"), m.run.Retry)
	}
	out := hint.Fit(head, w) + "\n\n" + progressLine(m.f, w) + "\n"
	if m.done {
		return out
	}
	var keys []hint.Pair
	switch {
	case m.stopping:
		return out + "\n  " + orange.Render("Stopping… the file in flight is removed if it is incomplete.") + "\n"
	case m.pause.Paused():
		keys = []hint.Pair{{Key: "p", Action: "resume"}, {Key: "ctrl+c", Action: "stop"}}
	default:
		keys = []hint.Pair{{Key: "p", Action: "pause"}, {Key: "ctrl+c", Action: "stop"}}
	}
	return out + "\n" + hint.Block(keys, w) + "\n"
}

// runCopyScreen runs one copy pass under the screen. It returns the result, and
// false when the user forced an exit with a second Ctrl+C.
func runCopyScreen(c *engine.CopyRun, stop *Stopper) (flatten.Result, bool) {
	m := newCopyScreen(c, stop)
	prog := tea.NewProgram(m, tea.WithOutput(os.Stdout))

	opts := c.Options
	opts.Pause = m.pause
	// The copier reports thousands of times a second on a folder of small files;
	// the screen needs twenty frames. Drop the rest before they reach the loop.
	var last atomic.Int64
	opts.OnProgress = func(p flatten.Progress) {
		now := time.Now().UnixNano()
		if now-last.Load() < int64(50*time.Millisecond) && p.Done < p.Total {
			return
		}
		last.Store(now)
		prog.Send(progressMsg(p))
	}
	result := make(chan flatten.Result, 1)
	go func() {
		res := c.Run(opts)
		result <- res
		prog.Send(doneMsg(res))
	}()
	if _, err := prog.Run(); err != nil {
		stop.Stop()
	}
	if m.forced {
		return flatten.Result{}, false
	}
	return <-result, true
}
