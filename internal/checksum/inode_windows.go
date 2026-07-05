//go:build windows

package checksum

import (
	"os"
	"syscall"
)

// fileInode extracts the NTFS file index and hardlink count from an open file.
// Uses GetFileInformationByHandle on the Windows file handle, matching the
// original C finddupe behavior. Called from the parallel scanner goroutine,
// not the single-threaded walker.
func fileInode(f *os.File) (inode uint64, numLinks uint64) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info); err != nil {
		return 0, 0
	}
	inode = (uint64(info.FileIndexHigh) << 32) | uint64(info.FileIndexLow)
	numLinks = uint64(info.NumberOfLinks)
	return inode, numLinks
}
