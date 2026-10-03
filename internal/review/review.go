// Package review is the screen for checking duplicate sets one by one before
// anything is skipped.
//
// The duplicates step offered one answer for every set at once: skip them all, or
// none. The scan ranks each set by rules, and the user knows what the rules
// cannot: which folder is the archive, which export was cropped. So each set is
// shown on its own, with the file the scan would copy marked. Any member can
// become the one copied, or the whole set can be kept.
//
// The model mirrors package exclude: an Elm-style Init/Update/View, driven wholly
// from the keyboard. Pictures load in the background and arrive as messages, so a
// slow drive never stalls a keypress.
package review

import (
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ozzyphantom/SuperDirectory/internal/hint"
)

// ErrCanceled means the user quit outright (ctrl+c). ErrBack means they stepped
// back to the previous screen (esc), and no set changed.
var (
	ErrCanceled = errors.New("review canceled")
	ErrBack     = errors.New("review back")
)

var (
	titleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	copyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#2ecc71"))
	skipStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange, matching the app
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	boldStyle   = lipgloss.NewStyle().Bold(true)
)

// Member is one file in a set.
type Member struct {
	Path    string
	Size    int64
	ModTime time.Time
	Dims    string // "4032×3024" for pictures, "" otherwise
	Note    string // why it is ranked as it is, e.g. "name reads as a copy"; may be ""
}

// Set is a group of files the scan believes are duplicates.
type Set struct {
	Kind    string // "Identical", "Smaller copy", "Similar document"
	Members []Member
	Keep    int  // index into Members of the file to copy; the rest are skipped
	KeepAll bool // the user chose to copy every member
}

// Options configure the screen.
type Options struct {
	// Thumbnail loads a picture for display, or nil for none. It is called off the
	// update loop, in a tea.Cmd, and its results are cached. Two calls may run at
	// once.
	Thumbnail func(path string) (image.Image, error)
}

const (
	// headLines is the blank line, title, set line, summary, and blank line above
	// the list. footLines is the notice line, and the newline that ends the view.
	// The key hints come on top of the foot, and wrap.
	headLines = 5
	footLines = 2

	// rowLead is the width ahead of a member's path: indent, pointer, marker, gap.
	// A note starts at the same column, under the path.
	rowLead = 12
	// minPath is the narrowest path column before the other columns give way.
	minPath = 16
	// listFloor is how many list lines the panes leave the list when height runs
	// short.
	listFloor = 5

	// Picture panes, in characters. A character holds two pixel rows, so a pane
	// half as tall as it is wide is square.
	maxPaneCols, maxPaneRows = 28, 14
	minPaneCols, minPaneRows = 10, 4
	paneIndent, paneGap      = 4, 4

	// keptSide is the side, in pixels, of the copies the cache holds: twice the
	// largest pane, so a pane of any size still averages several pixels.
	keptSide = 2 * maxPaneCols
	// maxLoads bounds the pictures loading at once. Each may decode a large file,
	// and holding an arrow key down would otherwise start one per set.
	maxLoads = 2
	// loadTimeout is how long a picture may take before its pane says no preview.
	// A read that never returns, on a failing drive, would otherwise hold its slot
	// for good, and with every slot held no picture would load again.
	loadTimeout = 20 * time.Second
	// maxThumbs bounds the cache. A review of a large library can pass thousands of
	// pictures, at a few kilobytes each.
	maxThumbs = 256
)

// Run shows the sets and returns them as the user left them.
//
// It works on a copy, so esc and ctrl+c leave the caller's sets as they were. A
// Keep outside Members reads as the first member: every set copies at least one
// file unless the user keeps them all. With no sets, Run returns at once.
func Run(sets []Set, opts Options) ([]Set, error) {
	if len(sets) == 0 {
		return sets, nil
	}
	m := newModel(sets, opts)
	m.color = drawsColor()
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	return final.(*model).result()
}

