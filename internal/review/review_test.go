package review

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

var day = time.Date(2019, 7, 14, 9, 30, 0, 0, time.UTC)

const (
	bigPicture   = "/Volumes/Archive/DSC_0012.JPG"
	smallPicture = "/Volumes/Archive/web/DSC_0012-web.jpg"
)

// fixture is four sets: identical files, one with a note; a picture at two sizes;
// three similar documents with the second ranked first; and a set kept whole.
func fixture() []Set {
	return []Set{
		{Kind: "Identical", Members: []Member{
			{Path: "/Volumes/Archive/Trip/beach.jpg", Size: 4_100_000, ModTime: day},
			{Path: "/Volumes/Archive/Backup/beach copy.jpg", Size: 4_100_000, ModTime: day.AddDate(1, 0, 0), Note: "name reads as a copy"},
		}},
		{Kind: "Smaller copy", Members: []Member{
			{Path: bigPicture, Size: 6_000_000, ModTime: day, Dims: "4032×3024"},
			{Path: smallPicture, Size: 300_000, ModTime: day, Dims: "1200×900"},
		}},
		{Kind: "Similar document", Keep: 1, Members: []Member{
			{Path: "/Volumes/Archive/Manuals/install-v1.pdf", Size: 1_000, ModTime: day},
			{Path: "/Volumes/Archive/Manuals/install-v2.pdf", Size: 2_000, ModTime: day},
			{Path: "/Volumes/Archive/Old/install.pdf", Size: 3_000, ModTime: day},
		}},
		{Kind: "Identical", KeepAll: true, Members: []Member{
			{Path: "/Volumes/Archive/a.txt", Size: 10, ModTime: day},
			{Path: "/Volumes/Archive/b.txt", Size: 10, ModTime: day},
		}},
	}
}

func pair() []Member {
	return []Member{{Path: "/x/a", Size: 1}, {Path: "/x/b", Size: 1}}
}

func update(m *model, msg tea.Msg) (*model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(*model), cmd
}

func send(m *model, msgs ...tea.Msg) *model {
	for _, msg := range msgs {
		m, _ = update(m, msg)
	}
	return m
}

func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }
func runeKey(r rune) tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

var space = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}

// newTestModel opens sets in an 80×30 window, without pictures.
func newTestModel(sets []Set) *model {
	return send(newModel(sets, Options{}), tea.WindowSizeMsg{Width: 80, Height: 30})
}

// messages runs cmd, and every command a batch holds, and returns what they sent.
func messages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, messages(c)...)
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

// deliver runs cmd and feeds what it sends back to the model, until no work is
// left.
func deliver(m *model, cmd tea.Cmd) *model {
	for _, msg := range messages(cmd) {
		var next tea.Cmd
		m, next = update(m, msg)
		m = deliver(m, next)
	}
	return m
}

// settle sends msg, then lets every load it starts finish.
func settle(m *model, msg tea.Msg) *model {
	m, cmd := update(m, msg)
	return deliver(m, cmd)
}

// loader is a fake Thumbnail. It counts calls by path, and serves a small picture,
// or the error or panic set up for a path.
type loader struct {
	mu     sync.Mutex
	calls  map[string]int
	fail   map[string]error
	panics map[string]bool
}

func newLoader() *loader {
	return &loader{calls: map[string]int{}, fail: map[string]error{}, panics: map[string]bool{}}
}

func (l *loader) load(path string) (image.Image, error) {
	l.mu.Lock()
	l.calls[path]++
	err, boom := l.fail[path], l.panics[path]
	l.mu.Unlock()
	if boom {
		panic("corrupt picture")
	}
	if err != nil {
		return nil, err
	}
	return solid(64, 48, color.RGBA{R: 200, G: 60, B: 40, A: 255}), nil
}

func (l *loader) total() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.calls {
		n += c
	}
	return n
}

// picModel opens the fixture on its picture set, in an 80×40 window that draws
// color, with l loading pictures. It does not run Init.
func picModel(l *loader) *model {
	m := newModel(fixture(), Options{Thumbnail: l.load})
	m.color = true
	m.cur = 1
	m.width, m.height = 80, 40
	return m
}

