//go:build unix

package checksum

import (
	"os"
	"syscall"
)

// fileInode extracts the inode number and hardlink count from an open file.
// On Unix, this is available via f.Stat() → Sys().(*syscall.Stat_t).
func fileInode(f *os.File) (inode uint64, numLinks uint64) {
	info, err := f.Stat()
	if err != nil {
		return 0, 0
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return stat.Ino, uint64(stat.Nlink)
}
