package action

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"finddupe/internal/dupe"
	"finddupe/internal/extent"
)

// ErrCoWNotSupported indicates the filesystem does not support Copy-on-Write cloning.
var ErrCoWNotSupported = errors.New("CoW clone not supported on this filesystem; use --hardlink or --delete instead")

// ErrCrossDevice indicates a clone was refused because the two files live on
// different volumes. It is not a filesystem limitation: a clone shares storage
// blocks, and blocks cannot be shared across a volume boundary, so no CoW
// implementation can satisfy the request.
var ErrCrossDevice = errors.New("cannot clone across volumes")

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

	if !sameDevice(keeper, victim) {
		return ResultSkippedCrossDevice, nil
	}

	if e.alreadyShared(keeper, victim) {
		return ResultAlreadyShared, nil
	}

	if err := cloneReplace(keeper.Path, victim.Path); err != nil {
		// A volume boundary can also surface here when the device of one side was
		// unknown before the attempt; that is a skip, not a failure.
		if errors.Is(err, ErrCrossDevice) {
			return ResultSkippedCrossDevice, nil
		}
		return ResultError, err
	}
	return ResultCoWCloned, nil
}

// alreadyShared reports whether the keeper and victim already share all of
// their storage. It is deliberately conservative: any uncertainty returns false
// so the caller performs the clone. Encoded (compressed) extents participate,
// because exact start+length identity is still meaningful for them.
func (e *Executor) alreadyShared(keeper, victim dupe.FileInfo) bool {
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

	// Every victim of one group asks about the same keeper, so its layout is
	// remembered: a group of n files otherwise pays n−1 extra opens and FIEMAP
	// calls (with FIEMAP_FLAG_SYNC on Linux) for an answer that cannot change —
	// the keeper is never modified by an elimination.
	keeperExtents, ok := e.layouts.get(keeper)
	if !ok {
		var err error
		if keeperExtents, err = extent.Query(keeper.Path, keeper.Size); err != nil {
			return false
		}
		e.layouts.put(keeper, keeperExtents)
	}

	victimExtents, err := extent.Query(victim.Path, victim.Size)
	if err != nil {
		return false
	}
	return extent.Equal(keeperExtents, victimExtents)
}

// keeperLayoutCache holds the extent layouts of the keepers whose groups are being
// eliminated. Entries are keyed by path, size and modification time, so a file that
// changed since it was scanned cannot be answered from a stale layout, and the map
// is dropped once it reaches layoutCacheLimit: a run over a huge tree must not
// accumulate an extent list per group forever.
type keeperLayoutCache struct {
	mu      sync.Mutex
	entries map[string]keeperLayout
}

// layoutCacheLimit bounds the cache. Reaching it clears the map; the next victims
// of each group re-query once, which is the cost this cache exists to avoid only
// for the group being processed now.
const layoutCacheLimit = 4096

type keeperLayout struct {
	size    int64
	modTime time.Time
	extents []extent.Extent
}

// get returns the cached layout of fi, if one was stored for exactly this version
// of the file.
func (c *keeperLayoutCache) get(fi dupe.FileInfo) ([]extent.Extent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[fi.Path]
	if !ok || entry.size != fi.Size || !entry.modTime.Equal(fi.ModTime) {
		return nil, false
	}
	return entry.extents, true
}

// put stores the layout of fi.
func (c *keeperLayoutCache) put(fi dupe.FileInfo, extents []extent.Extent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = make(map[string]keeperLayout)
	}
	if len(c.entries) >= layoutCacheLimit {
		clear(c.entries)
	}
	c.entries[fi.Path] = keeperLayout{size: fi.Size, modTime: fi.ModTime, extents: extents}
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
	if physicalIdentityMissing(groupExtents) {
		// The volume has extents to describe but reports no physical identity for
		// them, so nothing can be compared: the group has no answer, and printing
		// 0% would claim that nothing is shared.
		return out, nil
	}

	out.FileShared = groupSharedBytes(ex.Files, groupExtents)
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

