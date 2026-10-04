package flatten

import "golang.org/x/sys/unix"

// cloneFile makes dst an APFS clone of src: instant, sharing src's blocks until
// either changes. It fails on volumes without clones (HFS+, exFAT) and across
// volumes, and the caller then copies normally.
func cloneFile(src, dst string) error {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
}
