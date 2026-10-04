package expand

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// TestZipRefusesWhatIsNotAPlainFile: escapes, absolute paths, Finder's sidecar
// folder, links and encrypted entries never land; the plain files do, with their
// times.
func TestZipRefusesWhatIsNotAPlainFile(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	when := time.Date(2015, 6, 1, 12, 0, 0, 0, time.UTC)
	add := func(name string, body string, mode os.FileMode, flags uint16) {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: when, Flags: flags}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	add("docs/setup.htm", "setup", 0o644, 0)
	add(`docs\win\path.htm`, "windows tools write backslashes", 0o644, 0)
	add("../escape.htm", "nope", 0o644, 0)
	add("/abs.htm", "nope", 0o644, 0)
	add("a/../../b.htm", "nope", 0o644, 0)
	add("__MACOSX/docs/._setup.htm", "sidecar", 0o644, 0)
	add("link", "/etc/passwd", os.ModeSymlink|0o777, 0)
	add("secret.txt", "encrypted", 0o644, 0x1)
	zw.Close()

	dir := t.TempDir()
	out, skipped, err := Unpack(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "manual.zip", dir, Default)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(listDir(t, dir), ",")
	if got != "docs/setup.htm,docs/win/path.htm" {
		t.Errorf("unpacked %s", got)
	}
	if len(out) != 2 || skipped != 6 {
		t.Errorf("entries %d, skipped %d; want 2 and 6", len(out), skipped)
	}
	if info, _ := os.Stat(filepath.Join(dir, "docs", "setup.htm")); !info.ModTime().Equal(when) {
		t.Errorf("time %v, want %v", info.ModTime(), when)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.htm")); err == nil {
		t.Error("an entry escaped the folder")
	}
}

func TestTarGzSkipsLinksAndEscapes(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	file := func(name, body string) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg, ModTime: time.Now()})
		tw.Write([]byte(body))
	}
	file("guide/intro.md", "# Intro")
	tw.WriteHeader(&tar.Header{Name: "guide/", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Name: "guide/link", Typeflag: tar.TypeSymlink, Linkname: "/etc"})
	file("../../outside.md", "nope")
	tw.Close()
	gz.Close()

	dir := t.TempDir()
	out, skipped, err := Unpack(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "guide.tar.gz", dir, Default)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listDir(t, dir), ","); got != "guide/intro.md" || len(out) != 1 || skipped != 2 {
		t.Errorf("unpacked %s, %d entries, %d skipped", got, len(out), skipped)
	}
}

// TestBombsStopAtTheLimit: ten megabytes of zeros compress to a few kilobytes. With
// a tight ratio the unpacking must stop rather than fill the disk.
func TestBombsStopAtTheLimit(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("zeros.bin")
	w.Write(make([]byte, 10<<20))
	zw.Close()

	tight := Limits{Files: 10, Bytes: 1 << 20, Ratio: 1}
	_, _, err := Unpack(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "bomb.zip", t.TempDir(), tight)
	if !errors.Is(err, ErrLimit) {
		t.Errorf("err = %v, want ErrLimit", err)
	}
}

func TestSupportedAndStem(t *testing.T) {
	for name, want := range map[string]bool{"a.zip": true, "a.TAR.GZ": true, "a.tgz": true, "a.tar.bz2": true, "a.7z": false, "a.chm": CHM != nil} {
		if Supported(name) != want {
			t.Errorf("Supported(%s) = %v", name, !want)
		}
	}
	if Stem("Manual.tar.gz") != "Manual" || Stem("help.CHM") != "help" || Stem("notes.txt") != "notes.txt" {
		t.Error("Stem")
	}
}
