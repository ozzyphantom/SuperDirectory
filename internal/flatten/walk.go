package flatten

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/fsmeta"
)

// File is one file a walk found, with what the planners need to know about it.
type File struct {
	Path    string // absolute
	Rel     string // relative to the plan's root; see Scan.Files
	Size    int64
	ModTime time.Time

	// The stages between walking and planning may set these. Zero values mean
	// "take it from the file name".
	Name  string    // the name to plan under: a document's own title, a corrected extension
	Ext   string    // the type to classify by, lowercase without the dot
	Taken time.Time // when a photo or video was taken, for the date layout
}

// BaseName is the name the file is planned under.
func (f File) BaseName() string {
	if f.Name != "" {
		return f.Name
	}
	return filepath.Base(f.Path)
}

// Dir is the folder the file sat in, relative to the plan's root: "." for a file
// at the top.
func (f File) Dir() string { return filepath.Dir(f.Rel) }

// ErrStopped is returned when a walk is stopped through its Cancel channel.
var ErrStopped = errors.New("stopped")

// Scan describes a walk over one or more source folders.
type Scan struct {
	Sources  []string
	Excluded map[string]bool // absolute folders to skip with everything beneath them

	// Prune, when set, skips folders by name, with everything beneath them.
	Prune func(name string) bool
	// Keep, when set, decides which files are copied.
	Keep func(f File) bool
	// OnProgress, when set, is called every few hundred files with the count
	// found so far: measuring a drive's files is a stat per file, and on a slow
	// drive that is long enough to need a counter on screen.
	OnProgress func(found int)
	// Cancel, once closed, stops the walk with ErrStopped.
	Cancel <-chan struct{}
}

// Files walks every source in turn, in lexical order, and returns the regular
// files it is to copy, measured.
//
// Excluded folders are skipped with everything beneath them, and so is filesystem
// bookkeeping: .DS_Store, AppleDouble "._" sidecars, .Spotlight-V100/,
// $RECYCLE.BIN/ and friends (see package fsmeta). Without that, flattening the
// root of an external drive copies more operating-system metadata than user files.
// A source folder itself is never treated as bookkeeping: if the user points at
// .Trashes on purpose, that is their business. Symlinks, sockets and devices are
// skipped; unreadable entries are passed over rather than ending the walk.
//
// Each file is measured with one stat. On exFAT that is the cost of the walk many
// times over — 175 ms for 2,000 files against 4 ms — but the size and time feed
// the filters, the free-space check, resume, the report and an honest time
// estimate, so it is paid once here and nowhere else.
//
// Rel is the path relative to the source for a single source. With several, each
// source becomes a top-level folder named by Labels, so files from different
// sources never share a parent and every planner keeps them apart.
func (s Scan) Files() ([]File, error) {
	labels := Labels(s.Sources)
	var files []File
	for i, source := range s.Sources {
		err := filepath.WalkDir(source, func(path string, d fs.DirEntry, walkErr error) error {
			if closed(s.Cancel) {
				return ErrStopped
			}
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				if path == source {
					return nil
				}
				if s.Excluded[path] || fsmeta.IsMetadata(d.Name()) || (s.Prune != nil && s.Prune(d.Name())) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || fsmeta.IsMetadata(d.Name()) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil // vanished, or unreadable: nothing to copy
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return nil
			}
			if len(s.Sources) > 1 {
				rel = filepath.Join(labels[i], rel)
			}
			f := File{Path: path, Rel: rel, Size: info.Size(), ModTime: info.ModTime()}
			if s.Keep != nil && !s.Keep(f) {
				return nil
			}
			files = append(files, f)
			if s.OnProgress != nil && len(files)%256 == 0 {
				s.OnProgress(len(files))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if s.OnProgress != nil {
		s.OnProgress(len(files))
	}
	return files, nil
}

// Labels names each source for the top level of a multi-source plan: its folder
// name, with a numeric suffix when two sources share one ("Photos", "Photos_2").
// The comparison ignores case, as most destination drives do.
func Labels(sources []string) []string {
	out := make([]string, len(sources))
	used := map[string]bool{}
	for i, s := range sources {
		base := filepath.Base(filepath.Clean(s))
		if base == string(filepath.Separator) || base == "." || base == "" || strings.HasSuffix(base, ":") || strings.HasSuffix(base, `:\`) {
			base = "root"
		}
		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = base + "_" + itoa(n)
		}
		used[strings.ToLower(name)] = true
		out[i] = name
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
