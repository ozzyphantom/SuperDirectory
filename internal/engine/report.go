package engine

import (
	"encoding/csv"
	"fmt"

	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
)

// row is one line of the report.
type row struct {
	status, src, rel, dst string
	bytes                 int64
	note                  string
}

// notebookLMBytes is NotebookLM's limit per uploaded source.
const notebookLMBytes = 200_000_000

// writeReport leaves two files in the superdirectory's record folder: report.csv,
// a row per file — what happened to it and why — for searching and sorting; and
// report.md, the run at a glance. Every file the run considered is accounted for,
// so "where did this file go?" always has an answer.
func writeReport(r *run, items []flatten.Item, res flatten.Result) error {
	var rows []row
	for i, it := range items {
		src := it.Src
		if o, ok := r.origin[src]; ok {
			src = o
		}
		rw := row{src: src, rel: it.Rel, dst: it.Dst, bytes: it.Size, note: r.notes[it.Src]}
		switch res.Outcomes[i] {
		case flatten.Copied:
			rw.status = "copied"
		case flatten.Cloned:
			rw.status = "cloned"
		case flatten.Existing:
			rw.status = "already there"
		case flatten.Failed:
			rw.status = "failed"
		default:
			rw.status = "not reached"
		}
		rows = append(rows, rw)
	}
	for _, f := range res.Failures {
		for k := range rows {
			if rows[k].src == f.Src && rows[k].status == "failed" {
				rows[k].note = f.Err.Error()
			}
		}
	}
	landed := map[string]string{}
	for _, it := range items {
		if it.Move {
			landed[it.Src] = it.Dst
		}
	}
	for _, rw := range r.gone {
		if rw.status == "merged" {
			rw.dst = landed[rw.dst]
		}
		rows = append(rows, rw)
	}

	dir := StateDir(r.j.Target)
	if err := writeCSV(filepath.Join(dir, "report.csv"), rows); err != nil {
		return err
	}
	return job.WriteFileAtomic(filepath.Join(dir, "report.md"), []byte(markdown(r, rows, res)))
}

