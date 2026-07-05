package action

import (
	"os"

	"finddupe/internal/dupe"
)

// deleteFile removes the duplicate file.
func (e *Executor) deleteFile(group dupe.DupeGroup) (Result, error) {
	// Check if the file is read-only.
	if isReadOnly(group.Candidate.Path) {
		if !e.opts.IncludeReadonly {
			return ResultSkippedRO, nil
		}
		// Make writable before deleting.
		// Only add user-write permission (mode | 0200) rather than 0666 to
		// avoid a TOCTOU window where the file is temporarily world-writable.
		info, statErr := os.Stat(group.Candidate.Path)
		if statErr != nil {
			return ResultError, statErr
		}
		if chmodErr := os.Chmod(group.Candidate.Path, info.Mode()|0200); chmodErr != nil {
			return ResultError, chmodErr
		}
	}

	if err := os.Remove(group.Candidate.Path); err != nil {
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
