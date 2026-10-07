//go:build windows

package action

import "os"

// isReadOnly reports whether the file carries the read-only attribute. Go maps
// FILE_ATTRIBUTE_READONLY onto the owner-write bit of the mode.
func isReadOnly(_ string, info os.FileInfo) bool {
	return info.Mode().Perm()&0o200 == 0
}

// clearWriteProtection makes a read-only file replaceable: Windows refuses to
// delete or rename over one. changed reports whether the mode was altered, so a
// failed action can put it back.
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
