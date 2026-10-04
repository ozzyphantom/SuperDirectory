package merge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestWords(t *testing.T) {
	for s, want := range map[string]int{
		"":                              0,
		"Hello, world!":                 2,
		"don't stop":                    2,
		"it’s fine":                     2,
		"snake_case_name":               1,
		"state-of-the-art":              4,
		"3.14 and 1,000":                5,
		"e\u0301tat café":               2,
		"日本語":                           3,
		"東京tower":                       3,
		"ひらがな カタカナ":                     8,
		"한국어 문장":                        2,
		"--- *** ''' ___ ’’":            0,
		"## docs/api/index.html":        4,
		"tab\tseparated\nlines":         3,
		"emoji 😀 between":               2,
		"x²+y² ½":                       3,
		"\u200dzero\u200cjoiners\u200d": 1,
	} {
		if got := Words(s); got != want {
			t.Errorf("Words(%q) = %d, want %d", s, got, want)
		}
	}
}

// read returns a file's contents and fails the test on an error.
func read(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newWriter(t testing.TB, lim Limits) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	w, err := NewWriter(dir, "notes", lim)
	if err != nil {
		t.Fatal(err)
	}
	return w, dir
}

func add(t testing.TB, w *Writer, origin, text string) string {
	t.Helper()
	f, err := w.Add(origin, text)
	if err != nil {
		t.Fatalf("Add(%q): %v", origin, err)
	}
	return f
}

