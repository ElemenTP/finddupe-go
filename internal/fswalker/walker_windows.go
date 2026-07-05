//go:build windows

package fswalker

import (
	"io/fs"
)

// getInode returns zero on Windows — inode/link retrieval happens later
// in the parallel checksum computation path (checksum.ComputeFileInfo),
// where the file handle is already open. Opening files here in the
// single-threaded walker would serialize I/O and kill parallelism.
func getInode(_ string, _ fs.FileInfo) (inode uint64, numLinks uint64) {
	return 0, 0
}
