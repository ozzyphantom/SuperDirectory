// Package exclude is the interactive, recursive directory-exclusion UI, built
// as a Bubble Tea model. It replaces the Python app's level-by-level menu with
// a single navigable tree: descend to any depth, mark directories to skip, and
// see the excluded subtree highlighted live. Directories load lazily on expand,
// so it stays responsive on trees with hundreds of subdirectories.
package exclude

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ozzyphantom/SuperDirectory/internal/fsmeta"
)

// ErrCanceled is returned when the user quits the tree outright (q / Ctrl+C).
// ErrBack is returned when they step back to the previous screen (Esc).
var (
	ErrCanceled = errors.New("exclusion canceled")
	ErrBack     = errors.New("exclusion back")
)

var (
	cursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	titleStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	keyStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true) // keys in help
	arrowStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8"))            // expand arrows
	excludedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))                // orange, matching the app
	keptStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#2ecc71"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// node is one directory in the lazily-loaded tree.
type node struct {
	path        string
	name        string
	depth       int
	parent      *node
	children    []*node
	loaded      bool
	expanded    bool
	hasSubdirs  bool
	fileCount   int
	subdirCount int

	// counted guards the three fields above; counting means a background worker is
	// already fetching them. Establishing them costs a full directory read, so it
	// happens off the render path — see countVisibleCmd.
	counted  bool
	counting bool

	// previewFiles memoizes the node's file listing for the preview pane.
	// previewHeight and renderPreview both need it on every frame; reading the
	// directory twice per keystroke is imperceptible locally and painful on a
	// USB drive.
	previewFiles  []string
	previewLoaded bool
}

// countsMsg carries background count results back to the update loop. Nodes are
// stable objects, so a result is always safe to apply, even if the row has since
// scrolled off screen.
type countsMsg struct{ results []nodeCount }

type nodeCount struct {
	node        *node
	files, dirs int
}

// countVisibleCmd counts the on-screen rows whose numbers are still unknown,
// in the background.
//
// Counting a node means reading its whole directory: a folder holding 800 files
// and one subfolder costs 801 entries to learn "1 dir". Doing that inside View,
// for every visible row, made the tree stall on an external drive — and stall
// worse the deeper you went, because deeper folders hold more files.
//
// Each node is dispatched at most once, so scrolling cannot pile up duplicate
// work. The worker only reads n.path, which never changes; every write to a node
// happens back on the update loop in applyCounts.
func (m *model) countVisibleCmd() tea.Cmd {
	rows := m.listRows()
	end := min(m.offset+rows, len(m.visible))

	var todo []*node
	for i := m.offset; i < end; i++ {
		n := m.visible[i]
		if !n.counted && !n.counting {
			n.counting = true
			todo = append(todo, n)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	return func() tea.Msg {
		results := make([]nodeCount, 0, len(todo))
		for _, n := range todo {
			f, d := countChildren(n.path)
			results = append(results, nodeCount{node: n, files: f, dirs: d})
		}
		return countsMsg{results: results}
	}
}

func (m *model) applyCounts(msg countsMsg) {
	for _, r := range msg.results {
		r.node.counted = true
		r.node.counting = false
		r.node.fileCount, r.node.subdirCount, r.node.hasSubdirs = r.files, r.dirs, r.dirs > 0
	}
}

// previewFilesOf returns the node's copyable files, reading the directory at most
// once for the life of the tree.
func previewFilesOf(n *node) []string {
	if n.previewLoaded {
		return n.previewFiles
	}
	n.previewLoaded = true
	entries, err := os.ReadDir(n.path)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() && !fsmeta.IsMetadata(e.Name()) {
			n.previewFiles = append(n.previewFiles, e.Name())
		}
	}
	sort.Strings(n.previewFiles)
	return n.previewFiles
}

type model struct {
	root     *node
	visible  []*node // flattened, in display order
	cursor   int
	offset   int // scroll offset into visible
	height   int // rows available for the list
	excluded map[string]bool
	preview  *node
	canceled bool
	back     bool
	source   string
}

// Run opens the tree for source and returns the set of absolute directory
// paths the user chose to exclude, or ErrCanceled/ErrBack if they left. The
// preselected set seeds the tree, so re-opening it (e.g. after stepping back
// from the confirm screen) preserves earlier choices instead of starting empty.
func Run(source string, preselected map[string]bool) (map[string]bool, error) {
	final, err := tea.NewProgram(newModel(source, preselected), tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	m := final.(*model)
	switch {
	case m.canceled:
		return nil, ErrCanceled
	case m.back:
		return nil, ErrBack
	default:
		return m.excluded, nil
	}
}

func newModel(source string, preselected map[string]bool) *model {
	excluded := map[string]bool{}
	for path, on := range preselected {
		if on {
			excluded[path] = true
		}
	}
	root := &node{path: source, name: filepath.Base(source), depth: -1, expanded: true}
	loadChildren(root)
	m := &model{
		root:     root,
		excluded: excluded,
		source:   source,
		height:   15,
	}
	m.rebuild()
	return m
}

// Init kicks off the first background count, for the rows visible on open.
func (m *model) Init() tea.Cmd { return m.countVisibleCmd() }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case countsMsg:
		m.applyCounts(msg)
		return m, nil
	case tea.WindowSizeMsg:
		m.height = msg.Height - 8
		if m.height < 3 {
			m.height = 3
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.canceled = true
			return m, tea.Quit
		case "esc":
			m.back = true
			return m, tea.Quit
		case "enter":
			return m, tea.Quit // done
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.visible)-1 {
				m.cursor++
			}
		case "right", "l":
			m.expandCurrent()
		case "left", "h":
			m.collapseCurrent()
		case " ":
			if len(m.visible) > 0 {
				m.toggle(m.visible[m.cursor])
			}
		case "p":
			if len(m.visible) > 0 {
				if m.preview == m.visible[m.cursor] {
					m.preview = nil
				} else {
					m.preview = m.visible[m.cursor]
				}
			}
		}
	}
	m.clampScroll()
	// Scrolling, expanding, or resizing may have exposed uncounted rows.
	return m, m.countVisibleCmd()
}

