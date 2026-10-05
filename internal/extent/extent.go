// Package extent queries the physical extent information backing files so that
// Copy-on-Write clones (which share storage) can be distinguished from
// independent copies.
package extent

import (
	"errors"
	"slices"
	"sort"
)

// ErrUnsupported reports that extent querying is not available for this
// filesystem or platform.
var ErrUnsupported = errors.New("extent query not supported on this filesystem")

// Query returns the extent information for path as this platform can report it.
//
// On Linux this is FIEMAP, on Windows FSCTL_GET_RETRIEVAL_POINTERS, and on
// macOS fcntl(F_LOG2PHYS_EXT) (with the APFS clone ID as the fallback for
// decmpfs-compressed files; see Extent.Physical). A non-empty file for which the
// platform reports no extents is treated as unsupported rather than as "shares
// nothing", so callers can distinguish "0% shared" from "cannot tell".
//
// Extent lengths are clamped to the file's size: filesystems report whole
// allocated blocks, so a 100000-byte file can come back as one 102400-byte
// extent. Left alone, that makes a fully shared file report more shared bytes
// than it has (and a ratio above 100%).
//
// size is the file size the caller already knows from its scan, so no stat is
// needed here — and it is the size the decision is based on, which is the
// meaningful limit if the file changed since.
//
// A filesystem that supports extent queries but has no extents to report for the
// file (a sparse file, or resident NTFS data) returns an empty slice, not an
// error: nothing is shared, which is a real answer.
func Query(path string, size int64) ([]Extent, error) {
	extents, err := query(path)
	if err != nil {
		if !errors.Is(err, ErrUnsupported) {
			return nil, err
		}
		// Some filesystems report a file that has no extents to report exactly
		// like one they cannot report extents for (btrfs answers EOPNOTSUPP for a
		// fully sparse file). An unallocated file shares nothing, which is a real
		// answer; only a file with data on disk makes the missing information a
		// limitation of the filesystem. On platforms that cannot tell the two
		// apart the error is kept: turning it into "no extents" would report
		// "nothing is shared" for a query that was never answered.
		if size > 0 && unallocatedFile(path) {
			return nil, nil
		}
		return nil, err
	}
	return clampToSize(extents, size), nil
}

// clampToSize drops extents that start at or beyond the end of the file and
// truncates the one that crosses it.
func clampToSize(extents []Extent, size int64) []Extent {
	if size < 0 {
		return extents
	}
	limit := uint64(size)

	out := extents[:0]
	for _, e := range extents {
		if e.Logical >= limit {
			continue
		}
		if e.Length > limit-e.Logical {
			e.Length = limit - e.Logical
		}
		if e.Length == 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Extent is one run of a file's data.
type Extent struct {
	// Logical is the byte offset of the run within the file.
	Logical uint64

	// Physical is the platform's sharing identity for this run: the
	// device-relative physical offset in bytes on Linux, Windows and macOS
	// (F_LOG2PHYS_EXT), or the APFS clone ID on macOS for decmpfs-compressed
	// files (marked Opaque), where files of one clone family share a value.
	Physical uint64

	// Length is the run length in bytes.
	Length uint64

	// Shared is the filesystem's "shared with another file" hint, when known
	// (Linux FIEMAP_EXTENT_SHARED). macOS reports family sharing through
	// Physical instead.
	Shared bool

	// Encoded marks extents whose on-disk representation is not a plain block
	// range (for example compressed btrfs extents). Physical offsets and the
	// logical length are not comparable for such extents.
	Encoded bool

	// Opaque marks Physical as an abstract sharing key rather than a device
	// offset (the APFS clone ID used for decmpfs-compressed files on macOS).
	// Only exact equality is meaningful for such extents: their ranges must
	// never be intersected, because unrelated keys can be numerically close.
	Opaque bool
}

// Equal reports whether two extent lists describe the same storage layout:
// same count in the same logical order, with equal logical offset, physical
// identity, and length.
//
// It is deliberately conservative: it returns false for empty lists and for any
// unknown (zero) physical identity. Encoded (compressed/inline) extents are
// compared too: an exact match of start+length is a sound identity signal even
// when the extent is compressed — only the range-overlap arithmetic in
// SharedBytes is unreliable for those, which is why Encoded exists. Callers use
// this as an "already sharing, skip the work" fast path; returning false merely
// means the work is attempted.
func Equal(a, b []Extent) bool {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i].Physical == 0 || b[i].Physical == 0 {
			return false
		}
		if a[i].Logical != b[i].Logical ||
			a[i].Physical != b[i].Physical ||
			a[i].Length != b[i].Length {
			return false
		}
	}
	return true
}

