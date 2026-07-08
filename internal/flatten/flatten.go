// Package flatten holds the functional core of SuperDirectory: walk a source
// tree, decide a collision-free destination for every file, and copy.
//
// This package has NO knowledge of the TUI. It is pure, deterministic, and
// unit-testable — that separation is one of the reasons Go was chosen: the
// core stays independent of the interactive layer.
//
// Plan is one of two planners. The other, package organize, sorts files into
// type folders instead of one flat directory; both emit []Item and both are
// executed by Copy.
package flatten

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Item is one planned copy: an absolute source path, and the destination
// relative to the target directory. Plan produces bare filenames here;
// package organize produces nested paths like "Images/jpg/beach.jpg". Copy
// creates whatever parent directories the path implies.
type Item struct {
	Src string
	Dst string
}

// Failure records a file that could not be copied, with the cause.
type Failure struct {
	Src string
	Err error
}

// Walk visits every regular file under source in lexical order, skipping
// excluded directories and everything beneath them. Unreadable entries are
// tolerated rather than fatal, mirroring the Python code's `except
// PermissionError` behavior. Symlinks, sockets, and devices are skipped — real
// files only.
//
// Both planners share this traversal so that exclusion semantics can never
// drift between them.
func Walk(source string, excluded map[string]bool, fn func(path string, d os.DirEntry)) error {
	return filepath.WalkDir(source, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path != source && excluded[path] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fn(path, d)
		return nil
	})
}

// Plan walks source and returns the ordered copy plan for a flat superdirectory.
// Files in the source root keep their name; files in a subdirectory are prefixed
// with "<parentdir>_" to preserve context. Remaining collisions get a numeric
// "_1", "_2" suffix — matching the Python implementation.
func Plan(source string, excluded map[string]bool) ([]Item, error) {
	var items []Item
	used := map[string]bool{}

	err := Walk(source, excluded, func(path string, d os.DirEntry) {
		parent := filepath.Dir(path)
		name := d.Name()
		newName := name
		if parent != source {
			newName = filepath.Base(parent) + "_" + name
		}
		items = append(items, Item{Src: path, Dst: Unique(used, newName)})
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// Unique reserves name in used, appending _1, _2, ... before the extension
// until it finds a free slot. Exported so package organize can apply the same
// collision rule to its nested paths.
func Unique(used map[string]bool, name string) string {
	if !used[name] {
		used[name] = true
		return name
	}
	ext := filepath.Ext(name)
	base := name[:len(name)-len(ext)]
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s_%d%s", base, i, ext)
		if !used[cand] {
			used[cand] = true
			return cand
		}
	}
}

// Copy executes the plan into target, calling onProgress after each file
// (done, total). It never aborts on a single failure; instead it collects
// and returns them so the caller can report at the end.
//
// Items whose Dst names a nested path get their parent directories created on
// demand. The set of already-created directories is cached, so a plan with
// thousands of files in one type folder costs one mkdir, not thousands.
func Copy(target string, items []Item, onProgress func(done, total int)) []Failure {
	var failures []Failure
	total := len(items)
	made := map[string]bool{target: true}

	for i, it := range items {
		dst := filepath.Join(target, it.Dst)
		err := ensureParent(made, filepath.Dir(dst))
		if err == nil {
			err = copyFile(it.Src, dst)
		}
		if err != nil {
			failures = append(failures, Failure{Src: it.Src, Err: err})
		}
		if onProgress != nil {
			onProgress(i+1, total)
		}
	}
	return failures
}

// ensureParent creates dir once, remembering it. A failed MkdirAll is not
// cached, so a later item targeting the same directory retries rather than
// silently inheriting the earlier error.
func ensureParent(made map[string]bool, dir string) error {
	if made[dir] {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	made[dir] = true
	return nil
}

// copyFile streams src to dst, preserving the source's permission bits.
// io.Copy between two *os.File lets the runtime use the kernel fast path
// (copy_file_range on Linux) where available.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