func TestOpensOnTheFirstFileToSkip(t *testing.T) {
	// The first frame comes before the window size is known.
	if v := ansi.Strip(newModel(fixture(), Options{}).View()); !strings.Contains(v, "Set 1 of 4 · Identical") {
		t.Errorf("the screen should draw before the first resize:\n%s", v)
	}
	m := newTestModel(fixture())
	// The keeper is member 0 in every set but the third, which keeps member 1.
	for i, want := range []int{1, 1, 0, 1} {
		if got := m.cursors[i]; got != want {
			t.Errorf("set %d opens on member %d, want %d", i, got, want)
		}
	}
}

func TestSetsStopAtTheEnds(t *testing.T) {
	m := newTestModel(fixture())
	m = send(m, key(tea.KeyLeft))
	if m.cur != 0 || !strings.Contains(m.notice, "first set") {
		t.Errorf("← on the first set: cur=%d notice=%q, want 0 and a notice", m.cur, m.notice)
	}
	m = send(m, key(tea.KeyRight))
	if m.cur != 1 || m.notice != "" {
		t.Errorf("→ should move to set 2 and clear the notice: cur=%d notice=%q", m.cur, m.notice)
	}
	m = send(m, runeKey('l'), runeKey('l'))
	if m.cur != 3 {
		t.Fatalf("l should move on like →, cur=%d", m.cur)
	}
	m = send(m, runeKey('l'))
	if m.cur != 3 {
		t.Errorf("→ on the last set must not wrap, cur=%d", m.cur)
	}
	if !strings.Contains(ansi.Strip(m.View()), "Press d when done") {
		t.Error("→ on the last set should say how to finish")
	}
	m = send(m, runeKey('h'))
	if m.cur != 2 || m.notice != "" {
		t.Errorf("h should step back like ←: cur=%d notice=%q", m.cur, m.notice)
	}
}

func TestMembersStopAtTheEndsAndAreRemembered(t *testing.T) {
	m := newTestModel(fixture())
	m = send(m, runeKey('l'), runeKey('l')) // the three documents, opened on member 0
	m = send(m, key(tea.KeyUp))
	if c := m.cursors[2]; c != 0 {
		t.Errorf("↑ on the first member must not wrap, cursor=%d", c)
	}
	m = send(m, key(tea.KeyDown), runeKey('j'), runeKey('j'))
	if c := m.cursors[2]; c != 2 {
		t.Errorf("↓ past the last member must stop on it, cursor=%d", c)
	}
	m = send(m, runeKey('k'))
	if c := m.cursors[2]; c != 1 {
		t.Errorf("k should move up like ↑, cursor=%d", c)
	}
	// A set looks as it was left when the user comes back to it.
	m = send(m, key(tea.KeyRight), key(tea.KeyLeft))
	if m.cur != 2 || m.cursors[2] != 1 {
		t.Errorf("returning to a set lost its row: cur=%d cursor=%d", m.cur, m.cursors[2])
	}
}

func TestEnterOrSpaceChoosesTheFileToCopy(t *testing.T) {
	m := newTestModel(fixture())
	m = send(m, key(tea.KeyEnter)) // the first set opens on member 1
	if k := m.sets[0].Keep; k != 1 {
		t.Errorf("enter should make the highlighted file the keeper, Keep=%d", k)
	}
	m = send(m, key(tea.KeyUp), space)
	if k := m.sets[0].Keep; k != 0 {
		t.Errorf("space should choose like enter, Keep=%d", k)
	}
	// Choosing one file of a set kept whole ends keep-all: the user picked one.
	m = send(m, runeKey('l'), runeKey('l'), runeKey('l'), key(tea.KeyEnter))
	if s := m.sets[3]; s.KeepAll || s.Keep != 1 {
		t.Errorf("enter on a set kept whole: KeepAll=%v Keep=%d, want false and 1", s.KeepAll, s.Keep)
	}
}

func TestKeepAllToggles(t *testing.T) {
	m := newTestModel(fixture())
	m = send(m, runeKey('a'))
	if s := m.sets[0]; !s.KeepAll || s.Keep != 0 {
		t.Fatalf("a should keep the set whole and remember its keeper: %+v", s)
	}
	v := ansi.Strip(m.View())
	for _, want := range []string{"★ copy", "✓ copy", "copying all", "a copy ★ only"} {
		if !strings.Contains(v, want) {
			t.Errorf("a set kept whole should show %q", want)
		}
	}
	if strings.Contains(v, "✗ skip") {
		t.Error("a set kept whole still marks a file skipped")
	}

	m = send(m, runeKey('a'))
	if s := m.sets[0]; s.KeepAll || s.Keep != 0 {
		t.Errorf("a again should skip the copies once more, keeping the same file: %+v", s)
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "✗ skip") || !strings.Contains(v, "a copy all") {
		t.Error("after a second a, the copy should read as skipped again")
	}
}

