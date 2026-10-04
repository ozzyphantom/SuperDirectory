// Package filter turns a job's rules about which files to copy into the two tests
// a walk applies: which folders to prune, and which files to keep.
package filter

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
	"github.com/ozzyphantom/SuperDirectory/internal/organize"
)

// Rules is a job's filters, compiled.
type Rules struct {
	skip     []string // lowercased name patterns
	only     set      // categories and extensions to keep; empty keeps all
	not      set      // categories and extensions to drop
	min, max int64
	since    time.Time
	until    time.Time
	table    *organize.Table
}

// set holds lowercased categories and extensions.
type set map[string]bool

func newSet(items []string) set {
	s := set{}
	for _, it := range items {
		it = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(it), "."))
		if it != "" {
			s[it] = true
		}
	}
	return s
}

// From compiles a job's filters against a category table (nil means built-in).
func From(j *job.Job, table *organize.Table) Rules {
	if table == nil {
		table = organize.DefaultTable()
	}
	r := Rules{
		only: newSet(j.Only), not: newSet(j.Not),
		min: j.MinSize, max: j.MaxSize,
		since: j.SinceTime(), until: j.UntilTime(),
		table: table,
	}
	for _, p := range j.Skip {
		if p = strings.TrimSpace(p); p != "" {
			r.skip = append(r.skip, strings.ToLower(p))
		}
	}
	return r
}

// Active reports whether any rule is set, so a summary can say "everything".
func (r Rules) Active() bool {
	return len(r.skip) > 0 || len(r.only) > 0 || len(r.not) > 0 || r.min > 0 || r.max > 0 ||
		!r.since.IsZero() || !r.until.IsZero()
}

// Prune reports whether a folder is skipped with everything beneath it: when its
// name matches a skip pattern ("node_modules", ".git", "_old*").
func (r Rules) Prune(name string) bool { return r.skipped(name) }

// Keep reports whether a file is copied. Patterns match names case-insensitively,
// as macOS and Windows treat names; types are judged by the file name's extension,
// before any content detection.
func (r Rules) Keep(f flatten.File) bool {
	name := filepath.Base(f.Path)
	if r.skipped(name) {
		return false
	}
	if r.min > 0 && f.Size < r.min {
		return false
	}
	if r.max > 0 && f.Size > r.max {
		return false
	}
	if !r.since.IsZero() && f.ModTime.Before(r.since) {
		return false
	}
	if !r.until.IsZero() && f.ModTime.After(r.until) {
		return false
	}
	if len(r.only) > 0 || len(r.not) > 0 {
		ext := organize.Extension(name)
		cat := strings.ToLower(r.table.Category(ext))
		hit := func(s set) bool { return s[cat] || (ext != "" && s[ext]) || s[r.table.Folder(ext)] }
		if len(r.only) > 0 && !hit(r.only) {
			return false
		}
		if hit(r.not) {
			return false
		}
	}
	return true
}

func (r Rules) skipped(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range r.skip {
		if ok, _ := filepath.Match(p, lower); ok {
			return true
		}
	}
	return false
}
