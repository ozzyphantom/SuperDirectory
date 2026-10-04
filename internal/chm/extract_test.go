package chm

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExtractWritesEveryEntry(t *testing.T) {
	entries := sample()
	f := openBuilt(t, buildCHM(t, chmSpec{}, entries))
	dir := filepath.Join(t.TempDir(), "out", "nested") // Extract creates it
	if err := f.Extract(dir); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string][]byte{}
	for _, e := range userFiles(entries) {
		want[filepath.FromSlash(e.name)] = e.data
	}
	got := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		got[rel] = b
		info, _ := d.Info()
		if perm := info.Mode().Perm(); perm&0o600 != 0o600 || perm&0o133 != 0 {
			t.Errorf("%s has mode %v; Extract writes 0o644", rel, perm)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Errorf("wrote %d files, want %d: %v", len(got), len(want), keys(got))
	}
	for name, data := range want {
		if !bytes.Equal(got[name], data) {
			t.Errorf("%s: wrong contents", name)
		}
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestExtractConfinement: entries named to climb out of the folder, or to land
// at an absolute path, are skipped; everything else is written; and nothing at
// all appears outside the folder.
func TestExtractConfinement(t *testing.T) {
	hostile := []string{
		"../escape.htm",
		"/abs.htm",
		"a/../../b.htm",
		"a/../b.htm",
		"..",
		`..\win.htm`,
		`a\..\..\c.htm`,
		"/",
		"nul\x00.htm",
		"bad\xff.htm",
	}
	var entries []testEntry
	for _, n := range hostile {
		// The directory stores names with a leading "/", which Entry.Name drops.
		entries = append(entries, testEntry{name: "/" + n, section: 1, data: []byte("hostile " + n)})
	}
	entries = append(entries,
		testEntry{name: "../raw.htm", section: 0, data: []byte("stored without its slash")},
		testEntry{name: "/html/ok.htm", section: 1, data: []byte("fine")},
		testEntry{name: "/./dot.htm", section: 0, data: []byte("cleaned to dot.htm")},
	)
	f := openBuilt(t, buildCHM(t, chmSpec{}, entries))

	root := t.TempDir()
	dir := filepath.Join(root, "sub", "out")
	err := f.Extract(dir)
	if err == nil {
		t.Fatal("Extract reported no skipped entries")
	}
	for _, n := range append(hostile, "../raw.htm") {
		if n == "/" { // listed as a folder, so never a file to skip
			continue
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("%q", n)) {
			t.Errorf("the error does not name %q: %v", n, err)
		}
	}
	var outside []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			outside = append(outside, p)
		}
		return nil
	})
	if len(outside) > 0 {
		t.Fatalf("wrote outside the folder: %v", outside)
	}
	for name, want := range map[string]string{"html/ok.htm": "fine", "dot.htm": "cleaned to dot.htm"} {
		if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))); err != nil || string(b) != want {
			t.Errorf("%s: %q, %v", name, b, err)
		}
	}
}

// TestExtractRefusesSymlinks: a link already inside the folder that leads out
// of it must not carry a write with it.
func TestExtractRefusesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "out")
	elsewhere := filepath.Join(root, "elsewhere")
	for _, d := range []string{dir, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	f := openBuilt(t, buildCHM(t, chmSpec{}, []testEntry{{name: "/link/evil.htm", section: 0, data: []byte("evil")}}))
	if err := f.Extract(dir); err == nil {
		t.Error("Extract followed a link out of the folder without an error")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "evil.htm")); err == nil {
		t.Fatal("wrote through the link, outside the folder")
	}
}

