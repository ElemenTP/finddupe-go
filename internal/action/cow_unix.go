//go:build unix

package action

import "errors"

// clonePlatformFile attempts a CoW clone on Unix.
// Real implementations:
//
//	Linux (btrfs/xfs): ioctl FICLONERANGE via unix.IoctlFileCloneRange
//	macOS (APFS): clonefile(2) via unix.Clonefile
//
// For now, return ErrCoWNotSupported until the cross-platform phase.
func clonePlatformFile(src, dst string) error {
	// TODO: implement via golang.org/x/sys/unix or raw syscalls.
	return errors.Join(ErrCoWNotSupported,
		errors.New("CoW clone requires platform-specific implementation"),
	)
}
