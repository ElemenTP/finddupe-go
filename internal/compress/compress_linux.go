//go:build linux

package compress

import (
	"finddupe/internal/dupe"
	"finddupe/internal/extent"
)

// IsCompressed reports whether the file's extents are stored encoded
// (compressed). FIEMAP reports btrfs/zfs compression as an encoded extent; the
// same flag also covers extents the filesystem marks unknown or delayed, which
// is acceptable for a keeper preference.
func IsCompressed(fi dupe.FileInfo) bool {
	extents, err := extent.Query(fi.Path, fi.Size)
	if err != nil {
		return false
	}
	for _, e := range extents {
		if e.Encoded {
			return true
		}
	}
	return false
}