func TestNextFindsSetsThatStillSkip(t *testing.T) {
	m := newTestModel([]Set{
		{Kind: "skips", Members: pair()},
		{Kind: "kept whole", Members: pair(), KeepAll: true},
		{Kind: "one file", Members: pair()[:1]},
		{Kind: "skips too", Members: pair()},
	})
	m = send(m, runeKey('n'))
	if m.cur != 3 {
		t.Errorf("n should pass over sets that skip nothing, cur=%d, want 3", m.cur)
	}
	m = send(m, runeKey('n'))
	if m.cur != 0 {
		t.Errorf("n should wrap around to the first set, cur=%d", m.cur)
	}
	// Once every other set copies everything, n stays put and says why.
	m = send(m, runeKey('l'), runeKey('l'), runeKey('l'), runeKey('a'), runeKey('h'), runeKey('h'), runeKey('h'))
	m = send(m, runeKey('n'))
	if m.cur != 0 || !strings.Contains(m.notice, "No other set") {
		t.Errorf("n with nothing left: cur=%d notice=%q", m.cur, m.notice)
	}
}

func TestSummaryTotalsTheReview(t *testing.T) {
	m := newTestModel(fixture())
	skipped, bytes, kept := tally(m.sets)
	if skipped != 4 || bytes != 4_404_000 || kept != 5 {
		t.Errorf("tally = %d files, %d bytes, keeping %d; want 4, 4404000, 5", skipped, bytes, kept)
	}
	if got := ansi.Strip(m.summary()); got != "Skipping 4 file(s), 4.4 MB · keeping 5" {
		t.Errorf("summary = %q", got)
	}
	// A window too narrow for the whole line drops the noun, not the counts.
	m = send(m, tea.WindowSizeMsg{Width: 36, Height: 30})
	if got := ansi.Strip(m.summary()); got != "Skipping 4, 4.4 MB · keeping 5" {
		t.Errorf("at 36 columns, summary = %q", got)
	}
	m = send(m, tea.WindowSizeMsg{Width: 80, Height: 30})

	// Copying the small picture instead skips the large one, and the total says so
	// at once.
	m = send(m, runeKey('l'), key(tea.KeyEnter))
	if got := ansi.Strip(m.summary()); got != "Skipping 4 file(s), 10.1 MB · keeping 5" {
		t.Errorf("after choosing the small picture, summary = %q", got)
	}

	// Keeping every set whole skips nothing.
	m = send(m, runeKey('a'), runeKey('h'), runeKey('a'), runeKey('l'), runeKey('l'), runeKey('a'))
	if got := ansi.Strip(m.summary()); got != "Skipping nothing · keeping 9" {
		t.Errorf("with every set kept whole, summary = %q", got)
	}
}

func TestDoneBackAndCancel(t *testing.T) {
	quits := func(cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, ok := cmd().(tea.QuitMsg)
		return ok
	}

	m := newTestModel(fixture())
	m = send(m, key(tea.KeyEnter))
	m, cmd := update(m, runeKey('d'))
	sets, err := m.result()
	if !quits(cmd) || err != nil || len(sets) != 4 || sets[0].Keep != 1 {
		t.Errorf("d should quit and return the sets as left: quit=%v err=%v sets=%v", quits(cmd), err, sets)
	}

	m = send(newTestModel(fixture()), key(tea.KeyEnter))
	m, cmd = update(m, key(tea.KeyEsc))
	if sets, err := m.result(); !quits(cmd) || sets != nil || !errors.Is(err, ErrBack) {
		t.Errorf("esc should quit with ErrBack and no sets: quit=%v sets=%v err=%v", quits(cmd), sets, err)
	}

	m, cmd = update(newTestModel(fixture()), key(tea.KeyCtrlC))
	if sets, err := m.result(); !quits(cmd) || sets != nil || !errors.Is(err, ErrCanceled) {
		t.Errorf("ctrl+c should quit with ErrCanceled and no sets: quit=%v sets=%v err=%v", quits(cmd), sets, err)
	}

	// A program that ends any other way, on a signal say, has not been told done.
	if sets, err := newTestModel(fixture()).result(); sets != nil || !errors.Is(err, ErrCanceled) {
		t.Errorf("an ending without d must not count as done: sets=%v err=%v", sets, err)
	}
}

