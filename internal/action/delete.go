package action

import (
	"os"

	"finddupe/internal/dupe"
)

// deleteFile removes the duplicate (victim) file, keeping Files[0]. victimInfo is
// the stat the freshness check already performed, so the read-only decision and
// the write-protection change need no further lookups.
func (e *Executor) deleteFile(ex dupe.Execution, victimInfo os.FileInfo) (Result, error) {
	victimPath := ex.Files[1].Path

	if isReadOnly(victimPath, victimInfo) && !e.opts.IncludeReadonly {
		return ResultSkippedRO, nil
	}

	changed, protectErr := clearWriteProtection(victimPath, victimInfo)
	if protectErr != nil {
		return ResultError, protectErr
	}

	if err := os.Remove(victimPath); err != nil {
		// The file is still there: leave it exactly as it was found.
		restoreWriteProtection(victimPath, victimInfo, changed)
		return ResultError, err
	}

	return ResultDeleted, nil
}
