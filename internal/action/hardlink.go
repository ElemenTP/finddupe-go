package action

import (
	"os"

	"finddupe/internal/dupe"
)

// createHardlink replaces the duplicate file with a hardlink to the original.
// Strategy: delete the duplicate, then create a hardlink at its path pointing to the original.
func (e *Executor) createHardlink(group dupe.DupeGroup) (Result, error) {
	candidatePath := group.Candidate.Path
	originalPath := group.Original.Path

	// Check NTFS hardlink limit (1023 links per file on Windows).
	if group.Original.NumLinks >= MaxHardlinks {
		return ResultHardlinkLimit, nil
	}

	// Check if candidate is read-only.
	readOnly := isReadOnly(candidatePath)
	if readOnly && !e.opts.IncludeReadonly {
		return ResultSkippedRO, nil
	}

	// Save the original mode for restoration.
	origInfo, statErr := os.Stat(candidatePath)
	if statErr != nil {
		return ResultError, statErr
	}
	origMode := origInfo.Mode()
	origModTime := origInfo.ModTime()

	// Make writable if needed. Only add user-write permission
	// (mode | 0200) rather than 0666 to avoid a TOCTOU window where
	// the file is temporarily world-writable on multi-user systems.
	if readOnly {
		if chmodErr := os.Chmod(candidatePath, origMode|0200); chmodErr != nil {
			return ResultError, chmodErr
		}
	}

	// Delete the duplicate.
	if removeErr := os.Remove(candidatePath); removeErr != nil {
		return ResultError, removeErr
	}

	// Create hardlink to the original.
	if linkErr := createPlatformHardlink(candidatePath, originalPath); linkErr != nil {
		return ResultError, linkErr
	}

	// Restore original mode and modification time.
	if chmodErr := os.Chmod(candidatePath, origMode); chmodErr != nil {
		return ResultError, chmodErr
	}
	if chtimesErr := os.Chtimes(candidatePath, origModTime, origModTime); chtimesErr != nil {
		return ResultError, chtimesErr
	}

	return ResultHardlinked, nil
}
