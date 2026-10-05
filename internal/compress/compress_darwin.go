//go:build darwin

package compress

import (
	"golang.org/x/sys/unix"

	"finddupe/internal/dupe"
)

// ufCompressed is UF_COMPRESSED from <sys/stat.h>: the file is stored in a
// compressed container (decmpfs) rather than as plain extents.
const ufCompressed = 0x20

// IsCompressed reports whether the file carries the APFS/HFS+ compressed flag.
func IsCompressed(fi dupe.FileInfo) bool {
	var st unix.Stat_t
	if err := unix.Stat(fi.Path, &st); err != nil {
		return false
	}
	return st.Flags&ufCompressed != 0
}