// physicalIdentityMissing reports whether the group has extents but none of them
// carries a physical identity. The kernel's per-extent "shared" flag is not a
// substitute: it says the extent is shared with *someone*, which is not the
// question — every member's ratio is how much it shares with the rest of its
// group. A group with no extents at all (a fully sparse file) is not missing
// anything: it genuinely shares nothing.
func physicalIdentityMissing(groupExtents [][]extent.Extent) bool {
	sawExtent := false
	for _, extents := range groupExtents {
		for _, x := range extents {
			if x.Physical != 0 {
				return false
			}
			sawExtent = true
		}
	}
	return sawExtent
}

// groupSharedBytes returns the already-shared bytes of every group member.
//
// Physical addresses are only comparable within one device, so the members are
// bucketed by device first and each bucket is answered in one group-wide pass
// instead of rebuilding the union of all the others for every member (which cost
// O(n²) extent work for a group of n files). An unknown device forms its own
// bucket: comparing it against a known one could invent sharing that does not
// exist, while reporting too little only means a clone is attempted.
func groupSharedBytes(files []dupe.FileInfo, groupExtents [][]extent.Extent) []int64 {
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

		// The clone's bytes came from src, so anything that describes where those
		// bytes live belongs to src as well; the victim's metadata must not decide
		// it (see preserveDataLayout). It goes first so that the victim's file flags,
		// which are applied after, cannot block the attribute writes.
		if layoutErr := preserveDataLayout(tmpPath, src); layoutErr != nil {
			_ = os.Remove(tmpPath)
			return layoutErr
		}

		victimInfo, cleared, metaErr := preserveVictimMetadata(tmpPath, dst)
		if metaErr != nil {
			_ = os.Remove(tmpPath)
			return metaErr
		}

		// Everything below can still fail and the victim was already made
		// replaceable: put its write protection back before giving up, so a failed
		// clone leaves the victim exactly as it was found.
		abandon := func(err error) error {
			restoreWriteProtection(dst, victimInfo, cleared)
			_ = os.Remove(tmpPath)
			return err
		}

		// A clone must hold as many bytes as the file it was cloned from. This is
		// what stands between a filesystem that silently produced an unusable clone
		// and the victim being replaced by it: the victim is left untouched and the
		// action is reported as failed instead.
		if sizeErr := requireSameSize(tmpPath, src); sizeErr != nil {
			return abandon(sizeErr)
		}
		return nil
	})
}

// requireSameSize checks that a clone holds as many bytes as the file it was
// cloned from.
func requireSameSize(clonePath, sourcePath string) error {
	cloneInfo, err := os.Stat(clonePath)
	if err != nil {
		return err
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	if cloneInfo.Size() != sourceInfo.Size() {
		return fmt.Errorf("clone of %s holds %d bytes instead of %d: refused to replace the duplicate with it",
			sourcePath, cloneInfo.Size(), sourceInfo.Size())
	}
	return nil
}

// preserveVictimMetadata gives the clone the victim's metadata — as much of it
// as the platform can restore, see preserveMetadata — and then clears the victim's
// write protection, which Windows needs before it can be replaced. The victim's
// stat and whether its mode was changed are returned so a later failure can put the
// protection back; on Unix clearing is a no-op and nothing needs restoring.
//
// Unlike hardlinking, cloning does not share an inode with the keeper, so the
// victim's identity can be preserved instead of inheriting the keeper's.
func preserveVictimMetadata(tmpPath, dst string) (os.FileInfo, bool, error) {
	info, err := os.Stat(dst)
	if err != nil {
		// The victim vanished: do not resurrect it from the keeper's content.
		return nil, false, err
	}

	// The metadata is applied before the victim's mode is touched, so the clone
	// inherits the mode the user gave the victim even where making the victim
	// writable is unavoidable.
	if metaErr := preserveMetadata(tmpPath, dst); metaErr != nil {
		return nil, false, metaErr
	}

	cleared, clearErr := clearWriteProtection(dst, info)
	if clearErr != nil {
		return nil, false, clearErr
	}
	return info, cleared, nil
}
