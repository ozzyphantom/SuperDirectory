// Package flatten holds the functional core of SuperDirectory: walk a source
// tree, decide a collision-free destination name for every file, and copy.
//
// This package has NO knowledge of the TUI. It is pure, deterministic, and
// unit-testable — that separation is one of the reasons Go was chosen: the
// core stays independent of the interactive layer.
package flatten

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Item is one planned copy: an absolute source path and the final, unique
// filename it will take inside the target directory.
type Item struct {
	Src     string
	DstName string
}

// Failure records a file that could not be copied, with the cause.
type Failure struct {
	Src string
	Err error
}

// Plan walks source and returns the ordered copy plan. Directories whose
// absolute path is present in excluded (and everything beneath them) are
// skipped. Files in the source root keep their name; files in a subdirectory
// are prefixed with "<parentdir>_" to preserve context. Remaining collisions
// get a numeric "_1", "_2" suffix — matching the Python implementation.
func Plan(source string, excluded map[string]bool) ([]Item, error) {
	var items []Item
	used := map[string]bool{}

	err := filepath.WalkDir(source, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Tolerate unreadable entries (mirrors the Python code's
			// `except PermissionError` behavior) instead of aborting.
			return nil
		}
		if d.IsDir() {
			if path != source && excluded[path] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// Skip symlinks, sockets, devices — copy real files only.
			return nil
		}

		parent := filepath.Dir(path)
		name := d.Name()
		newName := name
		if parent != source {
			newName = filepath.Base(parent) + "_" + name
		}
		items = append(items, Item{Src: path, DstName: unique(used, newName)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// unique reserves name in used, appending _1, _2, ... before the extension
// until it finds a free slot.
func unique(used map[string]bool, name string) string {
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
func Copy(target string, items []Item, onProgress func(done, total int)) []Failure {
	var failures []Failure
	total := len(items)
	for i, it := range items {
		dst := filepath.Join(target, it.DstName)
		if err := copyFile(it.Src, dst); err != nil {
			failures = append(failures, Failure{Src: it.Src, Err: err})
		}
		if onProgress != nil {
			onProgress(i+1, total)
		}
	}
	return failures
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
