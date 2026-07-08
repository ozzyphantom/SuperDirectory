// Package pick is a keyboard-driven directory browser built on Bubble Tea.
//
// It replaces huh's FilePicker in the wizard so the source and destination
// screens behave predictably:
//
//   - Typing never drops you into a raw text prompt. Letters jump the cursor to
//     the next matching folder; the only text entry is the explicit, labelled
//     "name the new folder" step used when choosing a save location.
//   - There is always a way back: ← moves up a directory, esc leaves the picker
//     to the previous wizard step.
//   - It opens wherever the caller points it (the wizard starts the source at
//     the user's home directory, not the filesystem root).
//
// The model mirrors package exclude: an Elm-style Init/Update/View over a
// single directory level, reloaded as you move up and down the tree.
package pick

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ErrCanceled means the user quit outright (ctrl+c / q). ErrBack means they
// asked to step back to the previous screen (esc).
var (
	ErrCanceled = errors.New("selection canceled")
	ErrBack     = errors.New("go back")
)

var (
	titleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	pathStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	keyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	nameStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#2ecc71")).Bold(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#e74c3c"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// Options configure a picker.
type Options struct {
	// Title is the heading shown above the listing.
	Title string
	// Start is the directory the picker opens in.
	Start string
	// NameEntry, when true, turns choosing a folder into a two-part action:
	// the folder currently being browsed becomes the parent, then the user
	// names a new subdirectory. Run returns filepath.Join(parent, name).
	NameEntry bool
	// NameDefault prefills the name field in NameEntry mode.
	NameDefault string
	// Validate checks a (parent, name) choice in NameEntry mode. A non-nil
	// error keeps the user in the name field with the message shown.
	Validate func(parent, name string) error
}

// Run opens the browser and blocks until the user chooses, steps back, or
// quits. On success it returns the chosen absolute path.
func Run(opts Options) (string, error) {
	start := opts.Start
	if info, err := os.Stat(start); err != nil || !info.IsDir() {
		if h, err := os.UserHomeDir(); err == nil {
			start = h
		} else {
			start = string(filepath.Separator)
		}
	}
	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}

	m := &model{opts: opts, dir: start, height: 15}
	m.load()

	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return "", err
	}
	fm := final.(*model)
	switch {
	case fm.canceled:
		return "", ErrCanceled
	case fm.back:
		return "", ErrBack
	default:
		return fm.result, nil
	}
}

// subdirsUnknown marks an entry whose subfolder count has not arrived yet. It
// renders as no hint at all — never as zero, which would wrongly claim the folder
// is a leaf.
const subdirsUnknown = -1

type entry struct {
	name    string
	subdirs int // subdirsUnknown until a background count lands; see countVisibleCmd
}

type model struct {
	opts    Options
	dir     string
	items   []entry
	cursor  int
	offset  int
	height  int
	loadErr error // set when the current directory could not be read

	// counts memoizes subfolder counts by absolute path, so re-entering a
	// directory — or scrolling back over a row — costs nothing. The browser is
	// read-only and short-lived, so entries are never invalidated. Only the
	// Bubble Tea update loop touches this map.
	counts map[string]int

	// gen increments on every navigation. A background count carries the
	// generation it was started for; if the user has moved on, the worker stops
	// mid-flight and its result is discarded. Without this, hopping quickly
	// through ten directories would leave ten workers competing for the disk.
	gen atomic.Int64

	// name-entry sub-mode
	naming bool
	name   []rune
	errMsg string

	result   string
	back     bool
	canceled bool
}

// Init kicks off the first background count, for the directory Run opened on.
func (m *model) Init() tea.Cmd { return m.countVisibleCmd() }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height - 10
		if m.height < 3 {
			m.height = 3
		}
		// A taller window reveals rows that were never counted.
		m.clampScroll()
		return m, m.countVisibleCmd()
	case countsMsg:
		m.applyCounts(msg)
		return m, nil
	case tea.KeyMsg:
		if m.naming {
			return m.updateNaming(msg)
		}
		return m.updateBrowse(msg)
	}
	m.clampScroll()
	return m, nil
}

// updateBrowse handles keys while navigating the tree.
func (m *model) updateBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Navigation is on the arrow keys so every letter stays free for
	// type-to-jump (you can hop to "Library" by pressing l, or a folder that
	// starts with q by pressing q). Only ctrl+c quits; esc steps back.
	switch msg.String() {
	case "ctrl+c":
		m.canceled = true
		return m, tea.Quit
	case "esc":
		m.back = true
		return m, tea.Quit
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case "right":
		m.descend()
	case "left", "backspace":
		m.ascend()
	case "enter":
		if m.loadErr != nil {
			return m, nil // can't choose a folder we can't even read
		}
		if m.opts.NameEntry {
			m.naming = true
			m.name = []rune(m.opts.NameDefault)
			m.errMsg = ""
		} else {
			m.result = m.dir
			return m, tea.Quit
		}
	default:
		if r, ok := singleRune(msg); ok {
			m.jump(r)
		}
	}
	m.clampScroll()
	// Scrolling, jumping, or descending may have exposed uncounted rows.
	return m, m.countVisibleCmd()
}

