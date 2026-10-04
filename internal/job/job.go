// Package job is everything one run of SuperDirectory needs to know: where from,
// where to, and how.
//
// The wizard fills one in, command-line flags fill one in, a preset stores one, and
// the report saved inside every superdirectory records one. That last copy is what
// lets an interrupted run resume with exactly the settings it started with.
package job

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Layout is how the superdirectory is arranged.
type Layout string

const (
	// Flat pools every file in one folder.
	Flat Layout = "flat"
	// ByType sorts files into Category/extension folders.
	ByType Layout = "type"
	// ByDate sorts files into year/month folders by when they were taken.
	ByDate Layout = "date"
	// ByDepth keeps the top Depth levels of folders and flattens everything below.
	ByDepth Layout = "depth"
)

// Kinds of duplicate the scans can look for.
const (
	Identical = "identical" // the same bytes, whatever the name
	Pictures  = "pictures"  // smaller copies of the same picture
	Documents = "documents" // mostly the same text
)

// Job is one run's settings. The zero value of every field is its default, so a
// preset or a report written by an older version still loads.
type Job struct {
	Sources  []string `json:"sources"`
	Target   string   `json:"target,omitempty"`
	Excluded []string `json:"excluded,omitempty"` // absolute folders under the sources

	Layout      Layout `json:"layout,omitempty"`      // "" means Flat
	Depth       int    `json:"depth,omitempty"`       // ByDepth only: folder levels kept
	KeepFolders bool   `json:"keepFolders,omitempty"` // ByType only: original folders inside each type folder

	Skip    []string `json:"skip,omitempty"`    // name patterns, files and folders
	Only    []string `json:"only,omitempty"`    // categories or extensions to keep
	Not     []string `json:"not,omitempty"`     // categories or extensions to drop
	MinSize int64    `json:"minSize,omitempty"` // bytes
	MaxSize int64    `json:"maxSize,omitempty"` // bytes
	Since   string   `json:"since,omitempty"`   // YYYY-MM-DD, modified on or after
	Until   string   `json:"until,omitempty"`   // YYYY-MM-DD, modified on or before

	Duplicates []string `json:"duplicates,omitempty"` // Identical, Pictures, Documents
	Review     bool     `json:"review,omitempty"`     // review duplicate sets before skipping

	RenameTitles bool `json:"renameTitles,omitempty"`
	DetectTypes  bool `json:"detectTypes,omitempty"`
	Expand       bool `json:"expand,omitempty"`
	MergeText    bool `json:"mergeText,omitempty"`
	Batch        int  `json:"batch,omitempty"` // files per batch folder; 0 means no batches

	Verify   bool `json:"verify,omitempty"`
	Retries  int  `json:"retries,omitempty"`
	NoReport bool `json:"noReport,omitempty"`
	Notify   bool `json:"notify,omitempty"`
}

// LayoutOrFlat is the job's layout, with the empty default spelled out.
func (j *Job) LayoutOrFlat() Layout {
	if j.Layout == "" {
		return Flat
	}
	return j.Layout
}

// Finds reports whether the job looks for a kind of duplicate.
func (j *Job) Finds(kind string) bool {
	for _, k := range j.Duplicates {
		if k == kind {
			return true
		}
	}
	return false
}

// SinceTime and UntilTime are the date filters as instants: the start of Since's
// day, and the end of Until's, in local time. Zero when unset.
func (j *Job) SinceTime() time.Time {
	t, _ := parseDay(j.Since)
	return t
}

func (j *Job) UntilTime() time.Time {
	t, err := parseDay(j.Until)
	if err != nil || t.IsZero() {
		return time.Time{}
	}
	return t.AddDate(0, 0, 1).Add(-time.Nanosecond)
}

func parseDay(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02", s, time.Local)
}

