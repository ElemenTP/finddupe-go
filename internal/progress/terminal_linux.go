//go:build linux

package progress

import "golang.org/x/sys/unix"

// isTerminal reports whether fd is a terminal, using the termios ioctl: it fails
// with ENOTTY for anything that is not a tty, including /dev/null.
func isTerminal(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
	return err == nil
}