func (m *model) View() string {
	var b strings.Builder
	b.WriteString("\n  " + titleStyle.Render("Select directories to exclude") + "\n")
	b.WriteString("  " + dimStyle.Render(m.source) + "\n\n")

	if len(m.visible) == 0 {
		b.WriteString("  " + dimStyle.Render("(no subdirectories)") + "\n")
	}

	rows := m.listRows()
	end := m.offset + rows
	if end > len(m.visible) {
		end = len(m.visible)
	}
	for i := m.offset; i < end; i++ {
		b.WriteString(m.renderNode(m.visible[i], i == m.cursor))
	}
	if len(m.visible) > rows {
		b.WriteString("  " + dimStyle.Render(fmt.Sprintf("     %d–%d of %d", m.offset+1, end, len(m.visible))) + "\n")
	}

	if m.preview != nil {
		b.WriteString(m.renderPreview(m.preview))
	}

	b.WriteString("\n  " + m.statusLine() + "\n")
	b.WriteString("  " + helpLine() + "\n")
	return b.String()
}

// helpLine renders the key hints with the keys in the accent color and the
// action words dimmed, so the two read as distinct.
func helpLine() string {
	pairs := [][2]string{
		{"↑↓", "move"},
		{"→", "open"},
		{"←", "collapse"},
		{"space", "exclude"},
		{"p", "preview"},
		{"enter", "done"},
		{"esc", "back"},
		{"q", "quit"},
	}
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = keyStyle.Render(p[0]) + " " + dimStyle.Render(p[1])
	}
	return strings.Join(parts, dimStyle.Render("   "))
}

func (m *model) statusLine() string {
	switch n := len(m.excluded); n {
	case 0:
		return keptStyle.Render("nothing excluded")
	case 1:
		return excludedStyle.Render("1 directory excluded")
	default:
		return excludedStyle.Render(fmt.Sprintf("%d directories excluded", n))
	}
}

func (m *model) renderNode(n *node, focused bool) string {
	// Expandable directories get a colored arrow; a directory with no
	// subdirectories gets a dim leaf dot (nothing to expand into). Until the count
	// lands we assume expandable: an arrow that turns out to be a leaf costs one
	// harmless keypress, whereas a leaf dot on a real folder hides it.
	marker := dimStyle.Render("·") + " "
	if n.hasSubdirs || !n.counted {
		if n.expanded {
			marker = arrowStyle.Render("▾") + " "
		} else {
			marker = arrowStyle.Render("▸") + " "
		}
	}

	name := n.name
	if m.effectivelyExcluded(n) {
		name = excludedStyle.Render("✗ " + n.name)
	}
	// The label is omitted rather than showing "(0 files, 0 dirs)" for a folder
	// whose count has not arrived — a zero would be a lie, not a placeholder.
	count := ""
	if n.counted {
		count = dimStyle.Render(fmt.Sprintf("  (%d files, %d dirs)", n.fileCount, n.subdirCount))
	}

	pointer := "  "
	if focused {
		pointer = cursorStyle.Render("❯ ")
	}
	indent := strings.Repeat("  ", n.depth)
	return "  " + pointer + indent + marker + name + count + "\n"
}

// previewMax caps how many filenames the preview lists, bounding its height so
// the list, preview, status, and help all fit on screen together.
const previewMax = 10

