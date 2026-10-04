package engine

import (
	"errors"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/exif"
	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/guard"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
)

// inspect reads what the plan needs from inside the files: when photos and videos
// were taken (for the date layout), what type each file really is, and what each
// document calls itself. Each file is read through the guard, one at a time, so a
// drive that suits sequential reads gets them and a file that never reads costs
// one stall timeout, not the run.
func (r *run) inspect(files []flatten.File) error {
	dates := r.j.LayoutOrFlat() == job.ByDate
	types := r.j.DetectTypes
	titles := r.j.RenameTitles
	if !dates && !types && !titles {
		return nil
	}
	r.h.Stage(Inspecting)
	g := guard.Reader{Stall: flatten.DefaultStallTimeout, Cancel: r.stop}
	for i := range files {
		f := &files[i]
		if i%32 == 0 {
			r.h.Progress(Inspecting, i, len(files), f.BaseName())
		}
		if types {
			if err := r.detectType(g, f); errors.Is(err, guard.ErrCanceled) {
				return ErrStopped
			}
		}
		if titles {
			if err := r.readTitle(g, f); errors.Is(err, guard.ErrCanceled) {
				return ErrStopped
			}
		}
		if dates && exif.Supported(f.BaseName()) {
			when, err := guard.Read(g, f.Path, func(fh exif.File) (time.Time, error) {
				t, _ := exif.Taken(f.BaseName(), fh, f.Size)
				return t, nil
			})
			if errors.Is(err, guard.ErrCanceled) {
				return ErrStopped
			}
			f.Taken = when
		}
	}
	r.h.Progress(Inspecting, len(files), len(files), "")
	return nil
}
