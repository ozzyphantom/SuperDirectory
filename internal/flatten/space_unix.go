//go:build unix

package flatten

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// FreeSpace reports the bytes an unprivileged user can still write on the volume
// holding path, or its nearest existing parent: the target may not exist yet.
func FreeSpace(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(existingParent(path), &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// SameVolume reports whether two paths live on one volume, where a clone is
// possible and a copy costs no extra space when it is one.
func SameVolume(a, b string) bool {
	da, okA := device(existingParent(a))
	db, okB := device(existingParent(b))
	return okA && okB && da == db
}

func device(path string) (uint64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true // Dev is int32 on darwin and uint64 on linux
}

// existingParent climbs from path to the nearest folder that exists.
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
