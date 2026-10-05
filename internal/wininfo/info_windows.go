//go:build windows

package wininfo

import (
	"golang.org/x/sys/windows"
)

// Info is the part of ByHandleFileInformation the tools need.
type Info struct {
	// VolumeSerialNumber identifies the volume the file lives on.
	VolumeSerialNumber uint64

	// FileIndex is the NTFS file index, unique per volume.
	FileIndex uint64

	// NumLinks is the number of hard links to the file.
	NumLinks uint64

	// CreationTime, LastAccessTime and LastWriteTime are the file's times.
	CreationTime   windows.Filetime
	LastAccessTime windows.Filetime
	LastWriteTime  windows.Filetime
}

// FromHandle reads the file information of an open handle. The identity fields
// are what GetFileInformationByHandle is called for: os.FileInfo does not expose
// the volume serial number or the file index.
func FromHandle(handle windows.Handle) (Info, error) {
	var raw windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &raw); err != nil {
		return Info{}, err
	}

	return Info{
		VolumeSerialNumber: uint64(raw.VolumeSerialNumber),
		FileIndex:          uint64(raw.FileIndexHigh)<<32 | uint64(raw.FileIndexLow),
		NumLinks:           uint64(raw.NumberOfLinks),
		CreationTime:       raw.CreationTime,
		LastAccessTime:     raw.LastAccessTime,
		LastWriteTime:      raw.LastWriteTime,
	}, nil
}
