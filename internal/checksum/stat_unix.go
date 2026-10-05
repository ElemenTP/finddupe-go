//go:build unix

package checksum

import (
	"os"
	"syscall"
)

// statFile reads the file's size, modification time and physical identity from
// the open handle with a single fstat: [os.FileInfo] already carries the Unix
// stat structure, so the identity does not need a second call.
func statFile(f *os.File) (fileStat, error) {
	info, err := f.Stat()
	if err != nil {
		return fileStat{}, err
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileStat{Size: info.Size(), ModTime: info.ModTime()}, nil
	}
	return fileStat{
		Size:     info.Size(),
		ModTime:  info.ModTime(),
		Dev:      uint64(st.Dev), //nolint:unconvert // field types differ across Unix platforms
		Inode:    st.Ino,
		NumLinks: uint64(st.Nlink), //nolint:unconvert // field types differ across Unix platforms
	}, nil
}
