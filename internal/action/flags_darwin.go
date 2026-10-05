//go:build darwin

package action

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Storage flags describe how a file's bytes are stored rather than how the user
// sees it. A CoW clone inherited them together with the cloned data, so the
// victim's metadata must not replace them: clearing UF_COMPRESSED on a clone of a
// decmpfs-compressed file leaves the payload in the resource fork while the file
// is marked uncompressed, and it reads back as zero bytes.
const (
	ufCompressed = 0x00000020 // UF_COMPRESSED
	ufDataVault  = 0x00000080 // UF_DATAVAULT
	storageFlags = ufCompressed | ufDataVault
)

// preservePlatformFlags restores the BSD file flags (immutable, hidden,
// append-only, ...) that have no Linux equivalent, keeping the storage flags the
// clone inherited from the data it was cloned from. Flags are applied last
// because an immutable file refuses further metadata changes.
func preservePlatformFlags(dst string, srcInfo os.FileInfo) error {
	srcFlags := flagsOf(srcInfo)
	dstFlags, dstErr := fileFlags(dst)
	if dstErr != nil {
		return dstErr
	}

	if flagErr := unix.Chflags(dst, mergeFlags(srcFlags, dstFlags)); flagErr != nil &&
		!errors.Is(flagErr, os.ErrPermission) {
		return flagErr
	}
	return nil
}

// mergeFlags applies the user-visible flags of the metadata source to the clone
// while leaving the clone's storage flags (which came from the cloned data) in
// place.
func mergeFlags(srcFlags, dstFlags int) int {
	return (srcFlags &^ storageFlags) | (dstFlags & storageFlags)
}

// setStorageFlags copies the storage flags of the keeper (srcInfo) onto dst,
// leaving dst's user-visible flags alone.
func setStorageFlags(dst string, srcInfo os.FileInfo) error {
	dstFlags, err := fileFlags(dst)
	if err != nil {
		return err
	}

	desired := (dstFlags &^ storageFlags) | (flagsOf(srcInfo) & storageFlags)
	if desired == dstFlags {
		return nil
	}
	if flagErr := unix.Chflags(dst, desired); flagErr != nil &&
		!errors.Is(flagErr, os.ErrPermission) {
		return flagErr
	}
	return nil
}

// fileFlags returns the BSD flags of path.
func fileFlags(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return flagsOf(info), nil
}

// flagsOf returns the BSD flags of a stat result, or 0 when the platform does not
// report them.
func flagsOf(info os.FileInfo) int {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return int(stat.Flags)
}
