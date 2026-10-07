//go:build !unix && !windows

package action

import "os"

// isReadOnly reports whether the owner-write bit is missing.
func isReadOnly(_ string, info os.FileInfo) bool {
	return info.Mode().Perm()&0o200 == 0
}

// clearWriteProtection makes a read-only file replaceable, following the
// Windows convention of the owner-write bit.
func clearWriteProtection(path string, info os.FileInfo) (bool, error) {
	if info.Mode().Perm()&0o200 != 0 {
		return false, nil
	}
	if err := os.Chmod(path, info.Mode()|0o200); err != nil {
		return false, err
	}
	return true, nil
}

// restoreWriteProtection puts the original mode back after a failed action.
func restoreWriteProtection(path string, info os.FileInfo, changed bool) {
	if changed {
		_ = os.Chmod(path, info.Mode())
	}
}
