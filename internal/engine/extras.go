package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ozzyphantom/SuperDirectory/internal/exif"
	"github.com/ozzyphantom/SuperDirectory/internal/expand"
	"github.com/ozzyphantom/SuperDirectory/internal/filter"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/guard"
)

// The stages below are filled in as their format packages land: type detection,
// titles, archive expansion, text merging, and near-duplicate documents.

func (r *run) detectType(g guard.Reader, f *flatten.File) error { return nil }

func (r *run) readTitle(g guard.Reader, f *flatten.File) error { return nil }

// expand unpacks the archives among the files into the run's staging folder,
// inside the destination, and puts their contents in the archive's place: an
// archive's files take the archive's folder and name, "manual.zip" becoming
// "manual/…". The archive itself is not copied. One that cannot be unpacked —
// damaged, encrypted, over the limits — is copied whole, with the reason noted.
// Archives inside archives are copied, not unpacked: one level is a choice, not
// a recursion.
func (r *run) expand(files []flatten.File, rules filter.Rules) ([]flatten.File, error) {
	var archives []int
	for i, f := range files {
		if expand.Supported(f.BaseName()) {
			archives = append(archives, i)
		}
	}
	if len(archives) == 0 {
		return files, nil
	}
	r.h.Stage(Expanding)
	staging := filepath.Join(StateDir(r.j.Target), "expanded")
	g := guard.Reader{Stall: flatten.DefaultStallTimeout, Cancel: r.stop}
	inner := map[int][]flatten.File{}
	for k, i := range archives {
		f := files[i]
		r.h.Progress(Expanding, k, len(archives), f.BaseName())
		dir := filepath.Join(staging, strconv.Itoa(k))
		os.RemoveAll(dir) // a stopped run's leftovers
		var skipped int
		entries, err := guard.Read(g, f.Path, func(fh exif.File) ([]expand.Entry, error) {
			e, s, err := expand.Unpack(fh, f.Size, f.BaseName(), dir, expand.Default)
			skipped = s
			return e, err
		})
		if errors.Is(err, guard.ErrCanceled) {
			return nil, ErrStopped
		}
		if err != nil && len(entries) == 0 {
			r.notes[f.Path] = "copied whole: could not expand (" + err.Error() + ")"
			continue
		}
		stem := expand.Stem(f.Rel)
		var kept []flatten.File
	entry:
		for _, e := range entries {
			for _, part := range strings.Split(filepath.Dir(e.Rel), string(filepath.Separator)) {
				if part != "." && rules.Prune(part) {
					continue entry
				}
			}
			mod := e.ModTime
			if mod.IsZero() {
				mod = f.ModTime
			}
			nf := flatten.File{Path: filepath.Join(dir, e.Rel), Rel: filepath.Join(stem, e.Rel), Size: e.Size, ModTime: mod}
			if !rules.Keep(nf) {
				continue
			}
			r.origin[nf.Path] = f.Path + "!/" + filepath.ToSlash(e.Rel)
			kept = append(kept, nf)
		}
		inner[i] = kept
		note := fmt.Sprintf("into %d file(s)", len(kept))
		if skipped > 0 {
			note += fmt.Sprintf("; %d entries refused (links, escapes, encrypted)", skipped)
		}
		if err != nil {
			note += "; stopped early: " + err.Error()
		}
		r.gone = append(r.gone, row{status: "expanded", src: f.Path, rel: f.Rel, bytes: f.Size, note: note})
		r.sum.Expanded += len(kept)
	}
	r.h.Progress(Expanding, len(archives), len(archives), "")

	var out []flatten.File
	for i, f := range files {
		if kept, ok := inner[i]; ok {
			out = append(out, kept...)
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// staged reports whether path is a file the run made in its staging folder.
func (r *run) staged(path string) bool {
	return strings.HasPrefix(path, StateDir(r.j.Target)+string(filepath.Separator))
}

func (r *run) mergeText(items []flatten.Item) ([]flatten.Item, error) { return items, nil }

func (r *run) documentDuplicates(items []flatten.Item, sets []DupSet) ([]DupSet, error) {
	return nil, nil
}

// cleanup removes what the run staged inside the target. A stopped run re-stages
// on resume, so nothing staged is worth keeping either way.
func (r *run) cleanup() {
	os.RemoveAll(filepath.Join(StateDir(r.j.Target), "expanded"))
	os.RemoveAll(filepath.Join(StateDir(r.j.Target), "merged"))
}
