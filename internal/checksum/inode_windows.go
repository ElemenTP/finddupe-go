//go:build windows

package checksum

import (
	"os"
	"syscall"
)

// fileIdentity extracts the volume serial number, NTFS file index, and hardlink
// count from an open file. Uses GetFileInformationByHandle on the Windows file
// handle, matching the original C finddupe behavior. Called from the parallel
// scanner goroutine, not the single-threaded walker.
func fileIdentity(f *os.File) (dev uint64, inode uint64, numLinks uint64) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info); err != nil {
		return 0, 0, 0
	}
	dev = uint64(info.VolumeSerialNumber)
	inode = (uint64(info.FileIndexHigh) << 32) | uint64(info.FileIndexLow)
	numLinks = uint64(info.NumberOfLinks)
	return dev, inode, numLinks
}
