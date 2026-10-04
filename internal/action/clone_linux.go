//go:build linux

package action

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// clonePlatformFile creates a CoW clone of src at dst. dst must not exist.
// Linux supports this on btrfs and XFS (reflink) via the FICLONE ioctl.
func clonePlatformFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if cloneErr := unix.IoctlFileClone(int(dstFile.Fd()), int(srcFile.Fd())); cloneErr != nil {
		if isCloneUnsupportedErr(cloneErr) {
			return errors.Join(ErrCoWNotSupported, cloneErr)
		}
		return cloneErr
	}
	return nil
}

// isCloneUnsupportedErr reports whether the clone failed because the filesystem
// or platform does not support reflinks.
func isCloneUnsupportedErr(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOTTY) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.ENOSYS)
}