func TestTheCallersSetsAreLeftAlone(t *testing.T) {
	sets := fixture()
	m := newTestModel(sets)
	m = send(m, key(tea.KeyEnter), runeKey('l'), runeKey('a'), runeKey('d'))
	if sets[0].Keep != 0 || sets[1].KeepAll {
		t.Errorf("the review changed the caller's sets: %+v, %+v", sets[0], sets[1])
	}
	if got, _ := m.result(); got[0].Keep != 1 || !got[1].KeepAll {
		t.Errorf("the returned sets lost the user's choices: %+v, %+v", got[0], got[1])
	}
}

func TestAnyKeepCopiesAFile(t *testing.T) {
	m := newTestModel([]Set{{Members: pair(), Keep: 7}, {Members: pair(), Keep: -1}, {Kind: "empty"}})
	if m.sets[0].Keep != 0 || m.sets[1].Keep != 0 {
		t.Errorf("a Keep outside Members should read as the first: %d, %d", m.sets[0].Keep, m.sets[1].Keep)
	}
	// An empty set draws, and shrugs off every key.
	m = send(m, runeKey('l'), runeKey('l'), key(tea.KeyEnter), key(tea.KeyDown), key(tea.KeyUp), runeKey('a'), space)
	if !strings.Contains(ansi.Strip(m.View()), "(no files in this set)") {
		t.Error("an empty set should say it is empty")
	}
}

func TestRunWithNoSetsReturnsAtOnce(t *testing.T) {
	sets, err := Run(nil, Options{})
	if sets != nil || err != nil {
		t.Errorf("Run(nil) = %v, %v; want nothing, at once, without a terminal", sets, err)
	}
}

func TestLongListScrollsWithTheCursor(t *testing.T) {
	var members []Member
	for i := range 20 {
		members = append(members, Member{Path: fmt.Sprintf("/Volumes/Archive/copy-%02d.txt", i), Size: 10, ModTime: day})
	}
	m := send(newModel([]Set{{Kind: "Identical", Members: members}}, Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	for range 25 {
		m = send(m, key(tea.KeyDown))
	}
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "copy-19.txt") || strings.Contains(v, "copy-00.txt") || !strings.Contains(v, "of 20") {
		t.Errorf("the list should follow the cursor to the last member:\n%s", v)
	}
	for range 25 {
		m = send(m, key(tea.KeyUp))
	}
	if m.offsets[0] != 0 || !strings.Contains(ansi.Strip(m.View()), "copy-00.txt") {
		t.Errorf("the list should follow the cursor back to the top, offset=%d", m.offsets[0])
	}
}

func TestANarrowWindowDropsTheLeastTellingColumns(t *testing.T) {
	m := send(newTestModel(fixture()), runeKey('l')) // the picture set
	if v := ansi.Strip(m.View()); !strings.Contains(v, "4032×3024") || !strings.Contains(v, "6.0 MB") || !strings.Contains(v, "2019-07-14") {
		t.Errorf("80 columns should show every column:\n%s", v)
	}
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 30})
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "4032×3024") {
		t.Errorf("40 columns should keep a picture's dimensions:\n%s", v)
	}
	if strings.Contains(v, "2019-07-14") || strings.Contains(v, "6.0 MB") {
		t.Errorf("40 columns should give the date and size up for the path:\n%s", v)
	}
}

// stress is sets that push every column: very long paths and notes, a terabyte
// file, pictures, a list longer than any window, one member, and none.
func stress() []Set {
	long := "/Volumes/Archive/" + strings.Repeat("a folder with a long name/", 8)
	var many []Member
	for i := range 30 {
		many = append(many, Member{Path: fmt.Sprintf("%scopy %d.jpg", long, i), Size: 1_500_000_000_000, ModTime: day,
			Dims: "12000×9000", Note: "name reads as a copy, and sits deeper in the tree than the file kept"})
	}
	return []Set{
		{Kind: "Smaller copy", Members: []Member{
			{Path: long + "DSC_0001.JPG", Size: 9_999_999_999, ModTime: day, Dims: "12000×9000"},
			{Path: long + "DSC_0001 (1).jpg", Size: 999, ModTime: day, Dims: "64×48", Note: strings.Repeat("a long note ", 20)},
		}},
		{Kind: "Smaller copy", Members: many},
		{Kind: "Similar document with a kind long enough to need cutting short on any narrow window", Members: []Member{
			{Path: long + "report.pdf", Size: 12, ModTime: day},
			{Path: "r.pdf", Size: 1_234_567, ModTime: day},
		}},
		{Kind: "Identical", Members: []Member{{Path: long + "only.txt", Size: 1, ModTime: day}}},
		{Kind: "Identical"},
	}
}

