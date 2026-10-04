//go:build windows

package fswalker

import (
	"io/fs"
)

// getFileIdentity returns zero on Windows — identity retrieval happens later
// in the parallel checksum computation path (checksum.ComputeFileInfo),
// where the file handle is already open. Opening files here in the
// single-threaded walker would serialize I/O and kill parallelism.
func getFileIdentity(_ string, _ fs.FileInfo) (dev uint64, inode uint64, numLinks uint64) {
	return 0, 0, 0
}
