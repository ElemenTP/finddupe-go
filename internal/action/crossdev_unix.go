//go:build !windows

package action

import (
	"errors"
	"syscall"
)

// isCrossDeviceError reports whether err is the operating system refusing an
// operation because the two paths are on different volumes (EXDEV).
func isCrossDeviceError(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}