// SharedFlagBytes returns the number of bytes covered by extents the filesystem
// marked as shared (Linux FIEMAP_EXTENT_SHARED). This is a per-file signal: it
// says the extent is shared with someone, not with whom.
func SharedFlagBytes(e []Extent) int64 {
	var n int64
	for _, x := range e {
		if x.Shared && x.Length > 0 {
			n += int64(x.Length) //nolint:gosec // extent lengths are bounded by the file size
		}
	}
	return n
}

// SharedWithGroup returns, for each list in the group, the number of bytes that
// member shares with at least one *other* member of the group. A nil or empty
// list yields 0.
//
// Identity is the physical range: allocated extents of different files never
// overlap unless the blocks really are shared, so intersecting ranges count a
// shared run even when the filesystem splits it at different boundaries in each
// file (for example an APFS clone whose first blocks were rewritten, where the
// untouched tail becomes its own run starting mid-way through the original's
// run). A byte shared with several others is counted once.
//
// Every member's ranges are merged and then all of them are swept once over
// compressed coordinates, so the per-member answer is exact and the whole group
// costs O(M log M) in the total number of extents M. Asking each member the same
// question against the union of all the others rebuilt nearly the same union n
// times, which cost O(n²·E·log(nE)) for a group of n files.
//
// Encoded (compressed/inline) and opaque extents have no comparable range, so
// their bytes are added from matching physical starts instead. Callers must still
// filter by device: physical identities are only comparable within one.
func SharedWithGroup(group [][]Extent) []int64 {
	merged := mergedPhysicalRanges(group)
	shared := sharedPlainBytes(merged)
	sharedEncodedStarts(group, shared)
	return shared
}

// mergedPhysicalRanges returns each member's plain physical ranges, merged, so
// that every member's ranges are disjoint and counting the ranges covering a
// segment equals counting the members covering it.
func mergedPhysicalRanges(group [][]Extent) [][]interval {
	merged := make([][]interval, len(group))
	for i, extents := range group {
		merged[i] = mergeIntervals(collectRanges(extents, physicalRange))
	}
	return merged
}

// sharedPlainBytes returns the bytes each member shares with another member
// through overlapping physical ranges. The ranges of the whole group are swept
// once over compressed coordinates, which answers every member exactly in
// O(M log M) instead of rebuilding the union of all the others per member.
func sharedPlainBytes(merged [][]interval) []int64 {
	shared := make([]int64, len(merged))

	coords := collectBoundaries(merged)
	if len(coords) == 0 {
		return shared
	}

	index := boundaryIndexer(coords)
	sharedLength := prefixSharedLength(coords, coverageBySegment(merged, coords, index))
	for i, ranges := range merged {
		for _, iv := range ranges {
			lo, hi := index(iv.start), index(iv.end)
			if lo < hi {
				shared[i] += sharedLength[hi] - sharedLength[lo]
			}
		}
	}
	return shared
}

// collectBoundaries returns the sorted, de-duplicated coordinates that delimit
// the physical ranges of the group.
func collectBoundaries(merged [][]interval) []uint64 {
	var coords []uint64
	for _, ranges := range merged {
		for _, iv := range ranges {
			coords = append(coords, iv.start, iv.end)
		}
	}
	if len(coords) == 0 {
		return nil
	}
	slices.Sort(coords)
	return slices.Compact(coords)
}

// boundaryIndexer returns a function that maps a coordinate to its index in
// coords.
func boundaryIndexer(coords []uint64) func(uint64) int {
	return func(v uint64) int {
		return sort.Search(len(coords), func(i int) bool { return coords[i] >= v })
	}
}

