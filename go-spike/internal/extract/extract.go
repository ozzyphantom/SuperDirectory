// Package extract defines the content-inspection seam.
//
// This is the polyglot boundary discussed in the language decision: the Go
// core ships a pure-Go, zero-dependency extractor today. When (if) deep
// document extraction is needed — full PDF text, DOCX bodies, OCR — a
// Python-backed extractor can be added behind this SAME interface by shelling
// out to a helper process, WITHOUT touching the Go core or the single-binary
// build. The interface is the contract; the implementation is swappable.
package extract

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Metadata is the structured result of inspecting one file.
type Metadata struct {
	Path     string
	Title    string
	MIMEType string
	Size     int64
}

// Extractor inspects a file and returns structured metadata. Every backend —
// pure Go today, Python tomorrow — implements this one method.
type Extractor interface {
	Extract(path string) (Metadata, error)
}

// MetadataExtractor is the pure-Go, zero-dependency default. It sniffs the
// MIME type from the file's leading bytes and derives a human title from the
// filename. It intentionally does NOT parse document bodies — that is the line
// where a PythonExtractor takes over.
type MetadataExtractor struct{}

func (MetadataExtractor) Extract(path string) (Metadata, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{
		Path:     path,
		Title:    titleFromName(info.Name()),
		MIMEType: sniff(path),
		Size:     info.Size(),
	}, nil
}

func titleFromName(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	base = strings.NewReplacer("_", " ", "-", " ").Replace(base)
	return strings.TrimSpace(base)
}

// sniff reads up to 512 bytes and classifies the content with the standard
// library's DetectContentType (the same sniffing algorithm browsers use).
func sniff(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return http.DetectContentType(buf[:n])
}

// ErrExtractorUnavailable is returned by backends that are not wired up.
var ErrExtractorUnavailable = errors.New("extractor unavailable: no helper configured")

// PythonExtractor is the FUTURE seam — deliberately NOT wired in this spike.
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
	// Blueprint (intentionally not executed in the spike):
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
