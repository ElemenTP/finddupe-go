//go:build darwin

package action

import (
	"errors"

	"golang.org/x/sys/unix"
)

// clonePlatformFile creates a CoW clone of src at dst. dst must not exist.
// macOS supports this on APFS via clonefile(2).
func clonePlatformFile(src, dst string) error {
	if err := unix.Clonefile(src, dst, 0); err != nil {
		if errors.Is(err, unix.EXDEV) {
			return errors.Join(ErrCrossDevice, err)
		}
		if isCloneUnsupportedErr(err) {
			return errors.Join(ErrCoWNotSupported, err)
		}
		return err
	}
	return nil
}

// isCloneUnsupportedErr reports whether the clone failed because the filesystem
// or platform does not support cloning.
func isCloneUnsupportedErr(err error) bool {
	return errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.ENOSYS)
}
