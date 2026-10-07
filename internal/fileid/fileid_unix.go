//go:build unix

// Package fileid extracts physical file identity from a stat result. Unix
// exposes it in the stat structure the walker and the scanner already have, so
// both can share one implementation instead of mapping the platform fields
// twice.
package fileid

import (
	"io/fs"
	"syscall"
)

// FromFileInfo returns the device id, inode number and hardlink count of a file
// whose stat is already available. ok is false when the platform structure is
// not the expected one.
func FromFileInfo(info fs.FileInfo) (uint64, uint64, uint64, bool) {
	stat, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, 0, false
	}
	//nolint:unconvert // the stat field types differ across Unix platforms
	return uint64(stat.Dev), stat.Ino, uint64(stat.Nlink), true
}
