package action

import (
	"errors"

	"finddupe/internal/dupe"
)

// ErrCoWNotSupported indicates the filesystem does not support Copy-on-Write cloning.
var ErrCoWNotSupported = errors.New("CoW clone not supported on this filesystem; use --hardlink or --delete instead")

// cloneFile attempts to replace the duplicate with a CoW clone of the original.
// Falls back to ErrCoWNotSupported if the platform or filesystem doesn't support it.
func (e *Executor) cloneFile(group dupe.Execution) (Result, error) {
	err := clonePlatformFile(group.Files[0].Path, group.Files[1].Path)
	if err != nil {
		if errors.Is(err, ErrCoWNotSupported) {
			return ResultError, err
		}
		return ResultError, err
	}
	return ResultCoWCloned, nil
}