// updateNaming handles keys while typing a new folder name.
func (m *model) updateNaming(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.canceled = true
		return m, tea.Quit
	case "esc":
		// Back out to browsing, not out of the picker.
		m.naming = false
		m.errMsg = ""
	case "enter":
		name := strings.TrimSpace(string(m.name))
		if name == "" {
			m.errMsg = "name cannot be empty"
			return m, nil
		}
		if m.opts.Validate != nil {
			if err := m.opts.Validate(m.dir, name); err != nil {
				m.errMsg = err.Error()
				return m, nil
			}
		}
		m.result = filepath.Join(m.dir, name)
		return m, tea.Quit
	case "backspace":
		if len(m.name) > 0 {
			m.name = m.name[:len(m.name)-1]
			m.errMsg = ""
		}
	case " ", "space":
		m.name = append(m.name, ' ')
		m.errMsg = ""
	default:
		if msg.Type == tea.KeyRunes {
			m.name = append(m.name, msg.Runes...)
			m.errMsg = ""
		}
	}
	return m, nil
}

func (m *model) View() string {
	var b strings.Builder
	b.WriteString("\n  " + titleStyle.Render(m.opts.Title) + "\n")
	b.WriteString("  " + dimStyle.Render("Location  ") + pathStyle.Render(m.dir) + "\n\n")

	switch {
	case m.loadErr != nil:
		reason := "cannot be opened"
		if os.IsPermission(m.loadErr) {
			reason = "permission denied"
		}
		b.WriteString("  " + errStyle.Render("⚠ can't read this folder ("+reason+") — press ← to go back") + "\n")
	case len(m.items) == 0:
		b.WriteString("  " + dimStyle.Render("(no subfolders here — press enter to choose this folder)") + "\n")
	}

	end := m.offset + m.height
	if end > len(m.items) {
		end = len(m.items)
	}
	for i := m.offset; i < end; i++ {
		b.WriteString(m.renderItem(i))
	}
	if len(m.items) > m.height {
		b.WriteString("  " + dimStyle.Render(fmt.Sprintf("     %d–%d of %d", m.offset+1, end, len(m.items))) + "\n")
	}

	if m.naming {
		b.WriteString(m.renderNaming())
	}

	b.WriteString("\n  " + m.helpLine() + "\n")
	return b.String()
}

func (m *model) renderItem(i int) string {
	it := m.items[i]
	pointer := "  "
	if i == m.cursor {
		pointer = cursorStyle.Render("❯ ")
	}
	sub := ""
	if it.subdirs > 0 {
		sub = dimStyle.Render(fmt.Sprintf("  (%d subfolders)", it.subdirs))
	}
	arrow := dimStyle.Render("›")
	return "  " + pointer + arrow + " " + it.name + sub + "\n"
}

func (m *model) renderNaming() string {
	preview := filepath.Join(m.dir, strings.TrimSpace(string(m.name)))
	var b strings.Builder
	b.WriteString("\n  " + titleStyle.Render("Name the new folder") + "\n")
	b.WriteString("  " + nameStyle.Render(string(m.name)) + cursorStyle.Render("▏") + "\n")
	b.WriteString("  " + dimStyle.Render("Creates  "+preview) + "\n")
	if m.errMsg != "" {
		b.WriteString("  " + errStyle.Render("✗ "+m.errMsg) + "\n")
	}
	return b.String()
}

func (m *model) helpLine() string {
	var pairs [][2]string
	if m.naming {
		pairs = [][2]string{
			{"type", "a name"},
			{"enter", "create"},
			{"esc", "back"},
			{"ctrl+c", "quit"},
		}
	} else {
		pairs = [][2]string{
			{"↑↓", "move"},
			{"a–z", "jump"},
			{"→", "open"},
			{"←", "up"},
			{"enter", "choose this folder"},
			{"esc", "back"},
			{"ctrl+c", "quit"},
		}
	}
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = keyStyle.Render(p[0]) + " " + dimStyle.Render(p[1])
	}
	return strings.Join(parts, dimStyle.Render("   "))
}

// ── navigation ───────────────────────────────────────────────────────────

// descend opens the highlighted folder.
func (m *model) descend() {
	if len(m.items) == 0 {
		return
	}
	m.enter(filepath.Join(m.dir, m.items[m.cursor].name))
}

