//go:build darwin

package action

import (
	"bytes"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Data-layout extended attributes on macOS: a decmpfs-compressed file keeps its
// header in com.apple.decmpfs and its payload in com.apple.ResourceFork. They
// describe where the bytes are, not how the user sees the file, so a CoW clone
// must keep the ones it inherited from the cloned data.
const (
	xattrDecmpfs      = "com.apple.decmpfs"
	xattrResourceFork = "com.apple.ResourceFork"
)

// dataLayoutXattr reports whether an extended attribute carries the file's data
// layout rather than user-visible metadata.
func dataLayoutXattr(name string) bool {
	return name == xattrDecmpfs || name == xattrResourceFork
}

// preserveDataLayout makes the clone's storage state match the keeper's, because
// the clone holds the keeper's bytes: clonefile(2) is expected to carry the
// compressed payload, its header and the compression flag across, but the
// victim-metadata pass that follows can only ever clear them (the victim is
// usually not compressed), and a clone whose payload went missing is a
// zero-length file. When they are absent they are copied from the keeper, in the
// order the filesystem expects them: payload, then header, then flag.
func preserveDataLayout(dst, keeper string) error {
	if xattrSize(keeper, xattrDecmpfs) <= 0 {
		// The keeper is not stored compressed: nothing describes its bytes beyond
		// the data fork the clone already has.
		return nil
	}

	keeperInfo, err := os.Stat(keeper)
	if err != nil {
		return err
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		return err
	}

	// A decmpfs file reports its uncompressed size, so a clone that carries the
	// compressed data always reports the keeper's size.
	if dstInfo.Size() != keeperInfo.Size() || !sameXattr(dst, keeper, xattrDecmpfs) {
		if copyErr := copyXattrErr(dst, keeper, xattrResourceFork); copyErr != nil {
			return fmt.Errorf("copy %s of %s onto the clone: %w", xattrResourceFork, keeper, copyErr)
		}
		if copyErr := copyXattrErr(dst, keeper, xattrDecmpfs); copyErr != nil {
			return fmt.Errorf("copy %s of %s onto the clone: %w", xattrDecmpfs, keeper, copyErr)
		}
	}

	return setStorageFlags(dst, keeperInfo)
}

// xattrSize returns the size of one extended attribute, or 0 when it is absent.
func xattrSize(path, name string) int {
	size, err := unix.Getxattr(path, name, nil)
	if err != nil {
		return 0
	}
	return size
}

// sameXattr reports whether two files hold the same value for one attribute.
func sameXattr(a, b, name string) bool {
	aValue, aErr := readXattr(a, name)
	if aErr != nil {
		return false
	}
	bValue, bErr := readXattr(b, name)
	if bErr != nil {
		return false
	}
	return bytes.Equal(aValue, bValue)
}

// readXattr returns the value of one extended attribute.
func readXattr(path, name string) ([]byte, error) {
	size, err := unix.Getxattr(path, name, nil)
	if err != nil {
		return nil, err
	}
	if size <= 0 {
		return nil, nil
	}
	value := make([]byte, size)
	n, err := unix.Getxattr(path, name, value)
	if err != nil {
		return nil, err
	}
	return value[:n], nil
}
