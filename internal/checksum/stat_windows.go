//go:build windows

package checksum

import (
	"os"

	"golang.org/x/sys/windows"

	"finddupe/internal/wininfo"
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

	out := fileStat{Size: info.Size(), ModTime: info.ModTime()}
	winInfo, infoErr := wininfo.FromHandle(windows.Handle(f.Fd()))
	if infoErr != nil {
		// Without the identity the file is still scanned (checksum, size, mtime)
		// but it cannot be grouped by --listlink or recognized as one half of an
		// existing hardlink. The error travels with the result so the pipeline can
		// say so in verbose mode instead of degrading invisibly.
		out.identityErr = infoErr
		return out, nil
	}
	out.Dev = winInfo.VolumeSerialNumber
	out.Inode = winInfo.FileIndex
	out.NumLinks = winInfo.NumLinks
	return out, nil
}
