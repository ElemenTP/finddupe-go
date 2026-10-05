//go:build unix

package action

import (
	"bytes"
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// preserveMetadata gives dst the metadata of src: ownership, permissions
// (including setuid/setgid/sticky), extended attributes — which carry POSIX
// ACLs on Linux and resource forks or provenance on macOS — BSD file flags, and
// timestamps.
//
// Metadata that cannot be restored without privileges is skipped rather than
// failing the replacement: the clone's content is already in place and correct,
// and a partial metadata restore is closer to the victim than the keeper's
// metadata would be.
func preserveMetadata(dst, src string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		return err
	}

	// Ownership has to come first: chown clears setuid/setgid, so the mode bits
	// are applied afterwards. It is only called when the ids really differ, so
	// a clone that already belongs to the right user never loses those bits.
	srcStat, srcOK := srcInfo.Sys().(*syscall.Stat_t)
	dstStat, dstOK := dstInfo.Sys().(*syscall.Stat_t)
	if srcOK && dstOK && (srcStat.Uid != dstStat.Uid || srcStat.Gid != dstStat.Gid) {
		if chownErr := os.Chown(dst, int(srcStat.Uid), int(srcStat.Gid)); chownErr != nil &&
			!errors.Is(chownErr, os.ErrPermission) {
			return chownErr
		}
	}

	if chmodErr := os.Chmod(dst, metadataMode(srcInfo.Mode())); chmodErr != nil {
		return chmodErr
	}

	copyXattrs(dst, src)

	atime, mtime := fileTimes(srcInfo)
	if timeErr := os.Chtimes(dst, atime, mtime); timeErr != nil {
		return timeErr
	}

	// File flags last: an immutable flag would block the calls above.
	return preservePlatformFlags(dst, src)
}

// copyXattrs makes dst's extended attributes match src's: the victim's are
// copied across and any attribute dst inherited from elsewhere (macOS
// clonefile(2) copies the source's attributes) is removed. Every step is
// best-effort — reading system.* or trusted.* namespaces needs privileges.
func copyXattrs(dst, src string) {
	wanted := make(map[string]bool)

	for _, name := range listXattrs(src) {
		wanted[name] = true
		copyXattr(dst, src, name)
	}

	for _, name := range listXattrs(dst) {
		if !wanted[name] {
			_ = unix.Removexattr(dst, name)
		}
	}
}

// listXattrs returns the extended attribute names of path, or nil when the
// filesystem does not support them.
func listXattrs(path string) []string {
	size, err := unix.Listxattr(path, nil)
	if err != nil || size <= 0 {
		return nil
	}

	buf := make([]byte, size)
	n, err := unix.Listxattr(path, buf)
	if err != nil || n <= 0 {
		return nil
	}

	var names []string
	for part := range bytes.SplitSeq(buf[:n], []byte{0}) {
		if len(part) > 0 {
			names = append(names, string(part))
		}
	}
	return names
}

// copyXattr copies one extended attribute between files.
func copyXattr(dst, src, name string) {
	size, err := unix.Getxattr(src, name, nil)
	if err != nil || size <= 0 {
		return
	}

	value := make([]byte, size)
	n, err := unix.Getxattr(src, name, value)
	if err != nil {
		return
	}
	_ = unix.Setxattr(dst, name, value[:n], 0)
}
