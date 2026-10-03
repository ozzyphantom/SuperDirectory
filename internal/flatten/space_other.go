//go:build !unix && !windows

package flatten

import "errors"

// FreeSpace is unknown on this system.
func FreeSpace(path string) (int64, error) { return 0, errors.New("free space unknown here") }

// SameVolume is unknown on this system; assume not.
func SameVolume(a, b string) bool { return false }