func TestFormat(t *testing.T) {
	w, dir := newWriter(t, Limits{})
	add(t, w, "a.txt", "\n\n  \nalpha  \r\n\r\n")
	add(t, w, "b/c.html", "  beta\r\ngamma\rdelta")
	add(t, w, "bad\nname\t.txt", "x")
	add(t, w, "empty.txt", " \n\t\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := "## a.txt\n\nalpha\n\n" +
		"## b/c.html\n\n  beta\ngamma\ndelta\n\n" +
		"## bad name .txt\n\nx\n\n" +
		"## empty.txt\n"
	files := w.Files()
	if len(files) != 1 || files[0] != filepath.Join(dir, "notes 001.md") {
		t.Fatalf("files = %q", files)
	}
	if got := read(t, files[0]); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports every writable file as 0666; only Unix keeps the bits.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && (perm&^0o644 != 0 || perm&0o600 != 0o600) {
		t.Errorf("mode %v, want 0644 less the umask", perm)
	}
}

func TestWordLimit(t *testing.T) {
	// Each section is 4 words: "dN" and three in the text. Two fit under 10.
	w, dir := newWriter(t, Limits{Words: 10})
	var got []string
	for i := 1; i <= 5; i++ {
		got = append(got, filepath.Base(add(t, w, fmt.Sprintf("d%d", i), "one two three")))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"notes 001.md", "notes 001.md", "notes 002.md", "notes 002.md", "notes 003.md"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Add returned %q, want %q", got, want)
	}
	for _, f := range w.Files() {
		if n := Words(read(t, f)); n > 10 {
			t.Errorf("%s holds %d words", f, n)
		}
	}
	if got := read(t, filepath.Join(dir, "notes 002.md")); got != "## d3\n\none two three\n\n## d4\n\none two three\n" {
		t.Errorf("second file: %q", got)
	}
}

func TestByteLimit(t *testing.T) {
	// A section is 17 bytes; with the blank line between, two take 35.
	w, _ := newWriter(t, Limits{Bytes: 40})
	for i := 0; i < 5; i++ {
		add(t, w, "a", "0123456789")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files := w.Files()
	if len(files) != 3 {
		t.Fatalf("%d files, want 3", len(files))
	}
	for _, f := range files {
		if n := len(read(t, f)); n > 40 {
			t.Errorf("%s holds %d bytes", f, n)
		}
	}
	if got := read(t, files[0]); got != "## a\n\n0123456789\n\n## a\n\n0123456789\n" {
		t.Errorf("first file: %q", got)
	}
}

func TestSplit(t *testing.T) {
	// The continued heading takes 2 of the 12 words, leaving 10 for text.
	w, _ := newWriter(t, Limits{Words: 12})
	add(t, w, "small", "before")
	text := "one two three four five\n\nsix seven eight nine\n\nten eleven twelve\n\n" +
		"a b c d e f g h i j k l\n\nend"
	first := add(t, w, "big", text)
	last := add(t, w, "next", "tail words")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files := w.Files()
	if len(files) != 4 || first != files[1] || last != files[3] {
		t.Fatalf("files %q; Add returned %q and %q", files, first, last)
	}
	want := []string{
		"## small\n\nbefore\n",
		"## big\n\none two three four five\n\nsix seven eight nine\n",
		"## big (continued)\n\nten eleven twelve\n\na b c d e f g\n",
		"## big (continued)\n\nh i j k l\n\nend\n\n## next\n\ntail words\n",
	}
	for i, f := range files {
		got := read(t, f)
		if got != want[i] {
			t.Errorf("file %d:\n got %q\nwant %q", i+1, got, want[i])
		}
		if n := Words(got); n > 12 {
			t.Errorf("file %d holds %d words", i+1, n)
		}
	}
}

func TestSplitLines(t *testing.T) {
	// One paragraph too big for a file is cut between lines, not mid-line.
	w, _ := newWriter(t, Limits{Words: 6})
	add(t, w, "L", "a b\nc d\n  e f\ng h")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"## L\n\na b\nc d\n", "## L (continued)\n\ne f\ng h\n"}
	files := w.Files()
	if len(files) != len(want) {
		t.Fatalf("%d files, want %d", len(files), len(want))
	}
	for i, f := range files {
		if got := read(t, f); got != want[i] {
			t.Errorf("file %d: got %q, want %q", i+1, got, want[i])
		}
	}
}

func TestSplitBytes(t *testing.T) {
	text := strings.Repeat("abcdefghi ", 50) // 500 bytes of 10-byte words
	w, _ := newWriter(t, Limits{Bytes: 120})
	add(t, w, "doc", text)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var body []string
	for _, f := range w.Files() {
		got := read(t, f)
		if len(got) > 120 {
			t.Errorf("%s holds %d bytes", f, len(got))
		}
		head, rest, _ := strings.Cut(got, "\n\n")
		if head != "## doc" && head != "## doc (continued)" {
			t.Errorf("heading %q", head)
		}
		body = append(body, strings.Fields(rest)...)
	}
	if strings.Join(body, " ") != strings.TrimSpace(text) {
		t.Error("the parts do not add up to the document")
	}
}

func TestUnsplittable(t *testing.T) {
	// A word bigger than the limit cannot be cut without changing the text, so
	// it goes over the limit in a file of its own.
	w, _ := newWriter(t, Limits{Bytes: 30})
	word := strings.Repeat("x", 100)
	add(t, w, "a", "small")
	f := add(t, w, "w", "tiny "+word+" end")
	add(t, w, "b", "small")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files := w.Files()
	want := []string{
		"## a\n\nsmall\n",
		"## w\n\ntiny\n",
		"## w (continued)\n\n" + word + "\n",
		"## w (continued)\n\nend\n",
		"## b\n\nsmall\n", // 35 bytes with the part before it: over 30
	}
	if len(files) != len(want) || f != files[1] {
		t.Fatalf("files %q, Add returned %q", files, f)
	}
	for i, f := range files {
		if got := read(t, f); got != want[i] {
			t.Errorf("file %d: got %q, want %q", i+1, got, want[i])
		}
	}
}

func TestHeadingOverLimit(t *testing.T) {
	// The heading alone passes the limit: each word gets a file of its own, and
	// an empty document still gets its heading.
	w, _ := newWriter(t, Limits{Words: 1})
	add(t, w, "two words", "x y")
	add(t, w, "two words", "")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"## two words\n\nx\n", "## two words (continued)\n\ny\n", "## two words\n"}
	files := w.Files()
	if len(files) != len(want) {
		t.Fatalf("%d files, want %d", len(files), len(want))
	}
	for i, f := range files {
		if got := read(t, f); got != want[i] {
			t.Errorf("file %d: got %q, want %q", i+1, got, want[i])
		}
	}
}

func TestNumberingPast999(t *testing.T) {
	w, dir := newWriter(t, Limits{Words: 1})
	for i := 0; i < 1001; i++ {
		add(t, w, "d", "x") // two words: a file each
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files := w.Files()
	if len(files) != 1001 {
		t.Fatalf("%d files, want 1001", len(files))
	}
	for i, name := range map[int]string{0: "notes 001.md", 998: "notes 999.md", 999: "notes 1000.md", 1000: "notes 1001.md"} {
		if files[i] != filepath.Join(dir, name) {
			t.Errorf("file %d is %q, want %q", i+1, files[i], name)
		}
	}
}

func TestClose(t *testing.T) {
	w, dir := newWriter(t, Limits{})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 || len(w.Files()) != 0 {
		t.Errorf("a writer with no documents left %d files", len(entries))
	}
	if _, err := w.Add("a", "b"); err == nil {
		t.Error("Add after Close succeeded")
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	w, _ = newWriter(t, Limits{})
	add(t, w, "a", "b")
	files := w.Files()
	files[0] = "changed"
	if w.Files()[0] == "changed" {
		t.Error("Files returned the writer's own slice")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, w.Files()[0]); got != "## a\n\nb\n" {
		t.Errorf("after Close: %q", got)
	}
}

func TestExistingFile(t *testing.T) {
	w, dir := newWriter(t, Limits{})
	taken := filepath.Join(dir, "notes 001.md")
	if err := os.WriteFile(taken, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("a", "b"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("err = %v, want os.ErrExist", err)
	}
	if _, err := w.Add("c", "d"); !errors.Is(err, os.ErrExist) {
		t.Errorf("second Add: %v, want the first error again", err)
	}
	if err := w.Close(); !errors.Is(err, os.ErrExist) {
		t.Errorf("Close: %v, want the first error again", err)
	}
	if got := read(t, taken); got != "keep me" {
		t.Errorf("existing file overwritten: %q", got)
	}
}

func TestNewWriter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "made", "for", "it")
	if _, err := NewWriter(dir, "notes", NotebookLM); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("dir not created: %v", err)
	}
	for _, base := range []string{"", "a/b", `a\b`, "../up"} {
		if _, err := NewWriter(dir, base, Limits{}); err == nil {
			t.Errorf("base %q accepted", base)
		}
	}
	if _, err := NewWriter(dir, "x", Limits{Words: -1}); err == nil {
		t.Error("negative limit accepted")
	}
}

