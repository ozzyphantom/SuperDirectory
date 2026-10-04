// Package expand unpacks archives found in the sources, so their contents are
// copied as files rather than as one opaque archive. Documentation scrapes often
// arrive zipped, and old vendor manuals ship as compiled help (.chm).
//
// Archives are untrusted. An entry may name a path outside the folder it is
// unpacked into ("zip slip"); it may be a link or a device; the archive may be a
// bomb that expands a kilobyte into terabytes. Each of those is refused, and
// unpacking stops at the limits.
package expand

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/fsmeta"
)

// Entry is one file unpacked from an archive.
type Entry struct {
	Rel     string // path inside the archive, with the OS separator
	Size    int64
	ModTime time.Time
}

// Limits bound what one archive may unpack to.
type Limits struct {
	Files int     // entries
	Bytes int64   // total unpacked bytes
	Ratio float64 // unpacked bytes per archive byte, above a 100 MB allowance
}

// Default is generous for real archives and stops bombs: a 42 KB zip that would
// expand to petabytes trips the ratio in its first gigabyte.
var Default = Limits{Files: 100_000, Bytes: 20 << 30, Ratio: 200}

// ErrLimit is returned when an archive passes a limit. What was unpacked before it
// stays.
var ErrLimit = errors.New("the archive expands past the safety limits")

// CHM, when set, lists and reads compiled help files. The reader lives in its own
// package; a nil CHM means .chm files are copied whole.
var CHM func(r io.ReaderAt, size int64) (Archive, error)

// Archive is a format reader that lists entries and reads one at a time.
type Archive interface {
	Names() []string
	Read(name string) ([]byte, error)
}

// Supported reports whether name is an archive this package unpacks.
func Supported(name string) bool {
	return format(name) != ""
}

// Stem is name without its archive extension: "manual.tar.gz" -> "manual".
func Stem(name string) string {
	lower := strings.ToLower(name)
	for _, ext := range []string{".tar.gz", ".tar.bz2", ".tgz", ".tbz2", ".tar", ".zip", ".chm"} {
		if strings.HasSuffix(lower, ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
}

func format(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tgz"
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return "tbz2"
	case strings.HasSuffix(lower, ".chm"):
		if CHM != nil {
			return "chm"
		}
	}
	return ""
}

// Unpack writes the archive's regular files under dir and returns them. name
// chooses the format. Entries that would escape dir, links, devices, encrypted
// entries and filesystem bookkeeping are skipped; skipped reports how many.
func Unpack(r io.ReaderAt, size int64, name, dir string, lim Limits) (entries []Entry, skipped int, err error) {
	u := &unpacker{dir: dir, lim: lim, budget: lim.Bytes}
	if allowed := int64(float64(size)*lim.Ratio) + 100<<20; lim.Ratio > 0 && allowed < u.budget {
		u.budget = allowed
	}
	switch format(name) {
	case "zip":
		err = u.zip(r, size)
	case "tar":
		err = u.tar(io.NewSectionReader(r, 0, size))
	case "tgz":
		var gz *gzip.Reader
		if gz, err = gzip.NewReader(io.NewSectionReader(r, 0, size)); err == nil {
			err = u.tar(gz)
		}
	case "tbz2":
		err = u.tar(bzip2.NewReader(io.NewSectionReader(r, 0, size)))
	case "chm":
		err = u.chm(r, size)
	default:
		err = fmt.Errorf("%s is not an archive this can unpack", name)
	}
	return u.out, u.skipped, err
}

type unpacker struct {
	dir     string
	lim     Limits
	budget  int64
	out     []Entry
	skipped int
}

// clean turns an archive name into a safe relative path, or "" when it would
// leave the folder, names nothing, or is filesystem bookkeeping.
func clean(name string) string {
	name = strings.ReplaceAll(name, `\`, "/") // Windows tools write backslashes
	if strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return ""
	}
	c := path.Clean(name)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return ""
	}
	for _, part := range strings.Split(c, "/") {
		if part == ".." || fsmeta.IsMetadata(part) {
			return ""
		}
	}
	return filepath.FromSlash(c)
}

// write stores one entry, counting it against the limits.
func (u *unpacker) write(name string, mod time.Time, src io.Reader) error {
	rel := clean(name)
	if rel == "" {
		u.skipped++
		return nil
	}
	if len(u.out) >= u.lim.Files {
		return ErrLimit
	}
	dst := filepath.Join(u.dir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(src, u.budget+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if n > u.budget {
		os.Remove(dst)
		return ErrLimit
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	u.budget -= n
	if !mod.IsZero() {
		os.Chtimes(dst, time.Time{}, mod)
	}
	u.out = append(u.out, Entry{Rel: rel, Size: n, ModTime: mod})
	return nil
}

func (u *unpacker) zip(r io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		mode := f.Mode()
		if f.FileInfo().IsDir() || !mode.IsRegular() || f.Flags&0x1 != 0 { // 0x1: encrypted
			if !f.FileInfo().IsDir() {
				u.skipped++
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			u.skipped++ // an unsupported compression method, say; the rest still unpack
			continue
		}
		err = u.write(f.Name, f.Modified, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (u *unpacker) tar(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if err := u.write(h.Name, h.ModTime, tr); err != nil {
				return err
			}
		case tar.TypeDir, tar.TypeXGlobalHeader, tar.TypeXHeader:
		default:
			u.skipped++ // links, devices, FIFOs
		}
	}
}

func (u *unpacker) chm(r io.ReaderAt, size int64) error {
	a, err := CHM(r, size)
	if err != nil {
		return err
	}
	for _, name := range a.Names() {
		data, err := a.Read(name)
		if err != nil {
			u.skipped++
			continue
		}
		if err := u.write(name, time.Time{}, bytes.NewReader(data)); err != nil {
			return err
		}
	}
	return nil
}
