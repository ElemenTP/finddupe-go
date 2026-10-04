package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"finddupe/internal/dupe"
	"finddupe/internal/extent"
)

// ErrCoWNotSupported indicates the filesystem does not support Copy-on-Write cloning.
var ErrCoWNotSupported = errors.New("CoW clone not supported on this filesystem; use --hardlink or --delete instead")

// cloneFile replaces the duplicate (Files[1]) with a CoW clone of the original
// (Files[0]). If the two already share all their storage — because they are the
// same physical file or their extent layout is identical — nothing is done.
//
// The extent check is only a "skip the work" fast path: content equality was
// already established by the detector, so cloning anyway is safe and
// idempotent. Uncertainty (unsupported filesystem, compressed extents) simply
// results in a clone.
func (e *Executor) cloneFile(ex dupe.Execution) (Result, error) {
	keeper := ex.Files[0]
	victim := ex.Files[1]

	if isReadOnly(victim.Path) && !e.opts.IncludeReadonly {
		return ResultSkippedRO, nil
	}

	if alreadyShared(keeper, victim) {
		return ResultAlreadyShared, nil
	}

	if err := cloneReplace(keeper.Path, victim.Path); err != nil {
		return ResultError, err
	}
	return ResultCoWCloned, nil
}

// alreadyShared reports whether the keeper and victim already share all of
// their storage. It is deliberately conservative: any uncertainty returns false
// so the caller performs the clone. Encoded (compressed) extents participate,
// because exact start+length identity is still meaningful for them.
func alreadyShared(keeper, victim dupe.FileInfo) bool {
	if samePhysicalFile(keeper, victim) {
		return true
	}

	keeperExtents, err := extent.Query(keeper.Path)
	if err != nil {
		return false
	}
	victimExtents, err := extent.Query(victim.Path)
	if err != nil {
		return false
	}
	return extent.Equal(keeperExtents, victimExtents)
}

// detectCoW reports, for every file of an identical-content group, how many of
// its bytes are already shared with another group member.
//
// Signal selection:
//   - if any extent in the group carries the filesystem's "shared" flag
//     (Linux FIEMAP_EXTENT_SHARED), that per-extent flag is used;
//   - otherwise the physical start address is used as identity and compared
//     within the group.
//
// FileShared is nil when extent information is unavailable for the whole group.
func (e *Executor) detectCoW(ctx context.Context, ex dupe.Execution) (Outcome, error) {
	out := Outcome{Kind: ex.Type, Key: ex.Key, Files: ex.Files}
	if len(ex.Files) < minFilesPerExecution {
		return out, nil
	}

	select {
	case <-ctx.Done():
		return out, ctx.Err()
	default:
	}

	groupExtents, queried := queryGroupExtents(ex.Files)
	if queried == 0 {
		// Unsupported filesystem: leave FileShared nil.
		return out, nil
	}

	useSharedFlag := usesSharedFlag(groupExtents)
	shared := make([]int64, len(ex.Files))
	for i := range ex.Files {
		shared[i] = fileSharedBytes(ex.Files, i, groupExtents, useSharedFlag)
	}

	out.FileShared = shared
	return out, nil
}

// queryGroupExtents queries every file in the group, returning nils for files
// whose extents could not be read, plus the number of successful queries.
func queryGroupExtents(files []dupe.FileInfo) ([][]extent.Extent, int) {
	groupExtents := make([][]extent.Extent, len(files))
	queried := 0

	for i, fi := range files {
		extents, err := extent.Query(fi.Path)
		if err != nil {
			continue
		}
		groupExtents[i] = extents
		queried++
	}
	return groupExtents, queried
}

// usesSharedFlag reports whether any extent in the group carries the
// filesystem's "shared" flag.
func usesSharedFlag(groupExtents [][]extent.Extent) bool {
	for _, extents := range groupExtents {
		for _, x := range extents {
			if x.Shared {
				return true
			}
		}
	}
	return false
}

// fileSharedBytes returns the already-shared bytes of one group member.
func fileSharedBytes(files []dupe.FileInfo, i int, groupExtents [][]extent.Extent, useSharedFlag bool) int64 {
	own := groupExtents[i]
	if own == nil {
		return 0
	}
	if useSharedFlag {
		return extent.SharedFlagBytes(own)
	}

	fi := files[i]
	others := make([][]extent.Extent, 0, len(files)-1)
	for j, list := range groupExtents {
		if j == i || list == nil {
			continue
		}
		// Physical addresses are only comparable within one device.
		if fi.Dev != 0 && files[j].Dev != 0 && fi.Dev != files[j].Dev {
			continue
		}
		others = append(others, list)
	}
	return extent.SharedWithOthers(own, others)
}

// cloneReplace writes a CoW clone of src to a temporary file next to dst and
// atomically replaces dst with it. On any failure dst is left untouched.
func cloneReplace(src, dst string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".finddupe-cow-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}

	// clonefile(2) requires the destination not to exist; the Linux and Windows
	// block-clone implementations expect to create it themselves.
	if removeErr := os.Remove(tmpPath); removeErr != nil {
		return removeErr
	}

	if cloneErr := clonePlatformFile(src, tmpPath); cloneErr != nil {
		_ = os.Remove(tmpPath)
		return cloneErr
	}

	if metaErr := preserveVictimMetadata(tmpPath, dst); metaErr != nil {
		_ = os.Remove(tmpPath)
		return metaErr
	}

	if replaceErr := replaceFile(tmpPath, dst); replaceErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace %s: %w", dst, replaceErr)
	}
	return nil
}

// preserveVictimMetadata copies the victim's mode and modification time onto the
// clone and clears write-protection on the victim, which Windows requires before
// it can be replaced.
func preserveVictimMetadata(tmpPath, dst string) error {
	info, err := os.Stat(dst)
	if err != nil {
		return nil //nolint:nilerr // the victim vanished; nothing left to preserve
	}

	if chmodErr := os.Chmod(tmpPath, info.Mode().Perm()); chmodErr != nil {
		return chmodErr
	}
	if chtimesErr := os.Chtimes(tmpPath, info.ModTime(), info.ModTime()); chtimesErr != nil {
		return chtimesErr
	}
	if info.Mode().Perm()&0o200 == 0 {
		return os.Chmod(dst, info.Mode().Perm()|0o200)
	}
	return nil
}
