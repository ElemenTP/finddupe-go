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
		if chmodErr := os.Chmod(group.Candidate.Path, 0666); chmodErr != nil { //nolint:gosec
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