// TestExtractSkipsCaseVariants: on macOS and Windows, Index.htm and index.htm
// are one file, so the second would overwrite the first.
func TestExtractSkipsCaseVariants(t *testing.T) {
	entries := []testEntry{
		{name: "/Index.htm", section: 1, data: []byte("upper")},
		{name: "/index.htm", section: 1, data: []byte("lower")},
		{name: "/other.htm", section: 0, data: []byte("other")},
	}
	f := openBuilt(t, buildCHM(t, chmSpec{}, entries))
	dir := t.TempDir()
	err := f.Extract(dir)
	if err == nil || !strings.Contains(err.Error(), `"index.htm"`) {
		t.Fatalf("Extract: %v; want it to name the skipped index.htm", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Index.htm")); string(b) != "upper" {
		t.Errorf("Index.htm holds %q, want the first entry's", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "other.htm")); string(b) != "other" {
		t.Errorf("other.htm holds %q", b)
	}
}

func TestExtractLimits(t *testing.T) {
	entries := sample()
	b := buildCHM(t, chmSpec{}, entries)
	openLimited := func(lim limits) *File {
		f, err := open(bytes.NewReader(b.data), int64(len(b.data)), lim)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	lim := defaultLimits
	lim.total = 100_000 // the sample adds up to about 175,000 bytes
	dir := t.TempDir()
	if err := openLimited(lim).Extract(dir); err == nil {
		t.Fatal("extracted past the total limit")
	}
	if names, _ := os.ReadDir(dir); len(names) != 0 {
		t.Errorf("wrote %d entries before refusing; it should write none", len(names))
	}

	lim = defaultLimits
	lim.entry = 60_000 // setup.htm is 120,000 bytes
	dir = t.TempDir()
	err := openLimited(lim).Extract(dir)
	if err == nil || !strings.Contains(err.Error(), "setup.htm") {
		t.Fatalf("Extract: %v; want it to name the oversized setup.htm", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "html", "index.htm")); err != nil {
		t.Errorf("the entries within the limit should still be written: %v", err)
	}
}

// TestExtractCarriesOnPastBadData: an entry that will not read is skipped and
// named; the rest are written.
func TestExtractCarriesOnPastBadData(t *testing.T) {
	entries := sample()
	f := openBuilt(t, buildCHM(t, chmSpec{}, entries))
	f.files[f.index["notes.txt"]].offset = 1 << 40
	dir := t.TempDir()
	err := f.Extract(dir)
	if err == nil || !strings.Contains(err.Error(), "notes.txt") {
		t.Fatalf("Extract: %v; want it to name notes.txt", err)
	}
	for _, name := range []string{"html/index.htm", "html/setup.htm", "images/logo.gif", "html/empty.htm"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestExtractBrokenSection: when the compressed section cannot be read at all,
// the stored entries are still written, and the error says so once.
func TestExtractBrokenSection(t *testing.T) {
	entries := sample()
	b := buildCHM(t, chmSpec{}, entries)
	rename(t, b.data, controlName)
	f := openBuilt(t, b)
	dir := t.TempDir()
	err := f.Extract(dir)
	if err == nil || strings.Count(err.Error(), "ControlData") != 1 || !strings.Contains(err.Error(), "all 2 compressed entries") {
		t.Fatalf("Extract: %v; want one error for the 2 compressed entries", err)
	}
	for _, name := range []string{"images/logo.gif", "notes.txt", "html/empty.htm"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLocalPath(t *testing.T) {
	for name, want := range map[string]string{
		"a.htm":           "a.htm",
		"html/a.htm":      filepath.Join("html", "a.htm"),
		"./a.htm":         "a.htm",
		"html//a.htm":     filepath.Join("html", "a.htm"),
		`html\a.htm`:      filepath.Join("html", "a.htm"),
		"../a.htm":        "",
		"html/../../a":    "",
		"html/../a":       "",
		"/a.htm":          "",
		`\a.htm`:          "",
		".":               "",
		"":                "",
		"a\x00b":          "",
		"\xc3\x28.htm":    "",
		"trailing/../":    "",
		"x/./../y":        "",
		"deep/./ok/x.htm": filepath.Join("deep", "ok", "x.htm"),
	} {
		got, err := localPath(name)
		if want == "" {
			if err == nil {
				t.Errorf("localPath(%q) = %q; want it refused", name, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("localPath(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
}
