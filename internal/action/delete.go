package action

import (
	"os"

	"finddupe/internal/dupe"
)

// deleteFile removes the duplicate (victim) file, keeping Files[0].
func (e *Executor) deleteFile(ex dupe.Execution) (Result, error) {
	victimPath := ex.Files[1].Path

	// Check if the file is read-only.
	if isReadOnly(victimPath) {
		if !e.opts.IncludeReadonly {
			return ResultSkippedRO, nil
		}
		// Make writable before deleting.
		// Only add user-write permission (mode | 0200) rather than 0666 to
		// avoid a TOCTOU window where the file is temporarily world-writable.
		info, statErr := os.Stat(victimPath)
		if statErr != nil {
			return ResultError, statErr
		}
		if chmodErr := os.Chmod(victimPath, info.Mode()|0200); chmodErr != nil {
			return ResultError, chmodErr
		}
	}

	if err := os.Remove(victimPath); err != nil {
		return ResultError, err
	}

	return ResultDeleted, nil
}

// isReadOnly checks if a file is read-only for the current user.
func isReadOnly(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	// On Unix, check user write permission.
	// On Windows, check if the read-only attribute is set.
	return info.Mode().Perm()&0200 == 0
}

// DeletePath removes a file at the given path (used by hardlink to delete before linking).
func DeletePath(path string) error {
	return os.Remove(path)
}