type model struct {
	sets    []Set
	cur     int   // the set on screen
	cursors []int // the highlighted member of each set, so a set looks as it was left
	offsets []int // the first member drawn of each set, once its list scrolls

	width  int    // terminal columns; 0 until the first resize, meaning unknown
	height int    // terminal rows; 0 until the first resize, meaning unknown
	notice string // why the last key changed nothing; cleared by the next key

	opts  Options
	color bool // whether lipgloss draws color here; without it a picture is one glyph repeated

	// thumbs caches pictures by path, loaded or loading; order lists its keys,
	// oldest first. Only the update loop touches either.
	thumbs map[string]*thumb
	order  []string

	// shown holds the paths of the pictures on screen. Loads run on other
	// goroutines and read it before they start, so a load for a picture the user
	// has moved past stands down instead of decoding a file nobody will see.
	shown atomic.Pointer[[2]string]
	// slots holds one token per load in flight; see maxLoads.
	slots chan struct{}
	// timeout is loadTimeout, held here so tests can shorten it.
	timeout time.Duration

	done, back, canceled bool
}

// thumb is one cached picture.
type thumb struct {
	loading bool
	img     image.Image // a small copy; see keptSide
	err     error

	// text is the last rendering, at cols by rows. View draws on every key, and a
	// picture only changes size with the window.
	cols, rows int
	text       string
}

// thumbMsg carries a finished load back to the update loop.
type thumbMsg struct {
	path    string
	img     image.Image // nil when err is set, or the load stood down
	err     error
	dropped bool // the picture left the screen before the load began
}

var (
	errNoPicture = errors.New("no picture")
	errTooSlow   = errors.New("took too long to load")
)

func newModel(sets []Set, opts Options) *model {
	m := &model{
		sets:    slices.Clone(sets),
		cursors: make([]int, len(sets)),
		offsets: make([]int, len(sets)),
		opts:    opts,
		thumbs:  map[string]*thumb{},
		slots:   make(chan struct{}, maxLoads),
		timeout: loadTimeout,
	}
	for i := range m.sets {
		s := &m.sets[i]
		if s.Keep < 0 || s.Keep >= len(s.Members) {
			s.Keep = 0
		}
		// Open each set on the first member besides the keeper, so the panes compare
		// the file to copy with a file to skip from the first frame.
		if s.Keep == 0 && len(s.Members) > 1 {
			m.cursors[i] = 1
		}
	}
	return m
}

// result is what Run returns. Only d keeps the changes; any other ending, a
// signal included, leaves the caller's sets alone.
func (m *model) result() ([]Set, error) {
	switch {
	case m.done:
		return m.sets, nil
	case m.back:
		return nil, ErrBack
	default:
		return nil, ErrCanceled
	}
}

// Init starts loading the first set's pictures.
func (m *model) Init() tea.Cmd { return m.loadShown() }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case thumbMsg:
		m.store(msg)
	case tea.KeyMsg:
		if m.press(msg) {
			return m, tea.Quit
		}
	}
	m.clampScroll()
	// A key, a resize, or a load that stood down may have left a picture on
	// screen that is not loaded.
	return m, m.loadShown()
}

// press applies one key, and reports whether it ends the review.
func (m *model) press(msg tea.KeyMsg) bool {
	m.notice = ""
	s := &m.sets[m.cur]
	cursor := &m.cursors[m.cur]
	switch msg.String() {
	case "ctrl+c":
		m.canceled = true
		return true
	case "esc":
		m.back = true
		return true
	case "d":
		m.done = true
		return true
	case "left", "h":
		if m.cur == 0 {
			m.notice = "Already on the first set."
		} else {
			m.cur--
		}
	case "right", "l":
		if m.cur == len(m.sets)-1 {
			m.notice = "Last set. Press d when done."
		} else {
			m.cur++
		}
	case "up", "k":
		if *cursor > 0 {
			*cursor--
		}
	case "down", "j":
		if *cursor < len(s.Members)-1 {
			*cursor++
		}
	case "enter", " ", "space":
		if len(s.Members) > 0 {
			s.Keep, s.KeepAll = *cursor, false
		}
	case "a":
		s.KeepAll = !s.KeepAll
	case "n":
		if !m.nextSkipping() {
			m.notice = "No other set skips a file."
		}
	}
	return false
}

// nextSkipping moves to the next set that still skips a file, wrapping past the
// last, and reports whether there was one. Sets the user chose to keep whole are
// passed over: they are settled.
func (m *model) nextSkipping() bool {
	for step := 1; step < len(m.sets); step++ {
		i := (m.cur + step) % len(m.sets)
		if skips(&m.sets[i]) {
			m.cur = i
			return true
		}
	}
	return false
}

