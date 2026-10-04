package chm

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// Extract writes every entry Entries lists under dir, creating folders as
// needed, each file with mode 0o644. It never writes outside dir.
//
// It skips an entry whose name is absolute, has a ".." component, or cannot
// be a file name on this system. It also skips an entry whose data will not
// read, one larger than 256 MiB, and one whose name differs from an earlier
// entry's only in letter case, which macOS and Windows would let overwrite the
// first. Extract carries on past every skip and returns them joined, each
// error naming its entry, or one error for all the compressed entries when the
// compressed section cannot be read at all. It writes nothing if the entries
// add up to more than 2 GiB, and it stops at the first file it cannot write.
func (f *File) Extract(dir string) error {
	type job struct {
		e    *entry
		path string
	}
	var (
		jobs  []job
		skips []error
		total int64
		seen  = make(map[string]string)
	)
	for i := range f.files {
		e := &f.files[i]
		p, err := localPath(e.Name)
		if err != nil {
			skips = append(skips, fmt.Errorf("chm: skipped %q: %w", e.Name, err))
			continue
		}
		folded := strings.ToLower(p)
		if first, dup := seen[folded]; dup {
			skips = append(skips, fmt.Errorf("chm: skipped %q: it differs from %q only in letter case", e.Name, first))
			continue
		}
		seen[folded] = e.Name
		if e.Size > f.lim.entry {
			skips = append(skips, fmt.Errorf("chm: skipped %q: %d bytes is more than the %d one entry may hold", e.Name, e.Size, f.lim.entry))
			continue
		}
		total += e.Size
		jobs = append(jobs, job{e, p})
	}
	if total > f.lim.total {
		return fmt.Errorf("chm: the entries add up to %d bytes, more than the %d Extract writes", total, f.lim.total)
	}
	// When the compressed section itself is unreadable, say so once, not once
	// for every entry in it.
	packed := func(j job) bool { return j.e.section == 1 && j.e.Size > 0 }
	if slices.ContainsFunc(jobs, packed) {
		f.mu.Lock()
		_, err := f.compressed()
		f.mu.Unlock()
		if err != nil {
			n := len(jobs)
			jobs = slices.DeleteFunc(jobs, packed)
			skips = append(skips, fmt.Errorf("chm: skipped all %d compressed entries: %w", n-len(jobs), err))
		}
	}
	// In stream order, each compressed read resumes where the last one stopped.
	slices.SortStableFunc(jobs, func(a, b job) int {
		return cmp.Or(cmp.Compare(a.e.section, b.e.section), cmp.Compare(a.e.offset, b.e.offset))
	})

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("chm: %w", err)
	}
	root, err := os.OpenRoot(dir) // refuses any path, or symlink, that leads out of dir
	if err != nil {
		return fmt.Errorf("chm: %w", err)
	}
	defer root.Close()
	for _, j := range jobs {
		data, err := f.read(j.e)
		if err != nil {
			skips = append(skips, fmt.Errorf("chm: skipped %q: %w", j.e.Name, err))
			continue
		}
		if parent := filepath.Dir(j.path); parent != "." {
			err = root.MkdirAll(parent, 0o755)
		}
		if err == nil {
			err = root.WriteFile(j.path, data, 0o644)
		}
		if err != nil {
			return errors.Join(append([]error{fmt.Errorf("chm: writing %q: %w", j.e.Name, err)}, skips...)...)
		}
	}
	return errors.Join(skips...)
}

// localPath turns an entry's name into a path inside the extraction folder,
// or says why it cannot be one. A backslash counts as a separator, as Windows
// reads it, so "..\x" is refused on every system alike.
func localPath(name string) (string, error) {
	if !utf8.ValidString(name) || strings.IndexByte(name, 0) >= 0 {
		return "", errors.New("the name is not valid text")
	}
	slashed := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(slashed, "/") {
		return "", errors.New("the name is absolute")
	}
	for part := range strings.SplitSeq(slashed, "/") {
		if part == ".." {
			return "", errors.New(`the name climbs out with ".."`)
		}
	}
	clean := path.Clean(slashed)
	p, err := filepath.Localize(clean)
	if err != nil || clean == "." || !filepath.IsLocal(p) {
		return "", errors.New("the name cannot be a file name here")
	}
	return p, nil
}
