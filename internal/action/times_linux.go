//go:build linux

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

	//nolint:unconvert // the Timespec fields are int32 on 32-bit Linux and int64 elsewhere
	atime := time.Unix(int64(stat.Atim.Sec), int64(stat.Atim.Nsec))
	//nolint:unconvert // the Timespec fields are int32 on 32-bit Linux and int64 elsewhere
	mtime = time.Unix(int64(stat.Mtim.Sec), int64(stat.Mtim.Nsec))

	return atime, mtime
}