// skips reports whether a set leaves any file behind.
func skips(s *Set) bool { return !s.KeepAll && len(s.Members) > 1 }

// tally counts the files the sets skip, their bytes, and the files they copy.
func tally(sets []Set) (skipped int, bytes int64, kept int) {
	for _, s := range sets {
		for i, mem := range s.Members {
			if s.KeepAll || i == s.Keep {
				kept++
			} else {
				skipped++
				bytes += mem.Size
			}
		}
	}
	return skipped, bytes, kept
}

// ── layout ───────────────────────────────────────────────────────────────

// layout divides the window's height between the member list and the picture
// panes. It returns the lines the list may use, and the pane size in characters,
// or 0 by 0 when there are no panes.
func (m *model) layout() (listRows, cols, rows int) {
	need := m.listLines()
	avail := need + 2 + maxPaneRows // height unknown: room for everything
	if m.height > 0 {
		avail = m.height - headLines - footLines - len(hint.Lines(m.hints(), m.width))
	}
	avail = max(avail, 1)
	if m.pictures() {
		cols = maxPaneCols
		if m.width > 0 {
			cols = min(cols, (m.width-paneIndent-paneGap)/2)
		}
		// The two lines are the blank above the panes and their labels.
		rows = min(maxPaneRows, cols/2, avail-2-min(need, listFloor))
		if cols < minPaneCols || rows < minPaneRows {
			cols, rows = 0, 0
		}
	}
	listRows = avail
	if rows > 0 {
		listRows -= rows + 2
	}
	return min(listRows, need), cols, rows
}

// pictures reports whether the current set gets picture panes: it needs a
// loader, a terminal that draws color, and a picture in the set.
func (m *model) pictures() bool {
	if m.opts.Thumbnail == nil || !m.color {
		return false
	}
	for _, mem := range m.sets[m.cur].Members {
		if mem.Dims != "" {
			return true
		}
	}
	return false
}

// listLines is the lines the current set's members take, notes included. An
// empty set takes one, to say so.
func (m *model) listLines() int {
	return max(linesOf(m.sets[m.cur].Members), 1)
}

func linesOf(members []Member) int {
	n := 0
	for _, mem := range members {
		n += memberLines(mem)
	}
	return n
}

func memberLines(mem Member) int {
	if mem.Note != "" {
		return 2
	}
	return 1
}

// capacity is the list lines left for members, and whether the "x–y of z"
// indicator takes one. It shows only when the members do not all fit.
func (m *model) capacity(listRows int) (room int, indicator bool) {
	if m.listLines() > listRows && listRows > 1 {
		return listRows - 1, true
	}
	return listRows, false
}

// drawnTo is the end of the run of members drawn whole from o in room lines.
func (m *model) drawnTo(o, room int) int {
	members := m.sets[m.cur].Members
	used := 0
	for i := o; i < len(members); i++ {
		used += memberLines(members[i])
		if used > room {
			return i
		}
	}
	return len(members)
}

// clampScroll keeps the highlighted member in view, and pulls the list back down
// when a taller window leaves room above it.
func (m *model) clampScroll() {
	members := m.sets[m.cur].Members
	if len(members) == 0 {
		return
	}
	listRows, _, _ := m.layout()
	room, _ := m.capacity(listRows)
	c := m.cursors[m.cur]
	o := min(m.offsets[m.cur], c)
	for o < c && m.drawnTo(o, room) <= c {
		o++
	}
	for o > 0 && linesOf(members[o-1:]) <= room {
		o--
	}
	m.offsets[m.cur] = o
}

// ── view ─────────────────────────────────────────────────────────────────

func (m *model) View() string {
	listRows, cols, rows := m.layout()
	lines := []string{
		"",
		"  " + titleStyle.Render("Review duplicates"),
		"  " + m.setLine(),
		"  " + m.summary(),
		"",
	}
	lines = append(lines, m.list(listRows)...)
	if rows > 0 {
		lines = append(lines, m.panes(cols, rows)...)
	}
	notice := ""
	if m.notice != "" {
		notice = "  " + dimStyle.Render(m.notice)
	}
	lines = append(lines, notice)
	lines = append(lines, strings.Split(hint.Block(m.hints(), m.width), "\n")...)

	// The rows, panes, and hints are laid out to the width. Free text (the set
	// line, the summary, notes, the notice) is cut here, with an ellipsis. Fitting
	// every line also keeps a window too narrow for the layout's rules from
	// wrapping a line and misdrawing the frame.
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(hint.Fit(l, m.width) + "\n")
	}
	return b.String()
}

