//go:build windows

package action

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isCrossDeviceError reports whether err is the operating system refusing an
// operation because the two paths are on different volumes.
// ERROR_NOT_SAME_DEVICE is the Windows answer; syscall.EXDEV is a Go-invented
// errno that a Windows call never returns.
func isCrossDeviceError(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE)
}
