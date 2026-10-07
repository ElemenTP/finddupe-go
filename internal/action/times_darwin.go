//go:build darwin

package action

import (
	"os"
	"syscall"
	"time"
)

// fileTimes returns the access and modification times of info.
func fileTimes(info os.FileInfo) (time.Time, time.Time) {
	mtime := info.ModTime()

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return mtime, mtime
	}

	atime := time.Unix(stat.Atimespec.Sec, stat.Atimespec.Nsec)
	mtime = time.Unix(stat.Mtimespec.Sec, stat.Mtimespec.Nsec)

	return atime, mtime
}