// setLine is "Set 3 of 48 · Smaller copy", saying so when the set is kept whole.
func (m *model) setLine() string {
	s := &m.sets[m.cur]
	line := boldStyle.Render(fmt.Sprintf("Set %d of %d", m.cur+1, len(m.sets)))
	if s.Kind != "" {
		line += dimStyle.Render(" · ") + s.Kind
	}
	if s.KeepAll && len(s.Members) > 1 {
		line += dimStyle.Render(" · ") + copyStyle.Render("copying all")
	}
	return line
}

// summary totals the whole review, so each choice shows what it costs or saves.
// On a window too narrow for it, the noun goes before the counts do.
func (m *model) summary() string {
	skipped, bytes, kept := tally(m.sets)
	keeping := dimStyle.Render(" · ") + copyStyle.Render(fmt.Sprintf("keeping %d", kept))
	if skipped == 0 {
		return copyStyle.Render("Skipping nothing") + keeping
	}
	line := skipStyle.Render(fmt.Sprintf("Skipping %d file(s), %s", skipped, humanBytes(bytes))) + keeping
	if m.width > 0 && ansi.StringWidth(line) > m.width-2 {
		line = skipStyle.Render(fmt.Sprintf("Skipping %d, %s", skipped, humanBytes(bytes))) + keeping
	}
	return line
}

// hints are the keys, with a's action naming what it would do to this set.
func (m *model) hints() []hint.Pair {
	all := hint.Pair{Key: "a", Action: "copy all"}
	if m.sets[m.cur].KeepAll {
		all.Action = "copy ★ only"
	}
	return []hint.Pair{
		{Key: "←→", Action: "set"},
		{Key: "↑↓", Action: "file"},
		{Key: "enter", Action: "copy this one"},
		all,
		{Key: "n", Action: "next set that skips"},
		{Key: "d", Action: "done"},
		{Key: "esc", Action: "back"},
		{Key: "ctrl+c", Action: "quit"},
	}
}

// marker says what happens to member i: the keeper is starred, the other files
// of a set kept whole are ticked, and the rest are skipped.
func marker(s *Set, i int) string {
	switch {
	case i == s.Keep:
		return copyStyle.Render("★ copy")
	case s.KeepAll:
		return copyStyle.Render("✓ copy")
	default:
		return skipStyle.Render("✗ skip")
	}
}

// list draws the current set's members from its scroll offset, in listRows
// lines.
func (m *model) list(listRows int) []string {
	members := m.sets[m.cur].Members
	if len(members) == 0 {
		return []string{"  " + dimStyle.Render("(no files in this set)")}
	}
	room, indicator := m.capacity(listRows)
	cols := m.columns()
	o := m.offsets[m.cur]
	var lines []string
	i := o
	for ; i < len(members) && len(lines) < room; i++ {
		for _, l := range m.member(i, cols) {
			if len(lines) < room {
				lines = append(lines, l)
			}
		}
	}
	if indicator {
		lines = append(lines, "  "+dimStyle.Render(fmt.Sprintf("     %d–%d of %d", o+1, i, len(members))))
	}
	return lines
}

// rowCols are the widths of a set's columns. A width of 0 hides a column.
type rowCols struct{ path, size, dims, date int }

// columns sizes the current set's columns to the window. A narrow window drops
// the date first, then the size, then the picture's dimensions, so the path keeps
// room to say which file it is.
func (m *model) columns() rowCols {
	var c rowCols
	for _, mem := range m.sets[m.cur].Members {
		c.path = max(c.path, ansi.StringWidth(mem.Path))
		c.size = max(c.size, ansi.StringWidth(humanBytes(mem.Size)))
		c.dims = max(c.dims, ansi.StringWidth(mem.Dims))
		if !mem.ModTime.IsZero() {
			c.date = len(time.DateOnly)
		}
	}
	if m.width <= 0 {
		return c
	}
	room := func() int {
		n := m.width - rowLead
		for _, w := range []int{c.size, c.dims, c.date} {
			if w > 0 {
				n -= 2 + w
			}
		}
		return n
	}
	for _, col := range []*int{&c.date, &c.size, &c.dims} {
		if room() >= minPath {
			break
		}
		*col = 0
	}
	c.path = max(1, min(c.path, room()))
	return c
}