var heading = regexp.MustCompile(`^## d\d+( \(continued\))?$`)

// FuzzWriter sends random documents through a Writer with tiny limits. Every file
// must stay within the limits unless it holds one unsplittable unit, and the
// files together must hold every word of every document, in order.
func FuzzWriter(f *testing.F) {
	f.Add([]byte("one two\n\nthree\x00four five six\nseven"), uint8(3), uint16(40))
	f.Add([]byte("\x00\x00 \n\n\x00x\r\ny"), uint8(0), uint16(0))
	f.Add([]byte(strings.Repeat("word ", 50)), uint8(5), uint16(0))
	f.Add([]byte("日本語のテキスト\n\nmore text\x00\xff\xfe bad bytes"), uint8(2), uint16(30))
	f.Add([]byte("a\n\n\n\nb\n \n c\x00"+strings.Repeat("x", 90)), uint8(0), uint16(25))
	f.Fuzz(func(t *testing.T, data []byte, words uint8, size uint16) {
		if len(data) > 512 {
			return // the checks are about shape; volume only adds files to create
		}
		lim := Limits{Words: int(words % 40), Bytes: int64(size % 600)}
		dir := t.TempDir()
		w, err := NewWriter(dir, "fuzz", lim)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		last := 0
		for i, doc := range strings.Split(string(data), "\x00") {
			origin := fmt.Sprintf("d%d", i)
			file, err := w.Add(origin, doc)
			if err != nil {
				t.Fatal(err)
			}
			n := indexOf(w.Files(), file)
			if n < last {
				t.Fatalf("document %d went into file %d, after file %d", i, n+1, last+1)
			}
			last = n
			want = append(want, "##", origin)
			want = append(want, strings.Fields(strings.ToValidUTF8(doc, "\uFFFD"))...)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		files := w.Files()
		if entries, _ := os.ReadDir(dir); len(entries) != len(files) {
			t.Fatalf("%d files on disk, %d listed", len(entries), len(files))
		}
		var all strings.Builder
		for i, path := range files {
			if base := filepath.Base(path); base != fmt.Sprintf("fuzz %03d.md", i+1) {
				t.Fatalf("file %d is named %q", i+1, base)
			}
			s := read(t, path)
			all.WriteString(s)
			first, rest, _ := strings.Cut(s, "\n")
			if !heading.MatchString(first) || !strings.HasSuffix(s, "\n") {
				t.Fatalf("file %d is malformed: %q", i+1, s)
			}
			if lim.allows(Words(s), int64(len(s))) {
				continue
			}
			// Over the limit: one heading, and a body that cannot be cut.
			if rest != "" && (!strings.HasPrefix(rest, "\n") || len(strings.Fields(rest)) > 1) {
				t.Fatalf("file %d is over %+v with %d words, %d bytes: %q", i+1, lim, Words(s), len(s), s)
			}
		}

		// Every word arrives in order; all that is added is continued headings.
		got := strings.Fields(all.String())
		extra := map[string]int{}
		j := 0
		for _, g := range got {
			if j < len(want) && g == want[j] {
				j++
			} else {
				extra[g]++
			}
		}
		if j < len(want) {
			t.Fatalf("lost %q onward", want[j])
		}
		marks := extra["(continued)"]
		if extra["##"] != marks {
			t.Fatalf("extra tokens %v", extra)
		}
		delete(extra, "##")
		delete(extra, "(continued)")
		for tok, n := range extra {
			if !strings.HasPrefix(tok, "d") {
				t.Fatalf("extra token %q", tok)
			}
			marks -= n
		}
		if marks != 0 {
			t.Fatalf("continued headings do not add up: %v", extra)
		}
	})
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}
