package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/dedup"
	"github.com/ozzyphantom/SuperDirectory/internal/exif"
	"github.com/ozzyphantom/SuperDirectory/internal/expand"
	"github.com/ozzyphantom/SuperDirectory/internal/filter"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/guard"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
	"github.com/ozzyphantom/SuperDirectory/internal/merge"
	"github.com/ozzyphantom/SuperDirectory/internal/organize"
	"github.com/ozzyphantom/SuperDirectory/internal/sniff"
	"github.com/ozzyphantom/SuperDirectory/internal/textdup"
	"github.com/ozzyphantom/SuperDirectory/internal/textual"
)

// unrenamed are detected types a file is never renamed to. Family names — ole,
// elf, macho — are not extensions. A program or package is left as it is: a
// misnamed one is usually misnamed on purpose, and renaming a file so that a
// double-click runs it is not a tidy-up.
var unrenamed = map[string]bool{
	"ole": true, "elf": true, "macho": true,
	"exe": true, "class": true, "wasm": true, "jar": true, "apk": true,
}

// serverPages are the extensions of pages a server runs. Text fits any name in
// general, but HTML under one of these is a saved page — a scrape's
// "report.php" — and reads as a web page only when named one. A page with the
// server's code still in it is source, and keeps its name.
var serverPages = map[string]bool{
	"php": true, "php3": true, "php4": true, "php5": true, "phtml": true,
	"asp": true, "aspx": true, "jsp": true, "jspx": true, "cfm": true,
	"cgi": true, "do": true, "action": true,
}

// serverCode reports whether a page's start holds code a server would have run.
func serverCode(head []byte) bool {
	h := bytes.ToLower(head)
	return bytes.Contains(h, []byte("<?php")) || bytes.Contains(h, []byte("<?=")) || bytes.Contains(h, []byte("<%"))
}

// detectType reads the start of a file and, when its content disagrees with its
// name — a page saved as "report.php", a HEIC photo named ".jpg", a PDF with no
// extension at all — renames it to the extension its content shows, for the
// layout and for the program that opens it. The report keeps the old name.
func (r *run) detectType(g guard.Reader, f *flatten.File) error {
	var code bool
	detected, err := guard.Read(g, f.Path, func(fh exif.File) (string, error) {
		head := make([]byte, min(f.Size, sniff.HeadSize))
		n, _ := fh.ReadAt(head, 0)
		code = serverCode(head[:n])
		return sniff.DetectFile(fh, f.Size)
	})
	if err != nil {
		return err
	}
	name := f.BaseName()
	ext := organize.Extension(name)
	saved := detected == "html" && serverPages[ext] && !code
	if detected == "" || unrenamed[detected] || (sniff.Agrees(ext, detected) && !saved) {
		return nil
	}
	stem := name
	note := fmt.Sprintf("type: the content is %s; it had no extension", detected)
	if ext != "" {
		if n := len(name) - len(ext); n > 1 && strings.EqualFold(name[n:], ext) {
			stem = name[:n-1]
		}
		note = fmt.Sprintf("type: the content is %s, not .%s", detected, ext)
	}
	f.Name, f.Ext = stem+"."+detected, detected
	r.notes[f.Path] = note
	r.sum.Retyped++
	return nil
}

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

// mergeable are the text documents merging joins: prose and markup, read by
// package textual. PDFs stay whole, since NotebookLM reads them with their
// figures; code, configuration and tables stay whole too, since merged they
// would bury the prose.
var mergeable = map[string]bool{
	".txt": true, ".text": true, ".nfo": true,
	".md": true, ".markdown": true, ".mdown": true,
	".rst": true, ".adoc": true, ".asciidoc": true, ".org": true, ".tex": true,
	".srt": true, ".vtt": true,
	".html": true, ".htm": true, ".xhtml": true, ".shtml": true,
	".rtf": true, ".docx": true, ".docm": true, ".odt": true, ".epub": true,
}