// member draws member i: its row, then its note under the path when it has one.
func (m *model) member(i int, c rowCols) []string {
	s := &m.sets[m.cur]
	mem := s.Members[i]
	pointer := "  "
	if i == m.cursors[m.cur] {
		pointer = cursorStyle.Render("❯ ")
	}
	row := "  " + pointer + marker(s, i) + "  " + padRight(hint.FitPath(mem.Path, c.path), c.path)
	if c.size > 0 {
		row += "  " + padLeft(humanBytes(mem.Size), c.size)
	}
	if c.dims > 0 {
		row += "  " + padLeft(mem.Dims, c.dims)
	}
	if c.date > 0 && !mem.ModTime.IsZero() {
		row += "  " + mem.ModTime.Format(time.DateOnly)
	}
	lines := []string{row}
	if mem.Note != "" {
		lines = append(lines, strings.Repeat(" ", rowLead)+dimStyle.Render(mem.Note))
	}
	return lines
}

// panes draws the keeper's picture beside the highlighted member's, each under a
// label that names the file and what happens to it. With the keeper highlighted,
// there is one pane.
func (m *model) panes(cols, rows int) []string {
	s := &m.sets[m.cur]
	shown := []int{s.Keep}
	if c := m.cursors[m.cur]; c != s.Keep {
		shown = append(shown, c)
	}
	labels := make([]string, len(shown))
	bodies := make([][]string, len(shown))
	for k, i := range shown {
		name := filepath.Base(s.Members[i].Path)
		labels[k] = padRight(marker(s, i)+" "+hint.FitPath(name, cols-7), cols)
		bodies[k] = m.pane(s.Members[i], cols, rows)
	}
	indent, gap := strings.Repeat(" ", paneIndent), strings.Repeat(" ", paneGap)
	lines := []string{"", indent + strings.Join(labels, gap)}
	for r := range rows {
		row := make([]string, len(shown))
		for k := range shown {
			row[k] = bodies[k][r]
		}
		lines = append(lines, indent+strings.Join(row, gap))
	}
	return lines
}

// pane is one member's picture, exactly cols by rows, or a dim word in its place.
func (m *model) pane(mem Member, cols, rows int) []string {
	t := m.thumbs[mem.Path]
	switch {
	case mem.Dims == "" || (t != nil && t.err != nil):
		return placeholder("no preview", cols, rows)
	case t == nil || t.loading:
		// A load starts with the frame that first shows the pane, so a missing
		// entry is about to be loading.
		return placeholder("loading…", cols, rows)
	}
	if t.cols != cols || t.rows != rows {
		t.text, t.cols, t.rows = Thumb(t.img, cols, rows), cols, rows
	}
	return strings.Split(t.text, "\n")
}

// placeholder fills a pane with one dim word, keeping the pane's size so the
// layout does not jump when the picture arrives.
func placeholder(word string, cols, rows int) []string {
	lines := make([]string, rows)
	lines[0] = dimStyle.Render(padRight(hint.Fit(word, cols), cols))
	for i := 1; i < rows; i++ {
		lines[i] = strings.Repeat(" ", cols)
	}
	return lines
}

// ── pictures ─────────────────────────────────────────────────────────────

// onScreen lists the paths of the pictures the panes draw now: the keeper's, then
// the highlighted member's. A pane that is absent or shows no picture is "".
func (m *model) onScreen() [2]string {
	var out [2]string
	if _, _, rows := m.layout(); rows == 0 {
		return out
	}
	s := &m.sets[m.cur]
	for k, i := range []int{s.Keep, m.cursors[m.cur]} {
		if k == 1 && i == s.Keep {
			break
		}
		if mem := s.Members[i]; mem.Dims != "" {
			out[k] = mem.Path
		}
	}
	return out
}

