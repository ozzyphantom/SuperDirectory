package engine

import (
	"github.com/ozzyphantom/SuperDirectory/internal/filter"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/guard"
)

// The stages below are filled in as their format packages land: type detection,
// titles, archive expansion, text merging, and near-duplicate documents.

func (r *run) detectType(g guard.Reader, f *flatten.File) error { return nil }

func (r *run) readTitle(g guard.Reader, f *flatten.File) error { return nil }

func (r *run) expand(files []flatten.File, rules filter.Rules) ([]flatten.File, error) {
	return files, nil
}

func (r *run) mergeText(items []flatten.Item) ([]flatten.Item, error) { return items, nil }

func (r *run) documentDuplicates(items []flatten.Item, sets []DupSet) ([]DupSet, error) {
	return nil, nil
}

// cleanup removes what the run staged inside the target and no longer needs.
func (r *run) cleanup() {}
