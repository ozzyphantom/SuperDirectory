// Package extract defines the content-inspection seam.
//
// This is the polyglot boundary discussed in the language decision. The Go core
// reads what it needs from inside files in pure Go: a file's type from its
// content, a document's title and text, a photo's capture date. When deeper
// extraction is ever needed — OCR of scanned pages, say — a Python-backed
// extractor can be added behind this SAME interface by shelling out to a helper
// process, WITHOUT touching the Go core or the single-binary build. The
// interface is the contract; the implementation is swappable.
package extract

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/exif"
	"github.com/ozzyphantom/SuperDirectory/internal/organize"
	"github.com/ozzyphantom/SuperDirectory/internal/sniff"
	"github.com/ozzyphantom/SuperDirectory/internal/title"
)

// Metadata is the structured result of inspecting one file.
type Metadata struct {
	Path  string
	Size  int64
	Type  string    // the type the content shows, such as "pdf"; "" when unknown
	Fits  bool      // the name's extension agrees with Type
	Title string    // the document's own title; "" when it has none
	Taken time.Time // when a photo or video was taken; zero when unknown
}

// Extractor inspects a file and returns structured metadata. Every backend —
// pure Go today, a helper process tomorrow — implements this one method.
type Extractor interface {
	Extract(path string) (Metadata, error)
}

// MetadataExtractor is the pure-Go default, built on the readers the copy uses:
// package sniff for the type, title for the title, exif for the capture date.
type MetadataExtractor struct{}

func (MetadataExtractor) Extract(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Metadata{}, err
	}
	name := filepath.Base(path)
	m := Metadata{Path: path, Size: info.Size()}
	if m.Type, err = sniff.DetectFile(f, m.Size); err != nil {
		return m, err
	}
	m.Fits = sniff.Agrees(organize.Extension(name), m.Type)
	if title.Supported(name) {
		m.Title = title.Of(name, f, m.Size)
	}
	if exif.Supported(name) {
		m.Taken, _ = exif.Taken(name, f, m.Size)
	}
	return m, nil
}

// ErrExtractorUnavailable is returned by backends that are not wired up.
var ErrExtractorUnavailable = errors.New("extractor unavailable: no helper configured")

// PythonExtractor is the FUTURE seam — deliberately NOT wired up yet.
// It documents the exact shape the polyglot integration takes: launch an
// optional Python helper as a subprocess and decode JSON from its stdout.
// Because the coupling is process-level (not libpython/CGO), the Go binary
// stays a clean static single file and cross-compilation is unaffected.
type PythonExtractor struct {
	// Helper is the path to a Python helper (a bundled sidecar binary or an
	// installed script). Empty means the deep-extraction feature is simply
	// unavailable, and the app degrades gracefully to MetadataExtractor.
	Helper string
}

func (p PythonExtractor) Extract(path string) (Metadata, error) {
	if p.Helper == "" {
		return Metadata{}, ErrExtractorUnavailable
	}
	// Blueprint (intentionally not executed yet):
	//
	//   out, err := exec.Command(p.Helper, path).Output()
	//   if err != nil {
	//       return Metadata{}, err
	//   }
	//   var m Metadata
	//   if err := json.Unmarshal(out, &m); err != nil {
	//       return Metadata{}, err
	//   }
	//   return m, nil
	//
	return Metadata{}, ErrExtractorUnavailable
}

// Compile-time proof that both backends satisfy the interface. If a future
// edit breaks the contract, the build fails here — the kind of static check
// that motivated choosing Go.
var (
	_ Extractor = MetadataExtractor{}
	_ Extractor = PythonExtractor{}
)