// Validate reports the first thing wrong with the job, in words a user can act on.
// It checks the sources on disk, but not the target, which may not exist yet.
func (j *Job) Validate() error {
	if len(j.Sources) == 0 {
		return errors.New("no source folder")
	}
	for _, s := range j.Sources {
		if !filepath.IsAbs(s) {
			return fmt.Errorf("source %q is not an absolute path", s)
		}
		info, err := os.Stat(s)
		if err != nil {
			return fmt.Errorf("source %s: %w", s, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("source %s is not a folder", s)
		}
	}
	if j.Target == "" {
		return errors.New("no destination folder")
	}
	if !filepath.IsAbs(j.Target) {
		return fmt.Errorf("destination %q is not an absolute path", j.Target)
	}
	for _, s := range j.Sources {
		if Overlaps(j.Target, s) {
			return fmt.Errorf("the destination %s overlaps the source %s; a copy would eat itself", j.Target, s)
		}
	}
	switch j.LayoutOrFlat() {
	case Flat, ByType, ByDate:
	case ByDepth:
		if j.Depth < 1 {
			return errors.New("the depth layout needs a depth of 1 or more")
		}
	default:
		return fmt.Errorf("unknown layout %q: use flat, type, date or depth", j.Layout)
	}
	if j.MinSize < 0 || j.MaxSize < 0 {
		return errors.New("sizes cannot be negative")
	}
	if j.MaxSize > 0 && j.MinSize > j.MaxSize {
		return errors.New("the smallest size is larger than the largest")
	}
	since, err := parseDay(j.Since)
	if err != nil {
		return fmt.Errorf("since: %q is not a date like 2024-03-31", j.Since)
	}
	until, err := parseDay(j.Until)
	if err != nil {
		return fmt.Errorf("until: %q is not a date like 2024-03-31", j.Until)
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return errors.New("the until date is before the since date")
	}
	for _, d := range j.Duplicates {
		if d != Identical && d != Pictures && d != Documents {
			return fmt.Errorf("unknown kind of duplicate %q: use identical, pictures or documents", d)
		}
	}
	if j.Batch < 0 {
		return errors.New("the batch size cannot be negative")
	}
	if j.Retries < 0 || j.Retries > 10 {
		return errors.New("retries must be between 0 and 10")
	}
	for _, p := range j.Skip {
		if _, err := filepath.Match(p, ""); err != nil {
			return fmt.Errorf("skip pattern %q: %w", p, err)
		}
	}
	return nil
}

// ValidateDates checks only the date fields, for a form that asks for them alone.
func (j *Job) ValidateDates() error {
	if _, err := parseDay(j.Since); err != nil {
		return fmt.Errorf("%q is not a date like 2024-03-31", j.Since)
	}
	if _, err := parseDay(j.Until); err != nil {
		return fmt.Errorf("%q is not a date like 2024-03-31", j.Until)
	}
	return nil
}

// Overlaps reports whether a and b are the same folder or one contains the other.
// The comparison ignores case: on macOS and Windows two paths that differ only in
// case name one folder, and treating them as distinct would let a copy write into
// its own source. On a case-sensitive volume this can only over-block.
func Overlaps(a, b string) bool {
	return within(a, b) || within(b, a)
}

func within(child, parent string) bool {
	child = strings.ToLower(filepath.Clean(child))
	parent = strings.ToLower(filepath.Clean(parent))
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ParseSize reads a size such as "10KB", "1.5 GB", "200MiB" or "4096". Units are
// decimal (KB = 1000) to match how drives and the progress line count; the binary
// spellings (KiB = 1024) are accepted too.
func ParseSize(s string) (int64, error) {
	t := strings.TrimSpace(strings.ToUpper(s))
	if t == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10},
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3},
		{"T", 1e12}, {"G", 1e9}, {"M", 1e6}, {"K", 1e3}, {"B", 1},
	}
	mult := 1.0
	for _, u := range units {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mult
			break
		}
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v < 0 || v*mult > 1<<62 {
		return 0, fmt.Errorf("%q is not a size like 10KB or 200MB", s)
	}
	return int64(v * mult), nil
}
