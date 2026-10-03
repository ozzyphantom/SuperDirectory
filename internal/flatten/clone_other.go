//go:build !darwin && !linux

package flatten

import "errors"

// cloneFile is unavailable here; the caller copies normally.
func cloneFile(src, dst string) error { return errors.New("clones are not supported here") }
