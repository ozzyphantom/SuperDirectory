package wizard

import (
	"errors"
	"os"

	"github.com/charmbracelet/huh"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ozzyphantom/SuperDirectory/internal/hint"
)

// Menu runs a one-question select in the app's theme. With back set, esc steps back
// a screen (returned as errBack, which Run handles); ctrl+c returns
// huh.ErrUserAborted.
//
// It exists because huh binds nothing to esc. Every menu ignored it while the
// pickers and the exclusion tree honored it, so the one key the help promised
// "everywhere" worked on half the screens. Menu also turns off the select's "/"
// filter — a text box behind a stray keypress, on a list of three choices — and
// draws the same key hints as the pickers, so every screen reads alike.
func Menu(sel *huh.Select[string], back bool) error {
	keys := huh.NewDefaultKeyMap()
	keys.Select.Filter.SetEnabled(false)
	form := huh.NewForm(huh.NewGroup(sel)).
		WithTheme(Theme()).
		WithKeyMap(keys).
		WithShowHelp(false)
	// Form.Run sets these itself; a form driven by another model must be told.
	form.SubmitCmd = tea.Quit
	form.CancelCmd = tea.Interrupt

	m := &menu{form: form, back: back}
	final, err := tea.NewProgram(m, tea.WithOutput(os.Stderr), tea.WithReportFocus()).Run()
	switch {
	case errors.Is(err, tea.ErrInterrupted):
		return huh.ErrUserAborted
	case err != nil:
		return err
	}
	fm := final.(*menu)
	switch {
	case fm.wentBack:
		return errBack
	case fm.form.State == huh.StateAborted:
		return huh.ErrUserAborted
	}
	return nil
}

// menu wraps a huh form to give esc a meaning.
type menu struct {
	form     *huh.Form
	back     bool
	wentBack bool
	width    int // terminal columns, for wrapping the key hints
}

func (m *menu) Init() tea.Cmd { return m.form.Init() }

func (m *menu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = ws.Width
	}
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeyEsc && m.back {
		m.wentBack = true
		return m, tea.Quit
	}
	f, cmd := m.form.Update(msg)
	if f, ok := f.(*huh.Form); ok {
		m.form = f
	}
	return m, cmd
}

func (m *menu) View() string {
	// A finished menu clears itself, as huh's own Run does.
	if m.wentBack || m.form.State != huh.StateNormal {
		return ""
	}
	return m.form.View() + "\n\n" + hint.Block(menuHints(m.back), m.width) + "\n"
}

// menuHints are the keys a menu answers to, in the pickers' style.
func menuHints(back bool) []hint.Pair {
	pairs := []hint.Pair{{Key: "↑↓", Action: "move"}, {Key: "enter", Action: "select"}}
	if back {
		pairs = append(pairs, hint.Pair{Key: "esc", Action: "back"})
	}
	return append(pairs, hint.Pair{Key: "ctrl+c", Action: "quit"})
}
