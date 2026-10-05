//go:build !linux && !darwin && !windows

package progress

import "os"

// isTerminal falls back to the character-device test on platforms without an
// implementation here. It cannot tell the null device from a terminal, so
// interactive mode should not be relied on there.
func isTerminal(fd uintptr) bool {
	file := os.NewFile(fd, "fd")
	if file == nil {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
