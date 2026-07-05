//go:build windows

package action

import "errors"

// clonePlatformFile attempts a CoW clone on Windows.
// Real implementation: FSCTL_DUPLICATE_EXTENTS_TO_FILE via DeviceIoControl.
// Only works on ReFS volumes.
// For now, return ErrCoWNotSupported until the cross-platform phase.
func clonePlatformFile(src, dst string) error {
	// TODO: implement via syscall or golang.org/x/sys/windows.
	return errors.Join(ErrCoWNotSupported,
		errors.New("CoW clone requires platform-specific implementation"),
	)
}