func writeCSV(path string, rows []row) error {
	var b strings.Builder
	w := csv.NewWriter(&b)
	w.Write([]string{"status", "source", "destination", "bytes", "note"})
	for _, rw := range rows {
		w.Write([]string{rw.status, rw.src, rw.dst, fmt.Sprint(rw.bytes), rw.note})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return job.WriteFileAtomic(path, []byte(b.String()))
}

// markdown renders the run at a glance: settings, counts, and the lists a reader
// checks first, each capped so a run of eleven thousand files stays readable.
func markdown(r *run, rows []row, res flatten.Result) string {
	const listCap = 200
	j := r.j
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	p("# SuperDirectory report\n\n")
	p("| | |\n|---|---|\n")
	p("| Started | %s |\n", r.start.Format("2006-01-02 15:04"))
	if r.sum.Stopped {
		p("| Finished | stopped before the end; run it again into the same folder to resume |\n")
	} else {
		p("| Finished | %s (%s) |\n", time.Now().Format("2006-01-02 15:04"), r.sum.Elapsed.Round(time.Second))
	}
	p("| Sources | %s |\n", strings.Join(j.Sources, "<br>"))
	p("| Destination | %s |\n", j.Target)
	p("| Layout | %s |\n", describeLayout(j))
	p("| Filters | %s |\n", describeFilters(j))
	if len(j.Duplicates) > 0 {
		p("| Duplicates | %s |\n", strings.Join(j.Duplicates, ", "))
	}
	if extras := describeExtras(j); extras != "" {
		p("| Options | %s |\n", extras)
	}

	counts := map[string]int{}
	for _, rw := range rows {
		counts[rw.status]++
	}
	p("\n## Result\n\n")
	for _, st := range []string{"copied", "cloned", "already there", "expanded", "skipped", "merged", "failed", "not reached"} {
		if counts[st] > 0 {
			p("- %s: %d\n", st, counts[st])
		}
	}
	p("- bytes written: %d\n", res.Bytes)
	if r.sum.Batches > 0 {
		p("- batches: %d of at most %d files\n", r.sum.Batches, j.Batch)
	}

	section := func(title string, keep func(row) bool, line func(row) string) {
		var picked []row
		for _, rw := range rows {
			if keep(rw) {
				picked = append(picked, rw)
			}
		}
		if len(picked) == 0 {
			return
		}
		sort.SliceStable(picked, func(a, c int) bool { return picked[a].rel < picked[c].rel })
		p("\n## %s (%d)\n\n", title, len(picked))
		for i, rw := range picked {
			if i == listCap {
				p("- … and %d more; see report.csv\n", len(picked)-listCap)
				break
			}
			p("- %s\n", line(rw))
		}
	}
	section("Failed", func(rw row) bool { return rw.status == "failed" },
		func(rw row) string { return fmt.Sprintf("`%s` — %s", rw.rel, rw.note) })
	section("Skipped duplicates", func(rw row) bool { return rw.status == "skipped" },
		func(rw row) string { return fmt.Sprintf("`%s` — %s", rw.rel, rw.note) })
	section("Merged", func(rw row) bool { return rw.status == "merged" },
		func(rw row) string { return fmt.Sprintf("`%s` → `%s`", rw.rel, rw.dst) })
	section("Renamed", func(rw row) bool { return strings.HasPrefix(rw.note, "renamed") || strings.HasPrefix(rw.note, "type") },
		func(rw row) string { return fmt.Sprintf("`%s` → `%s` (%s)", rw.rel, filepath.Base(rw.dst), rw.note) })
	if j.Batch > 0 || j.MergeText {
		section("Over NotebookLM's 200 MB per source", func(rw row) bool {
			return rw.bytes > notebookLMBytes && (rw.status == "copied" || rw.status == "cloned" || rw.status == "already there")
		}, func(rw row) string { return fmt.Sprintf("`%s` — %d MB", rw.dst, rw.bytes/1_000_000) })
	}
	return b.String()
}

func describeLayout(j job.Job) string {
	switch j.LayoutOrFlat() {
	case job.ByType:
		if j.KeepFolders {
			return "by type, keeping the original folders"
		}
		return "by type"
	case job.ByDate:
		return "by date taken (year/month)"
	case job.ByDepth:
		return fmt.Sprintf("keep the top %d folder level(s), flatten below", j.Depth)
	}
	return "flat"
}

func describeFilters(j job.Job) string {
	var parts []string
	if len(j.Excluded) > 0 {
		parts = append(parts, fmt.Sprintf("%d folder(s) excluded", len(j.Excluded)))
	}
	if len(j.Skip) > 0 {
		parts = append(parts, "skip "+strings.Join(j.Skip, ", "))
	}
	if len(j.Only) > 0 {
		parts = append(parts, "only "+strings.Join(j.Only, ", "))
	}
	if len(j.Not) > 0 {
		parts = append(parts, "not "+strings.Join(j.Not, ", "))
	}
	if j.MinSize > 0 {
		parts = append(parts, fmt.Sprintf("at least %d bytes", j.MinSize))
	}
	if j.MaxSize > 0 {
		parts = append(parts, fmt.Sprintf("at most %d bytes", j.MaxSize))
	}
	if j.Since != "" {
		parts = append(parts, "modified from "+j.Since)
	}
	if j.Until != "" {
		parts = append(parts, "modified until "+j.Until)
	}
	if len(parts) == 0 {
		return "none: every file"
	}
	return strings.Join(parts, "; ")
}

func describeExtras(j job.Job) string {
	var parts []string
	add := func(on bool, s string) {
		if on {
			parts = append(parts, s)
		}
	}
	add(j.RenameTitles, "rename to titles")
	add(j.DetectTypes, "detect types by content")
	add(j.Expand, "expand archives")
	add(j.MergeText, "merge text")
	add(j.Batch > 0, fmt.Sprintf("batches of %d", j.Batch))
	add(j.Verify, "verify copies")
	add(j.Retries > 0, fmt.Sprintf("retry failures %d time(s)", j.Retries))
	add(j.Notify, "notify")
	return strings.Join(parts, ", ")
}
