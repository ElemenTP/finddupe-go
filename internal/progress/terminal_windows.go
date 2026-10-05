//go:build windows

package progress

import "golang.org/x/sys/windows"

// isTerminal reports whether fd is a console handle. GetConsoleMode fails for
// pipes, files and the null device.
func isTerminal(fd uintptr) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(fd), &mode) == nil
}
