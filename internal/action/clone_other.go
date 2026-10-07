//go:build !linux && !darwin && !windows

package action

// clonePlatformFile reports that CoW cloning is unavailable on this platform.
func clonePlatformFile(_, _ string) error {
	return ErrCoWNotSupported
}
