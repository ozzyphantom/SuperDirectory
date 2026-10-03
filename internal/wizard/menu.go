package wizard

import (
	"errors"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	tea "github.com/charmbracelet/bubbletea"
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
}

func (m *menu) Init() tea.Cmd { return m.form.Init() }

func (m *menu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	return m.form.View() + "\n\n" + menuHelp(m.back) + "\n"
}

var (
	helpKey  = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	helpDesc = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// menuHelp renders the key hints in the pickers' style: keys in the accent color,
// actions dimmed.
func menuHelp(back bool) string {
	pairs := [][2]string{{"↑↓", "move"}, {"enter", "select"}}
	if back {
		pairs = append(pairs, [2]string{"esc", "back"})
	}
	pairs = append(pairs, [2]string{"ctrl+c", "quit"})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = helpKey.Render(p[0]) + " " + helpDesc.Render(p[1])
	}
	return "  " + strings.Join(parts, helpDesc.Render("   "))
}
