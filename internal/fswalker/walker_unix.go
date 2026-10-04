//go:build unix

package fswalker

import (
	"io/fs"
	"syscall"
)

// getFileIdentity extracts the device id, inode number, and hardlink count from
// a FileInfo. The path parameter is unused on Unix (available via stat).
func getFileIdentity(_ string, info fs.FileInfo) (uint64, uint64, uint64) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0
	}
	return uint64(stat.Dev), stat.Ino, uint64(stat.Nlink) //nolint:unconvert // field types differ across Unix platforms
}
