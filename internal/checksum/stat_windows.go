//go:build windows

package checksum

import (
	"os"
	"syscall"
)

// statFile reads the file's size, modification time and physical identity. Size
// and modification time come from os.FileInfo; Windows keeps the volume serial
// number and NTFS file index in ByHandleFileInformation, which os.FileInfo does
// not expose, so the identity still needs one call on the handle.
func statFile(f *os.File) (fileStat, error) {
	info, err := f.Stat()
	if err != nil {
		return fileStat{}, err
	}

	// os.FileInfo does not expose the volume serial number and NTFS file index,
	// so those still come from the handle directly.
	var handle syscall.ByHandleFileInformation
	if hErr := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &handle); hErr != nil {
		return fileStat{Size: info.Size(), ModTime: info.ModTime()}, nil
	}

	return fileStat{
		Size:     info.Size(),
		ModTime:  info.ModTime(),
		Dev:      uint64(handle.VolumeSerialNumber),
		Inode:    uint64(handle.FileIndexHigh)<<32 | uint64(handle.FileIndexLow),
		NumLinks: uint64(handle.NumberOfLinks),
	}, nil
}
