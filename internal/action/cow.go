package action

import (
	"context"
	"errors"
	"io/fs"
	"os"

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
func (e *Executor) cloneFile(ex dupe.Execution, victimInfo os.FileInfo) (Result, error) {
	keeper := ex.Files[0]
	victim := ex.Files[1]

	if isReadOnly(victim.Path, victimInfo) && !e.opts.IncludeReadonly {
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

	// Physical offsets are only comparable within one device, so two files on
	// different volumes must never be declared identical from their layouts. An
	// unknown device counts as "different": claiming shared storage would skip a
	// clone that is needed, while cloning a pair that already shares its blocks
	// is harmless.
	if keeper.Dev == 0 || victim.Dev == 0 || keeper.Dev != victim.Dev {
		return false
	}

	keeperExtents, err := extent.Query(keeper.Path, keeper.Size)
	if err != nil {
		return false
	}
	victimExtents, err := extent.Query(victim.Path, victim.Size)
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

	out.FileShared = groupSharedBytes(ex.Files, groupExtents, usesSharedFlag(groupExtents))
	return out, nil
}

// queryGroupExtents queries every file in the group, returning nils for files
// whose extents could not be read, plus the number of successful queries.
func queryGroupExtents(files []dupe.FileInfo) ([][]extent.Extent, int) {
	groupExtents := make([][]extent.Extent, len(files))
	queried := 0

	for i, fi := range files {
		extents, err := extent.Query(fi.Path, fi.Size)
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

// groupSharedBytes returns the already-shared bytes of every group member.
//
// Physical addresses are only comparable within one device, so the members are
// bucketed by device first and each bucket is answered in one group-wide pass
// instead of rebuilding the union of all the others for every member (which cost
// O(n²) extent work for a group of n files). An unknown device forms its own
// bucket: comparing it against a known one could invent sharing that does not
// exist, while reporting too little only means a clone is attempted.
func groupSharedBytes(files []dupe.FileInfo, groupExtents [][]extent.Extent, useSharedFlag bool) []int64 {
	if useSharedFlag {
		shared := make([]int64, len(files))
		for i, extents := range groupExtents {
			shared[i] = extent.SharedFlagBytes(extents)
		}
		return shared
	}

	buckets := make(map[uint64][]int, len(files))
	for i, fi := range files {
		buckets[fi.Dev] = append(buckets[fi.Dev], i)
	}

	shared := make([]int64, len(files))
	for _, indexes := range buckets {
		lists := make([][]extent.Extent, len(indexes))
		for j, i := range indexes {
			lists[j] = groupExtents[i]
		}
		perMember := extent.SharedWithGroup(lists)
		for j, i := range indexes {
			shared[i] = perMember[j]
		}
	}
	return shared
}

// cloneReplace writes a CoW clone of src to a free temporary name next to dst and
// atomically replaces dst with it. On any failure dst is left untouched.
func cloneReplace(src, dst string) error {
	return withTemporaryName(dst, func(tmpPath string) error {
		if cloneErr := clonePlatformFile(src, tmpPath); cloneErr != nil {
			// A clone that failed after creating (part of) the file leaves it
			// behind; a name collision belongs to someone else and is left alone.
			if !errors.Is(cloneErr, fs.ErrExist) {
				_ = os.Remove(tmpPath)
			}
			return cloneErr
		}

		if metaErr := preserveVictimMetadata(tmpPath, dst); metaErr != nil {
			_ = os.Remove(tmpPath)
			return metaErr
		}
		return nil
	})
}

// preserveVictimMetadata gives the clone the victim's metadata — as much of it
// as the platform can restore, see preserveMetadata — and clears write
// protection on the victim, which Windows requires before it can be replaced.
//
// Unlike hardlinking, cloning does not share an inode with the keeper, so the
// victim's identity can be preserved instead of inheriting the keeper's.
func preserveVictimMetadata(tmpPath, dst string) error {
	info, err := os.Stat(dst)
	if err != nil {
		// The victim vanished: do not resurrect it from the keeper's content.
		return err
	}

	if metaErr := preserveMetadata(tmpPath, dst); metaErr != nil {
		return metaErr
	}

	if info.Mode().Perm()&0o200 == 0 {
		return os.Chmod(dst, info.Mode().Perm()|0o200)
	}
	return nil
}
