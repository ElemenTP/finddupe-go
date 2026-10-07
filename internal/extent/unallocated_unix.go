//go:build unix

package extent

import (
	"os"
	"syscall"
)

// unallocatedFile reports whether the file has no storage allocated to it (a
// fully sparse file). It is consulted only when an extent query failed, to tell
// "this filesystem cannot report extents" apart from "this file has none to
// report": btrfs returns EOPNOTSUPP for a fully sparse file, which otherwise
// looks exactly like a filesystem without FIEMAP support.
func unallocatedFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Blocks == 0
}