// fits checks that every line of the view fits the window, and that the view is
// no taller than it.
func fits(t *testing.T, m *model, state string) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) > m.height {
		t.Errorf("%d×%d, %s: the view is %d lines tall", m.width, m.height, state, len(lines))
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > m.width {
			t.Errorf("%d×%d, %s: a line is %d columns: %q", m.width, m.height, state, w, ansi.Strip(l))
		}
	}
}

func TestViewFitsTheWindow(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{24, 40} {
			l := newLoader()
			long := stress()[0].Members[1].Path
			l.fail[long] = errors.New("unsupported")
			m := newModel(stress(), Options{Thumbnail: l.load})
			m.color = true
			m, cmd := update(m, tea.WindowSizeMsg{Width: width, Height: height})
			fits(t, m, "pictures loading")
			m = deliver(m, cmd)
			fits(t, m, "pictures loaded")
			for range m.sets {
				for range 32 {
					m = settle(m, key(tea.KeyDown))
					fits(t, m, fmt.Sprintf("set %d, member %d", m.cur, m.cursors[m.cur]))
				}
				m = settle(m, runeKey('a'))
				fits(t, m, fmt.Sprintf("set %d kept whole", m.cur))
				m = settle(m, runeKey('l'))
			}
			fits(t, m, "with a notice")
		}
	}
}

func TestPicturesLoadOffTheUpdateLoop(t *testing.T) {
	l := newLoader()
	m := picModel(l)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init should start loading the keeper's and the highlighted picture")
	}
	if l.total() != 0 {
		t.Fatal("Init loaded a picture itself; loading belongs in a command")
	}
	if v := ansi.Strip(m.View()); strings.Count(v, "loading…") != 2 {
		t.Errorf("both panes should say loading… until their pictures arrive:\n%s", v)
	}

	msgs := messages(cmd)
	if len(msgs) != 2 || l.calls[bigPicture] != 1 || l.calls[smallPicture] != 1 {
		t.Fatalf("expected one load per picture, got %d messages and calls %v", len(msgs), l.calls)
	}
	for _, msg := range msgs {
		var next tea.Cmd
		if m, next = update(m, msg); next != nil {
			t.Error("a picture that arrived started another load")
		}
	}
	v := ansi.Strip(m.View())
	if strings.Contains(v, "loading…") || !strings.Contains(v, "▀") {
		t.Errorf("the pictures should be drawn once they arrive:\n%s", v)
	}

	// Moving onto the keeper and back is free: both pictures are cached.
	for _, k := range []tea.KeyType{tea.KeyUp, tea.KeyDown} {
		if _, next := update(m, key(k)); next != nil {
			t.Error("a cached picture was loaded again")
		}
	}
	if l.total() != 2 {
		t.Errorf("pictures were loaded %d times, want 2", l.total())
	}
}

func TestLoadsForPicturesLeftBehindStandDown(t *testing.T) {
	l := newLoader()
	m := picModel(l)
	cmd := m.Init()
	m = send(m, key(tea.KeyRight)) // on to the documents before the loads begin

	msgs := messages(cmd)
	for _, msg := range msgs {
		if tm, ok := msg.(thumbMsg); !ok || !tm.dropped {
			t.Errorf("a load for a picture no longer on screen should stand down, got %+v", msg)
		}
	}
	if l.total() != 0 {
		t.Errorf("pictures nobody can see were loaded %d times", l.total())
	}
	m = send(m, msgs...)
	if len(m.thumbs) != 0 || len(m.order) != 0 {
		t.Errorf("pictures that stood down should be forgotten, cache holds %d", len(m.thumbs))
	}

	// Back on the picture set, they load afresh.
	m, cmd = update(m, key(tea.KeyLeft))
	m = deliver(m, cmd)
	if l.total() != 2 || strings.Contains(ansi.Strip(m.View()), "loading…") {
		t.Errorf("returning should load the pictures: calls %v", l.calls)
	}
}

