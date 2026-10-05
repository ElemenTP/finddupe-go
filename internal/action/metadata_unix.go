//go:build unix

package action

import (
	"bytes"
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// listXattrGrowth is the factor the attribute-name buffer is grown by when the
// set grew between the size query and the read.
const listXattrGrowth = 2

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

	// Extended attributes need a writable file: Linux refuses setxattr(user.*)
	// on a read-only file even for its owner (and macOS removexattr likewise),
	// so they are copied before the final mode is applied.
	copyXattrs(dst, src)

	atime, mtime := fileTimes(srcInfo)
	if timeErr := os.Chtimes(dst, atime, mtime); timeErr != nil {
		return timeErr
	}

	// The mode goes last so that a read-only victim's permissions cannot block
	// the calls above, and so setuid/setgid survive the chown.
	if chmodErr := os.Chmod(dst, metadataMode(srcInfo.Mode())); chmodErr != nil {
		return chmodErr
	}

	// File flags last: an immutable flag would block the calls above.
	return preservePlatformFlags(dst, src)
}

// copyXattrs makes dst's extended attributes match src's: the victim's are
// copied across and any attribute dst inherited from elsewhere (macOS
// clonefile(2) copies the source's attributes) is removed. Every step is
// best-effort — reading system.* or trusted.* namespaces needs privileges.
//
// Attributes that describe where the file's bytes live are the exception: a CoW
// clone inherited them from the cloned data (macOS keeps a compressed file's
// payload in com.apple.ResourceFork and its header in com.apple.decmpfs), so they
// must follow the keeper, not the victim's metadata. Removing them left a clone
// whose data fork was empty — a zero-length file — and copying them from a
// compressed victim onto an uncompressed clone would attach a payload the file
// does not have.
//
// The removal loop only runs when src's own attributes could be listed: treating
// "could not read" as "no attributes" would strip everything the clone has.
func copyXattrs(dst, src string) {
	srcNames, srcOK := listXattrs(src)
	if !srcOK {
		return
	}

	wanted := make(map[string]bool, len(srcNames))
	for _, name := range srcNames {
		wanted[name] = true
		if !dataLayoutXattr(name) {
			copyXattr(dst, src, name)
		}
	}

	dstNames, dstOK := listXattrs(dst)
	if !dstOK {
		return
	}
	for _, name := range dstNames {
		if !wanted[name] && !dataLayoutXattr(name) {
			_ = unix.Removexattr(dst, name)
		}
	}
}

// listXattrs returns the extended attribute names of path. ok is false when the
// listing could not be obtained (unsupported filesystem, permission, or a
// size/read race), which the caller must not confuse with "no attributes".
func listXattrs(path string) ([]string, bool) {
	size, err := unix.Listxattr(path, nil)
	if err != nil {
		return nil, false
	}
	if size <= 0 {
		return nil, true
	}

	// The set can grow between the size query and the read; grow the buffer once
	// more instead of reporting "no attributes".
	buf := make([]byte, size)
	n, readErr := unix.Listxattr(path, buf)
	if errors.Is(readErr, unix.ERANGE) {
		buf = make([]byte, size*listXattrGrowth)
		n, readErr = unix.Listxattr(path, buf)
	}
	if readErr != nil {
		return nil, false
	}

	var names []string
	for part := range bytes.SplitSeq(buf[:max(n, 0)], []byte{0}) {
		if len(part) > 0 {
			names = append(names, string(part))
		}
	}
	return names, true
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
