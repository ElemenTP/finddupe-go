//go:build unix && !linux && !darwin

package action

import (
	"os"
	"time"
)

// fileTimes returns the access and modification times of info. Platforms
// without a mapping here fall back to the modification time for both.
func fileTimes(info os.FileInfo) (time.Time, time.Time) {
	mtime := info.ModTime()
	return mtime, mtime
}
