package action

import (
	"os"

	"finddupe/internal/dupe"
)

// createHardlink replaces the duplicate file (Files[1]) with a hardlink to the
// original (Files[0]).
//
// The link is created under a temporary name next to the victim and then
// renamed over it. Renaming is atomic within a filesystem, so the victim is
// either the untouched original file or the finished hardlink — never a
// half-finished state, and never missing. Deleting first (which is what a
// plain remove-then-link does) would destroy the victim whenever linking
// fails, for example across devices or on a filesystem without hard links.
//
// Hard links share the keeper's inode and therefore its permissions and
// timestamps. The victim's own metadata cannot be preserved and is not
// restored: doing so would silently rewrite the keeper's metadata as well.
func (e *Executor) createHardlink(ex dupe.Execution) (Result, error) {
	keeper := ex.Files[0]
	victim := ex.Files[1]

	// Check the hardlink limit again here: the count in the scan listing is
	// stale, because every link created during this run has raised the real
	// one, and NTFS rejects the link that crosses the limit.
	if hardlinkLimitReached(keeper.Path) {
		return ResultHardlinkLimit, nil
	}

	// Check if candidate is read-only.
	readOnly := isReadOnly(victim.Path)
	if readOnly && !e.opts.IncludeReadonly {
		return ResultSkippedRO, nil
	}

	// Windows refuses to replace a read-only file; clear the write protection
	// before the rename (same requirement as the CoW replacement).
	if readOnly {
		info, statErr := os.Stat(victim.Path)
		if statErr != nil {
			return ResultError, statErr
		}
		if chmodErr := os.Chmod(victim.Path, info.Mode()|0o200); chmodErr != nil {
			return ResultError, chmodErr
		}
	}

	if err := linkReplace(keeper.Path, victim.Path); err != nil {
		return ResultError, err
	}

	return ResultHardlinked, nil
}
