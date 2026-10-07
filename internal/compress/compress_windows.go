//go:build windows

package compress

import (
	"golang.org/x/sys/windows"

	"finddupe/internal/dupe"
)

// IsCompressed reports whether the file carries the NTFS compressed attribute.
func IsCompressed(fi dupe.FileInfo) bool {
	p, err := windows.UTF16PtrFromString(fi.Path)
	if err != nil {
		return false
	}
	attrs, attrErr := windows.GetFileAttributes(p)
	if attrErr != nil {
		return false
	}
	return attrs&windows.FILE_ATTRIBUTE_COMPRESSED != 0
}
