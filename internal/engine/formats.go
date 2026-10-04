package engine

import (
	"io"

	"github.com/ozzyphantom/SuperDirectory/internal/chm"
	"github.com/ozzyphantom/SuperDirectory/internal/expand"
)

// The format readers live in packages of their own, and the packages that use
// them take them through hooks, so no reader depends on another. They meet here,
// once, for every front end.
func init() {
	expand.CHM = func(r io.ReaderAt, size int64) (expand.Archive, error) {
		f, err := chm.Open(r, size)
		if err != nil {
			return nil, err
		}
		return chmArchive{f}, nil
	}
}

// chmArchive is a compiled help file as package expand lists and reads it.
type chmArchive struct{ f *chm.File }

func (a chmArchive) Names() []string {
	entries := a.f.Entries()
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}

func (a chmArchive) Read(name string) ([]byte, error) { return a.f.ReadFile(name) }