// ascend moves up to the parent directory, keeping the cursor on the folder we
// came out of.
func (m *model) ascend() {
	parent := filepath.Dir(m.dir)
	if parent == m.dir {
		return // already at the root
	}
	child := filepath.Base(m.dir)
	m.enter(parent)
	for i, it := range m.items {
		if it.name == child {
			m.cursor = i
			break
		}
	}
}

// enter switches to dir and reloads the listing from the top.
// enter moves to dir and invalidates any background count still running for the
// directory we are leaving.
func (m *model) enter(dir string) {
	m.gen.Add(1)
	m.dir = dir
	m.cursor = 0
	m.offset = 0
	m.load()
}

// jump moves the cursor to the next folder whose name starts with r, wrapping
// around. It is the type-to-jump that keeps letters from opening a text field.
func (m *model) jump(r rune) {
	r = unicode.ToLower(r)
	n := len(m.items)
	for off := 1; off <= n; off++ {
		i := (m.cursor + off) % n
		name := m.items[i].name
		if name != "" && unicode.ToLower([]rune(name)[0]) == r {
			m.cursor = i
			return
		}
	}
}

// load reads the current directory — one directory read, and nothing else. It
// does not touch any child, so the cost of a hop does not depend on how many
// files the folders inside it happen to hold. Subfolder counts arrive later, from
// countVisibleCmd, off the critical path.
func (m *model) load() {
	if m.counts == nil {
		m.counts = map[string]int{}
	}
	entries, err := os.ReadDir(m.dir)
	m.loadErr = err
	if err != nil {
		m.items = nil
		return
	}
	var items []entry
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		items = append(items, entry{name: e.Name(), subdirs: subdirsUnknown})
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].name) < strings.ToLower(items[j].name)
	})
	m.items = items
	m.clampScroll() // resolves anything already memoized; no disk access
}

// countsMsg carries the result of a background count back to the update loop.
type countsMsg struct {
	dir    string         // the directory the counts were taken in
	gen    int64          // the navigation generation they were started for
	counts map[string]int // child name -> its subfolder count
}

// fillFromCache resolves whatever the memo already knows. Pure: no disk access,
// so it is safe to call on every keystroke.
func (m *model) fillFromCache() {
	for i := range m.items {
		if m.items[i].subdirs != subdirsUnknown {
			continue
		}
		if n, ok := m.counts[filepath.Join(m.dir, m.items[i].name)]; ok {
			m.items[i].subdirs = n
		}
	}
}

// countVisibleCmd counts the on-screen rows that are still unknown, in the
// background.
//
// Counting a child means reading its whole directory: a folder holding 800 files
// and one subfolder costs 801 entries to learn the number "1". Doing that for
// fifteen rows before the first frame is what made an external drive feel slower
// the deeper you went — deeper folders hold more files. So navigation now costs
// exactly one directory read (the one you entered), and the subfolder hints arrive
// a moment later.
//
// Returns nil when there is nothing to do, which Bubble Tea treats as a no-op.
func (m *model) countVisibleCmd() tea.Cmd {
	end := min(m.offset+m.height, len(m.items))
	var names []string
	for i := m.offset; i < end; i++ {
		if m.items[i].subdirs == subdirsUnknown {
			names = append(names, m.items[i].name)
		}
	}
	if len(names) == 0 {
		return nil
	}

	dir, gen := m.dir, m.gen.Load()
	return func() tea.Msg {
		out := make(map[string]int, len(names))
		for _, name := range names {
			// Cheap to check, and it stops a stale worker from reading the rest
			// of a slow directory nobody is looking at any more.
			if m.gen.Load() != gen {
				return nil
			}
			out[name] = countSubdirs(filepath.Join(dir, name))
		}
		return countsMsg{dir: dir, gen: gen, counts: out}
	}
}

// applyCounts folds a background result into the memo, ignoring it if the user
// has navigated since it was started.
func (m *model) applyCounts(msg countsMsg) {
	if msg.gen != m.gen.Load() || msg.dir != m.dir {
		return
	}
	for name, n := range msg.counts {
		m.counts[filepath.Join(msg.dir, name)] = n
	}
	m.fillFromCache()
}

func (m *model) clampScroll() {
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.height {
		m.offset = m.cursor - m.height + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
	// Resolve anything already memoized. This touches no disk; rows still unknown
	// are picked up by countVisibleCmd, which runs in the background.
	m.fillFromCache()
}

// countSubdirs reports how many immediate subdirectories dir has, so the
// listing can hint which folders open further. Errors count as zero.
func countSubdirs(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			n++
		}
	}
	return n
}

// singleRune reports whether msg is a single printable rune keypress, and
// returns it. Used for type-to-jump.
func singleRune(msg tea.KeyMsg) (rune, bool) {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return 0, false
	}
	r := msg.Runes[0]
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return r, true
	}
	return 0, false
}
