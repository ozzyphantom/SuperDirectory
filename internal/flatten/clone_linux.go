package flatten

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile makes dst a reflink of src on filesystems that share extents (Btrfs,
// XFS): instant, sharing src's blocks until either changes. It fails elsewhere and
// across filesystems, and the caller then copies normally.
func cloneFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