// mergeText joins the text documents of each destination folder into a few
// Markdown files, "<folder> 001.md" and on, each under NotebookLM's limits: a
// documentation scrape of two thousand pages becomes a handful of sources. Each
// document sits under a heading naming where it came from. A folder with one
// document keeps it as it is, and a document with no readable text is copied
// whole, with the reason noted. The merged files are staged in the run's folder
// and moved into place.
func (r *run) mergeText(items []flatten.Item) ([]flatten.Item, error) {
	groups := map[string][]int{}
	var dirs []string
	for i, it := range items {
		if !mergeable[strings.ToLower(filepath.Ext(it.Src))] {
			continue
		}
		dir := filepath.Dir(it.Want)
		if groups[dir] == nil {
			dirs = append(dirs, dir)
		}
		groups[dir] = append(groups[dir], i)
	}
	var total int
	for _, d := range dirs {
		if len(groups[d]) > 1 {
			total += len(groups[d])
		}
	}
	if total == 0 {
		return items, nil
	}
	r.h.Stage(Merging)
	staging := filepath.Join(StateDir(r.j.Target), "merged")
	os.RemoveAll(staging) // a stopped run's leftovers
	g := guard.Reader{Stall: flatten.DefaultStallTimeout, Cancel: r.stop}

	merged := map[int][]flatten.Item{} // by the index of each group's first document
	gone := map[int]bool{}
	done := 0
	for k, dir := range dirs {
		members := groups[dir]
		if len(members) < 2 {
			continue
		}
		base := filepath.Base(dir)
		if dir == "." {
			base = filepath.Base(r.j.Target)
		}
		w, err := merge.NewWriter(filepath.Join(staging, strconv.Itoa(k)), base, merge.NotebookLM)
		if err != nil {
			return nil, err
		}
		var newest time.Time
		var joined []int
		into := map[int]string{} // the merged file each document starts in
		for _, i := range members {
			it := items[i]
			r.h.Progress(Merging, done, total, filepath.Base(it.Src))
			done++
			text, err := guard.Read(g, it.Src, func(f exif.File) (string, error) {
				return textual.Text(it.Src, f, it.Size, 0)
			})
			if errors.Is(err, guard.ErrCanceled) {
				w.Close()
				return nil, ErrStopped
			}
			if text == "" {
				reason := "no readable text"
				if err != nil {
					reason = err.Error()
				}
				r.notes[it.Src] = "not merged: " + reason
				continue
			}
			origin := it.Rel
			if o, ok := r.origin[it.Src]; ok {
				origin = o
			}
			file, err := w.Add(origin, text)
			if err != nil {
				w.Close()
				return nil, fmt.Errorf("merging %s: %w", it.Src, err)
			}
			into[i] = file
			joined = append(joined, i)
			if it.ModTime.After(newest) {
				newest = it.ModTime
			}
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		if len(joined) == 0 {
			continue
		}
		var out []flatten.Item
		for _, f := range w.Files() {
			// The newest document's date, not the time of this run: a resumed run
			// writes the same files with the same dates, and finds them already there.
			os.Chtimes(f, newest, newest)
			info, err := os.Stat(f)
			if err != nil {
				return nil, err
			}
			want := filepath.Join(dir, filepath.Base(f))
			out = append(out, flatten.Item{Src: f, Want: want, Dst: want, Rel: want, Size: info.Size(), ModTime: newest})
		}
		merged[joined[0]] = out
		for _, i := range joined {
			gone[i] = true
			it := items[i]
			src := it.Src
			if o, ok := r.origin[src]; ok {
				src = o
			}
			// dst holds the staged file for now; the report swaps in where it landed.
			r.gone = append(r.gone, row{status: "merged", src: src, rel: it.Rel, dst: into[i], bytes: it.Size})
		}
		r.sum.Merged += len(joined)
	}
	r.h.Progress(Merging, total, total, "")

	var out []flatten.Item
	for i, it := range items {
		out = append(out, merged[i]...)
		if !gone[i] {
			out = append(out, it)
		}
	}
	flatten.Assign(out)
	return out, nil
}

// textOf extracts a document's text, or reports that it cannot. A variable so
// tests can stand in for the readers.
var textOf = func(name string, f exif.File, size int64, limit int) (string, error) {
	return textual.Text(name, f, size, limit)
}

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
// revision supersedes the one before, see newer — and skips only the files that match the
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
		if skipped[i] || !textual.Supported(it.Src) {
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
			if newer(items[idx[k]], items[idx[keep]]) {
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

// newer reports whether a supersedes b as the document to keep: the newer file,
// and on a tie the larger — the same page as rendered HTML and as its source
// carry one date, and the rendering holds what the source only includes — then
// the shorter path.
func newer(a, b flatten.Item) bool {
	if !a.ModTime.Equal(b.ModTime) {
		return a.ModTime.After(b.ModTime)
	}
	if a.Size != b.Size {
		return a.Size > b.Size
	}
	return len(a.Src) < len(b.Src)
}

// cleanup removes what the run staged inside the target. A stopped run re-stages
// on resume, so nothing staged is worth keeping either way.
func (r *run) cleanup() {
	os.RemoveAll(filepath.Join(StateDir(r.j.Target), "expanded"))
	os.RemoveAll(filepath.Join(StateDir(r.j.Target), "merged"))
}
