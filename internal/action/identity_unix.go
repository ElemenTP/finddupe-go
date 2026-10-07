//go:build unix

package action

import (
	"os"

	"finddupe/internal/dupe"
	"finddupe/internal/fileid"
)

// sameIdentity reports whether the file that was just stat'ed is still the one
// whose content was hashed: the same device and inode as the record. The
// second result is false when the platform cannot answer from the stat result.
func sameIdentity(_ string, fi dupe.FileInfo, info os.FileInfo) (bool, bool) {
	dev, inode, _, isStat := fileid.FromFileInfo(info)
	if !isStat {
		return false, false
	}
	return dev == fi.Dev && inode == fi.Inode, true
}
