//go:build darwin

package action

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Data-layout extended attributes on macOS: a decmpfs-compressed file keeps its
// header in com.apple.decmpfs and — depending on the compression type — its
// payload either inside that same attribute or in com.apple.ResourceFork. They
// describe where the bytes are, not how the user sees the file, so a CoW clone
// must keep the ones it inherited from the cloned data.
const (
	xattrDecmpfs      = "com.apple.decmpfs"
	xattrResourceFork = "com.apple.ResourceFork"
)

// decmpfs header: "cmpf" then the compression type and the uncompressed size,
// both little-endian, followed by the payload when the payload is stored inline.
const (
	decmpfsHeaderSize = 16
	decmpfsMagic      = "cmpf"
)

// decmpfs compression types (XNU decmpfs.h). Types 1, 3 and 11 keep the payload
// in the com.apple.decmpfs attribute itself; 4, 7, 8 and 12 keep it in the
// resource fork.
const (
	decmpfsTypeUncompressed = 1
	decmpfsTypeZlibInline   = 3
	decmpfsTypeZlibFork     = 4
	decmpfsTypeLZVNFork     = 7
	decmpfsTypeLZVNForkAlt  = 8
	decmpfsTypeLZFSEInline  = 11
	decmpfsTypeLZFSEFork    = 12
)

// decmpfsNeedsResourceFork reports whether a decmpfs attribute describes a payload
// that lives in the file's resource fork. An unparseable or unknown header answers
// true: requiring the fork and failing the action is the safe direction, because a
// header claiming a payload that is not there must never reach a clone (a file
// whose payload went missing reads back as zeros).
func decmpfsNeedsResourceFork(header []byte) bool {
	if len(header) < 8 || string(header[:4]) != decmpfsMagic {
		return true
	}
	switch binary.LittleEndian.Uint32(header[4:8]) {
	case decmpfsTypeUncompressed, decmpfsTypeZlibInline, decmpfsTypeLZFSEInline:
		return false // the payload is in the attribute itself
	default:
		return true
	}
}

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
func preserveDataLayout(dst, keeper string, keeperInfo os.FileInfo) error {
	if xattrSize(keeper, xattrDecmpfs) <= 0 {
		// The keeper is not stored compressed: nothing describes its bytes beyond
		// the data fork the clone already has.
		return nil
	}

	dstInfo, err := os.Stat(dst)
	if err != nil {
		return err
	}

	// A decmpfs file reports its uncompressed size, so a clone that carries the
	// compressed data always reports the keeper's size.
	if dstInfo.Size() != keeperInfo.Size() || !sameXattr(dst, keeper, xattrDecmpfs) {
		keeperHeader, _ := readXattr(keeper, xattrDecmpfs)
		// The payload comes first for a resource-fork type, and it has to exist: a
		// header describing a payload that is not there (a damaged or truncated
		// keeper) fails the action here, before anything replaces the victim. A
		// payload stored inline has no resource fork by design — its bytes are in
		// the attribute copied below — so only the fork types require one.
		if decmpfsNeedsResourceFork(keeperHeader) {
			if copyErr := copyXattrErr(dst, keeper, xattrResourceFork); copyErr != nil {
				return fmt.Errorf("copy %s of %s onto the clone: %w", xattrResourceFork, keeper, copyErr)
			}
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
