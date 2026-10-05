//go:build !unix

package fileid

import "io/fs"

// FromFileInfo returns zero on platforms whose os.FileInfo does not expose the
// volume serial number and file index: Windows keeps them in a handle-only
// structure, so the scanner reads them from the open handle instead, and the
// walker works without identity (it must not open files: that would serialize
// the scan).
func FromFileInfo(fs.FileInfo) (dev, inode, numLinks uint64, ok bool) {
	return 0, 0, 0, false
}