// coverageBySegment counts, for every elementary segment between two consecutive
// coordinates, how many members cover it.
func coverageBySegment(merged [][]interval, coords []uint64, index func(uint64) int) []int32 {
	coverage := make([]int32, len(coords)+1)
	for _, ranges := range merged {
		for _, iv := range ranges {
			lo, hi := index(iv.start), index(iv.end)
			coverage[lo]++
			coverage[hi]--
		}
	}
	return coverage
}

// prefixSharedLength turns per-segment coverage into a prefix sum of the bytes at
// least two members cover.
func prefixSharedLength(coords []uint64, coverage []int32) []int64 {
	shared := make([]int64, len(coords)+1)
	active := int32(0)
	for j := range coords {
		active += coverage[j]
		shared[j+1] = shared[j]
		if active >= 2 && j+1 < len(coords) {
			shared[j+1] += int64(coords[j+1] - coords[j]) //nolint:gosec // bounded by the file size
		}
	}
	return shared
}

// startRef records that one member has an extent starting at a physical offset.
type startRef struct {
	member int
	length uint64
}

// sharedEncodedStarts adds the bytes of encoded and opaque extents whose physical
// start appears in another member of the group, capped to the shorter extent.
func sharedEncodedStarts(group [][]Extent, shared []int64) {
	starts := collectPhysicalStarts(group)
	for i, extents := range group {
		shared[i] += encodedSharedBytes(extents, starts, i)
	}
}

// collectPhysicalStarts indexes every extent of the group by its physical start.
func collectPhysicalStarts(group [][]Extent) map[uint64][]startRef {
	starts := make(map[uint64][]startRef)
	for i, extents := range group {
		for _, e := range extents {
			if e.Physical == 0 || e.Length == 0 {
				continue
			}
			starts[e.Physical] = append(starts[e.Physical], startRef{member: i, length: e.Length})
		}
	}
	return starts
}

// encodedSharedBytes sums the bytes of one member's encoded or opaque extents
// whose physical start another member also reports.
func encodedSharedBytes(extents []Extent, starts map[uint64][]startRef, self int) int64 {
	var shared int64
	for _, e := range extents {
		if !e.Encoded && !e.Opaque {
			continue // handled by the physical-range sweep
		}
		if e.Physical == 0 || e.Length == 0 {
			continue
		}
		if shortest := shortestOther(starts[e.Physical], self); shortest > 0 {
			shared += int64(min(e.Length, shortest)) //nolint:gosec // bounded by the file size
		}
	}
	return shared
}

// shortestOther returns the length of the shortest extent at a physical start
// that belongs to a member other than self.
func shortestOther(refs []startRef, self int) uint64 {
	shortest := uint64(0)
	for _, ref := range refs {
		if ref.member == self {
			continue
		}
		if shortest == 0 || ref.length < shortest {
			shortest = ref.length
		}
	}
	return shortest
}

// interval is a half-open byte range used for overlap arithmetic.
type interval struct {
	start uint64
	end   uint64
}

// rangeKey decides whether an extent participates in a comparison and which
// byte range it contributes.
type rangeKey func(Extent) (interval, bool)

// physicalRange selects plain, non-encoded, non-opaque extents by physical
// offset. Opaque identities are excluded: only exact equality is meaningful for
// them, so they are matched by start instead.
func physicalRange(e Extent) (interval, bool) {
	if e.Encoded || e.Opaque || e.Length == 0 {
		return interval{}, false
	}
	return interval{start: e.Physical, end: e.Physical + e.Length}, true
}

// mergeIntervals returns the union of start-sorted half-open ranges, merging
// overlapping and adjacent ones.
func mergeIntervals(ranges []interval) []interval {
	if len(ranges) == 0 {
		return nil
	}
	out := ranges[:1]
	for _, r := range ranges[1:] {
		last := &out[len(out)-1]
		if r.start <= last.end {
			if r.end > last.end {
				last.end = r.end
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// collectRanges extracts and sorts the intervals an extent list maps through key.
func collectRanges(extents []Extent, key rangeKey) []interval {
	out := make([]interval, 0, len(extents))
	for _, e := range extents {
		if iv, ok := key(e); ok {
			out = append(out, iv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}