// A load can stand down just before the user comes back to its picture. The pane
// must not then say loading… forever.
func TestAPictureThatStoodDownAsTheUserReturnedStillLoads(t *testing.T) {
	l := newLoader()
	m := picModel(l)
	cmd := m.Init()
	m = send(m, key(tea.KeyRight))
	msgs := messages(cmd)         // the loads stand down: their pictures are off screen
	m = send(m, key(tea.KeyLeft)) // and the user is back before that news lands
	for _, msg := range msgs {
		m = settle(m, msg)
	}
	if l.total() != 2 || strings.Contains(ansi.Strip(m.View()), "loading…") {
		t.Errorf("pictures that stood down as the user returned never loaded: calls %v", l.calls)
	}
}

func TestPicturesThatLandLateAreKept(t *testing.T) {
	l := newLoader()
	m := picModel(l)
	msgs := messages(m.Init())     // the loads run...
	m = send(m, key(tea.KeyRight)) // ...but the user moves on before they land
	m = send(m, msgs...)

	m, cmd := update(m, key(tea.KeyLeft))
	if cmd != nil {
		t.Error("pictures that landed while the user was away were loaded again")
	}
	if v := ansi.Strip(m.View()); strings.Contains(v, "loading…") || !strings.Contains(v, "▀") {
		t.Errorf("pictures that landed late should be drawn on return:\n%s", v)
	}
}

func TestPicturesThatFailSayNoPreview(t *testing.T) {
	l := newLoader()
	l.fail[smallPicture] = errors.New("unsupported format")
	l.panics[bigPicture] = true
	m := picModel(l)
	m = deliver(m, m.Init())
	if v := ansi.Strip(m.View()); strings.Count(v, "no preview") != 2 {
		t.Errorf("a failed load and a crashed decoder should each say no preview:\n%s", v)
	}
	// A failure is cached like a picture, so it is not retried on every key.
	if _, cmd := update(m, key(tea.KeyUp)); cmd != nil {
		t.Error("a failed picture was loaded again")
	}
}

// A read on a failing drive can block for good. Its pane gives up, and its slot
// goes to the next picture: with every slot held, no picture would load again.
func TestAPictureThatNeverLoadsGivesUpItsSlot(t *testing.T) {
	stuck := make(chan struct{})
	defer close(stuck) // let the abandoned loads finish
	m := newModel(fixture(), Options{Thumbnail: func(string) (image.Image, error) {
		<-stuck
		return nil, errors.New("unreachable")
	}})
	m.color, m.cur, m.width, m.height = true, 1, 80, 40
	m.timeout = 20 * time.Millisecond
	m = deliver(m, m.Init())
	if v := ansi.Strip(m.View()); strings.Count(v, "no preview") != 2 {
		t.Errorf("pictures that never load should say no preview:\n%s", v)
	}
	if n := len(m.slots); n != 0 {
		t.Errorf("%d load slot(s) still held by loads that gave up", n)
	}
}

func TestNoPanesWithoutAPictureToShow(t *testing.T) {
	cases := map[string]func(*model){
		"documents":      func(m *model) { m.cur = 2 },
		"no color":       func(m *model) { m.color = false },
		"no loader":      func(m *model) { m.opts.Thumbnail = nil },
		"a short window": func(m *model) { m.height = 16 },
	}
	for name, setup := range cases {
		l := newLoader()
		m := picModel(l)
		setup(m)
		if cmd := m.Init(); cmd != nil {
			t.Errorf("%s: Init started a load", name)
		}
		if v := ansi.Strip(m.View()); strings.Contains(v, "loading…") || strings.Contains(v, "★ copy DSC") {
			t.Errorf("%s: panes were drawn:\n%s", name, v)
		}
	}
}

func TestCacheStaysBounded(t *testing.T) {
	m := newModel(fixture(), Options{})
	m.shown.Store(&[2]string{"/p/0.jpg", ""})
	for i := range maxThumbs + 10 {
		m.remember(fmt.Sprintf("/p/%d.jpg", i), &thumb{loading: i == 1})
	}
	if len(m.thumbs) != maxThumbs || len(m.order) != maxThumbs {
		t.Errorf("cache holds %d (order %d), want %d", len(m.thumbs), len(m.order), maxThumbs)
	}
	for _, p := range []string{"/p/0.jpg", "/p/1.jpg"} {
		if _, ok := m.thumbs[p]; !ok {
			t.Errorf("%s was evicted, but it is on screen or still loading", p)
		}
	}
	if _, ok := m.thumbs["/p/2.jpg"]; ok {
		t.Error("the oldest picture off screen should go first")
	}
}

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
