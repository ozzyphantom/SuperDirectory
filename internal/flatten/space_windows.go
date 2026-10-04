//go:build windows

package flatten

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// FreeSpace reports the bytes the current user can still write on the volume
// holding path, or its nearest existing parent: the target may not exist yet.
func FreeSpace(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(existingParent(path))
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return int64(avail), nil
}

// SameVolume reports whether two paths live on one volume.
func SameVolume(a, b string) bool {
	va, vb := filepath.VolumeName(a), filepath.VolumeName(b)
	return va != "" && strings.EqualFold(va, vb)
}

func existingParent(path string) string {
	p := filepath.Clean(path)
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// diskFull reports whether err says the volume is full.
func diskFull(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}