func (m *model) renderPreview(n *node) string {
	files := previewFilesOf(n)

	var b strings.Builder
	b.WriteString("\n  " + titleStyle.Render("Preview: "+n.name) + "\n")
	if len(files) == 0 {
		b.WriteString("  " + dimStyle.Render("(no files directly in this directory)") + "\n")
	}
	for i, f := range files {
		if i >= previewMax {
			b.WriteString("  " + dimStyle.Render(fmt.Sprintf("     … and %d more", len(files)-previewMax)) + "\n")
			break
		}
		b.WriteString("  " + dimStyle.Render("   · "+f) + "\n")
	}
	return b.String()
}

// ── tree mechanics ───────────────────────────────────────────────────────

func (m *model) rebuild() {
	m.visible = m.visible[:0]
	var walk func(n *node)
	walk = func(n *node) {
		for _, c := range n.children {
			m.visible = append(m.visible, c)
			if c.expanded {
				walk(c)
			}
		}
	}
	walk(m.root)
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

func (m *model) expandCurrent() {
	if len(m.visible) == 0 {
		return
	}
	n := m.visible[m.cursor]
	// loadChildren reads n.path once and reveals whether there is anything to
	// expand into, so there is no need to establish hasSubdirs first — that would
	// have been a second read of the same directory.
	loadChildren(n)
	if len(n.children) == 0 {
		return
	}
	n.expanded = true
	m.rebuild()
}

func (m *model) collapseCurrent() {
	if len(m.visible) == 0 {
		return
	}
	n := m.visible[m.cursor]
	if n.expanded {
		n.expanded = false
		m.rebuild()
		return
	}
	// Already collapsed: jump up to the parent and collapse it.
	if n.parent != nil && n.parent != m.root {
		n.parent.expanded = false
		m.rebuild()
		for i, v := range m.visible {
			if v == n.parent {
				m.cursor = i
				break
			}
		}
	}
}

// toggle flips explicit exclusion on n. Toggling inside an already-excluded
// subtree is a no-op (un-exclude the ancestor instead). Excluding a directory
// drops any now-redundant descendant exclusions.
func (m *model) toggle(n *node) {
	for p := n.parent; p != nil; p = p.parent {
		if m.excluded[p.path] {
			return
		}
	}
	if m.excluded[n.path] {
		delete(m.excluded, n.path)
		return
	}
	m.excluded[n.path] = true
	prefix := n.path + string(filepath.Separator)
	for path := range m.excluded {
		if strings.HasPrefix(path, prefix) {
			delete(m.excluded, path)
		}
	}
}

func (m *model) effectivelyExcluded(n *node) bool {
	for p := n; p != nil; p = p.parent {
		if m.excluded[p.path] {
			return true
		}
	}
	return false
}

// listRows is how many tree rows fit right now. It starts from the middle
// region (m.height) and gives back space to the preview pane and the scroll
// indicator so the status and help lines below always stay on screen.
func (m *model) listRows() int {
	rows := m.height
	if m.preview != nil {
		rows -= m.previewHeight()
	}
	if rows < 1 {
		rows = 1
	}
	if len(m.visible) > rows {
		rows-- // the "x–y of z" indicator takes a line
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

// previewHeight predicts how many lines renderPreview will emit for the open
// node, so listRows can reserve exactly that much.
func (m *model) previewHeight() int {
	const header = 2 // blank + "Preview: name"
	// Reads the same memoized listing renderPreview draws, so the reserved height
	// and the rendered height cannot disagree and push the help line off screen.
	files := len(previewFilesOf(m.preview))
	switch {
	case files == 0:
		return header + 1 // "(no files…)"
	case files > previewMax:
		return header + previewMax + 1 // capped list + "… and N more"
	default:
		return header + files
	}
}

func (m *model) clampScroll() {
	rows := m.listRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func loadChildren(n *node) {
	if n.loaded {
		return
	}
	n.loaded = true
	entries, err := os.ReadDir(n.path)
	if err != nil {
		return
	}
	var kids []*node
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		// Never offer to exclude a directory the copy skips anyway.
		if fsmeta.IsMetadata(e.Name()) {
			continue
		}
		// Counts are left unset here on purpose: filling them would read every
		// child directory in full before a single row is drawn. countVisibleCmd
		// fetches them in the background, for on-screen rows only.
		kids = append(kids, &node{
			path:   filepath.Join(n.path, e.Name()),
			name:   e.Name(),
			depth:  n.depth + 1,
			parent: n,
		})
	}
	sort.Slice(kids, func(i, j int) bool { return kids[i].name < kids[j].name })
	n.children = kids
}

// countChildren reports what the copy would actually take from dir, so the
// "3 files · 2 dirs" label never promises filesystem bookkeeping.
func countChildren(dir string) (files, dirs int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if fsmeta.IsMetadata(e.Name()) {
			continue
		}
		if e.IsDir() {
			dirs++
		} else {
			files++
		}
	}
	return files, dirs
}
