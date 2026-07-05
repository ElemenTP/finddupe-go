//go:build unix

package fswalker

import (
	"io/fs"
	"syscall"
)

// getInode extracts the inode number and hardlink count from a FileInfo.
// The path parameter is unused on Unix (available via stat).
func getInode(_ string, info fs.FileInfo) (inode uint64, numLinks uint64) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return stat.Ino, uint64(stat.Nlink)
}
