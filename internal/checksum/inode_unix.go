//go:build unix

package checksum

import (
	"os"
	"syscall"
)

// fileIdentity extracts the device id, inode number, and hardlink count from
// an open file. On Unix this is available via f.Stat() → Sys().(*syscall.Stat_t).
func fileIdentity(f *os.File) (uint64, uint64, uint64) {
	info, err := f.Stat()
	if err != nil {
		return 0, 0, 0
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0
	}
	return uint64(stat.Dev), stat.Ino, uint64(stat.Nlink) //nolint:unconvert // field types differ across Unix platforms
}
