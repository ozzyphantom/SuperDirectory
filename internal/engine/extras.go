package engine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ozzyphantom/SuperDirectory/internal/dedup"
	"github.com/ozzyphantom/SuperDirectory/internal/exif"
	"github.com/ozzyphantom/SuperDirectory/internal/expand"
	"github.com/ozzyphantom/SuperDirectory/internal/filter"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/guard"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
	"github.com/ozzyphantom/SuperDirectory/internal/textdup"
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

// textOf extracts a document's text, by its name's extension, or reports that it
// cannot. Plain text formats are read directly; richer formats join through
// package textual.
var textOf = func(name string, f exif.File, size int64, limit int) (string, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".text", ".md", ".markdown", ".log", ".csv", ".tsv", ".rst":
		b, err := io.ReadAll(io.LimitReader(f, int64(limit)*4))
		return string(b), err
	}
	return "", errNoText
}

var errNoText = errors.New("no text reader for this format")

// docMinWords is the least text a document needs to be compared. Short pages that
// are mostly a site's shared navigation look alike to any measure of text; real
// revisions of a document are longer than that.
const docMinWords = 200

// docThreshold is the estimated share of shared five-word runs that makes two
// documents near-duplicates. Calibrated in package textdup: a revision with one
// paragraph rewritten stays above it; documents on one topic with a shared header
// and footer sit far below.
const docThreshold = 0.8

// documentDuplicates finds documents that are mostly the same text, among those
// the earlier scans did not already skip. Each group keeps its newest file — a
// revision supersedes the one before — and skips only the files that match the
// one kept: groups can chain revisions A-B-C where A and C barely match, and C
// must not be skipped on B's account.
func (r *run) documentDuplicates(items []flatten.Item, sets []DupSet) ([]DupSet, error) {
	skipped := map[int]bool{}
	for _, s := range sets {
		for _, i := range s.Skip {
			skipped[i] = true
		}
	}
	g := guard.Reader{Stall: flatten.DefaultStallTimeout, Cancel: r.stop}
	var sigs []textdup.Signature
	var idx []int
	for i, it := range items {
		if skipped[i] {
			continue
		}
		r.h.Scan(dedup.Progress{Phase: dedup.Hashing, Done: i, Total: len(items), Current: filepath.Base(it.Src)})
		text, err := guard.Read(g, it.Src, func(f exif.File) (string, error) {
			return textOf(it.Src, f, it.Size, 2_000_000)
		})
		if errors.Is(err, guard.ErrCanceled) {
			return nil, ErrStopped
		}
		if err != nil || len(strings.Fields(text)) < docMinWords {
			continue
		}
		if sig, ok := textdup.Sign(text); ok {
			sigs = append(sigs, sig)
			idx = append(idx, i)
		}
	}
	matches := textdup.Find(sigs, docThreshold)
	var out []DupSet
	for _, group := range textdup.Groups(matches) {
		keep := group[0]
		for _, k := range group[1:] {
			a, b := items[idx[k]], items[idx[keep]]
			if a.ModTime.After(b.ModTime) || (a.ModTime.Equal(b.ModTime) && len(a.Src) < len(b.Src)) {
				keep = k
			}
		}
		set := DupSet{Kind: job.Documents, Keep: idx[keep], Score: 1}
		for _, k := range group {
			if k == keep {
				continue
			}
			score := textdup.Similarity(sigs[k], sigs[keep])
			if score < docThreshold {
				continue // matched a neighbour in the chain, not the file kept
			}
			set.Skip = append(set.Skip, idx[k])
			set.Bytes += items[idx[k]].Size
			set.Score = min(set.Score, score)
		}
		if len(set.Skip) > 0 {
			sort.Ints(set.Skip)
			out = append(out, set)
		}
	}
	return out, nil
}

// cleanup removes what the run staged inside the target. A stopped run re-stages
// on resume, so nothing staged is worth keeping either way.
func (r *run) cleanup() {
	os.RemoveAll(filepath.Join(StateDir(r.j.Target), "expanded"))
	os.RemoveAll(filepath.Join(StateDir(r.j.Target), "merged"))
}
