//go:build darwin

package action

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// preservePlatformFlags restores the BSD file flags (immutable, hidden,
// append-only, ...) that have no Linux equivalent. Flags are applied last
// because an immutable file refuses further metadata changes.
func preservePlatformFlags(dst, src string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}

	if flagErr := unix.Chflags(dst, int(stat.Flags)); flagErr != nil &&
		!errors.Is(flagErr, os.ErrPermission) {
		return flagErr
	}
	return nil
}