// loadShown records which pictures are on screen, and starts loading those not
// yet cached. It returns nil when there is nothing to start.
func (m *model) loadShown() tea.Cmd {
	paths := m.onScreen()
	m.shown.Store(&paths)
	var cmds []tea.Cmd
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, ok := m.thumbs[p]; ok {
			continue // loaded, failed, or on its way: never twice
		}
		m.remember(p, &thumb{loading: true})
		cmds = append(cmds, m.loadCmd(p))
	}
	return tea.Batch(cmds...)
}

// loadCmd loads one picture and shrinks it to a copy small enough to keep. It
// waits for a slot first, and stands down if the picture has left the screen by
// then.
func (m *model) loadCmd(path string) tea.Cmd {
	load, slots, shown, timeout := m.opts.Thumbnail, m.slots, &m.shown, m.timeout
	return func() tea.Msg {
		slots <- struct{}{}
		defer func() { <-slots }()
		if on := shown.Load(); on == nil || (on[0] != path && on[1] != path) {
			return thumbMsg{path: path, dropped: true}
		}
		// The load runs on its own goroutine, so one that never returns can be
		// abandoned and its slot freed: a deliberate, bounded leak, as in the copy.
		// The channel has room for the answer, so a late load still finishes.
		answer := make(chan thumbMsg, 1)
		go func() { answer <- loadPicture(load, path) }()
		select {
		case msg := <-answer:
			return msg
		case <-time.After(timeout):
			return thumbMsg{path: path, err: errTooSlow}
		}
	}
}

// loadPicture calls load and shrinks what it returns. A picture that crashes its
// decoder costs its preview, not the review.
func loadPicture(load func(string) (image.Image, error), path string) (msg thumbMsg) {
	defer func() {
		if r := recover(); r != nil {
			msg = thumbMsg{path: path, err: fmt.Errorf("loading %s: %v", path, r)}
		}
	}()
	img, err := load(path)
	switch {
	case err != nil:
		return thumbMsg{path: path, err: err}
	case img == nil || img.Bounds().Empty():
		return thumbMsg{path: path, err: errNoPicture}
	}
	return thumbMsg{path: path, img: shrink(img, keptSide, keptSide)}
}

// store files a finished load. A picture that left the screen while it loaded is
// cached all the same: it is correct, and the user may come back to it.
func (m *model) store(msg thumbMsg) {
	if msg.dropped {
		// Nothing was loaded. Forget the entry so the picture loads when it is next
		// on screen; loadShown starts it again at once if it already is.
		delete(m.thumbs, msg.path)
		m.order = slices.DeleteFunc(m.order, func(p string) bool { return p == msg.path })
		return
	}
	t := m.thumbs[msg.path]
	if t == nil {
		t = &thumb{}
		m.remember(msg.path, t)
	}
	t.loading, t.img, t.err, t.text = false, msg.img, msg.err, ""
}

// remember caches t under path. Past maxThumbs it forgets the oldest pictures
// that are neither on screen nor still loading.
func (m *model) remember(path string, t *thumb) {
	m.thumbs[path] = t
	m.order = append(m.order, path)
	excess := len(m.order) - maxThumbs
	if excess <= 0 {
		return
	}
	on := m.shown.Load()
	m.order = slices.DeleteFunc(m.order, func(p string) bool {
		if excess == 0 || m.thumbs[p].loading || (on != nil && (p == on[0] || p == on[1])) {
			return false
		}
		delete(m.thumbs, p)
		excess--
		return true
	})
}

// drawsColor reports whether lipgloss draws color on this terminal. Without
// color, a picture is one glyph repeated, so the screen leaves the panes out.
func drawsColor() bool {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Render("x") != "x"
}

// ── formatting ───────────────────────────────────────────────────────────

// padRight fills s with spaces to w columns, so the columns after it line up.
func padRight(s string, w int) string {
	if n := w - ansi.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// padLeft right-aligns s in w columns, so sizes line up on their units.
func padLeft(s string, w int) string {
	if n := w - ansi.StringWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// humanBytes formats a byte count in decimal units, matching how drive and
// transfer speeds are quoted. It copies ui.HumanBytes: package ui is the front end
// that opens screens like this one, so importing it from here would make a cycle.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTP"[exp])
}
