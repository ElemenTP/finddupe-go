//go:build unix

package action

import (
	"os"

	"golang.org/x/sys/unix"
)

// isReadOnly reports whether the current user cannot write to the file. It asks
// the kernel rather than inspecting the owner bit, so a file that is read-only
// only because of its group or other bits, an ACL or a read-only mount is
// classified correctly.
func isReadOnly(path string, _ os.FileInfo) bool {
	return unix.Access(path, unix.W_OK) != nil
}

// clearWriteProtection is a no-op on Unix: unlinking and renaming a file only
// need write permission on its directory, so a read-only file can be replaced as
// it is. Changing the mode here would also change it for the file's other
// hardlinks, which is a side effect the failed-action rollback cannot undo for
// them.
func clearWriteProtection(string, os.FileInfo) (bool, error) {
	return false, nil
}

// restoreWriteProtection reverses clearWriteProtection; on Unix nothing was
// changed.
func restoreWriteProtection(string, os.FileInfo, bool) {}
