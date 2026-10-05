//go:build windows

package action

import (
	"os"

	"golang.org/x/sys/windows"

	"finddupe/internal/dupe"
	"finddupe/internal/wininfo"
)

// sameIdentity reports whether the file at path is still the one whose content
// was hashed: the same volume serial number and file index as the record.
// Windows does not expose either through os.FileInfo, so the file is opened
// once; the handle is closed before the caller acts on the path. The second
// result is false when the identity cannot be read (an unreadable file), in
// which case the caller falls back to size and modification time.
func sameIdentity(path string, fi dupe.FileInfo, _ os.FileInfo) (bool, bool) {
	file, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer file.Close()

	info, err := wininfo.FromHandle(windows.Handle(file.Fd()))
	if err != nil {
		return false, false
	}
	return info.VolumeSerialNumber == fi.Dev && info.FileIndex == fi.Inode, true
}
