// Package extent queries the physical extents backing files so that
// Copy-on-Write clones (which share physical extents) can be distinguished from
// independent copies.
package extent

import (
	"errors"
	"sort"
)

// ErrUnsupported reports that extent querying is not available for this
// filesystem or platform.
var ErrUnsupported = errors.New("extent query not supported on this filesystem")

// Extent is one run of a file's data.
type Extent struct {
	// Logical is the byte offset of the run within the file.
	Logical uint64

	// Physical is the device-relative physical offset in bytes. It is only
	// meaningful when Encoded is false.
	Physical uint64

	// Length is the logical run length in bytes.
	Length uint64

	// Shared is the filesystem's "shared with another file" hint, when known.
	Shared bool

	// Encoded marks extents whose on-disk representation is not a plain block
	// range (for example compressed btrfs extents). Physical offsets and the
	// logical length are not comparable for such extents.
	Encoded bool
}

// SharedBytes returns the number of bytes that a and b map to the same physical
// storage. Where the same physical range is claimed twice, the overlap is
// counted once.
//
// The primary signal is the physical offset of non-encoded extents. When that
// yields nothing (for example compressed btrfs extents) the logical ranges of
// extents the filesystem itself marked as shared are compared instead.
func SharedBytes(a, b []Extent) int64 {
	if shared := rangeOverlap(a, b, physicalRange); shared > 0 {
		return shared
	}
	return rangeOverlap(a, b, sharedLogicalRange)
}

// interval is a half-open byte range used for overlap arithmetic.
type interval struct {
	start uint64
	end   uint64
}

// rangeKey decides whether an extent participates in a comparison and which
// byte range it contributes.
type rangeKey func(Extent) (interval, bool)

// physicalRange selects plain, non-encoded extents by physical offset.
func physicalRange(e Extent) (interval, bool) {
	if e.Encoded || e.Length == 0 {
		return interval{}, false
	}
	return interval{start: e.Physical, end: e.Physical + e.Length}, true
}

// sharedLogicalRange selects extents the filesystem marked as shared by their
// logical offset.
func sharedLogicalRange(e Extent) (interval, bool) {
	if !e.Shared || e.Length == 0 {
		return interval{}, false
	}
	return interval{start: e.Logical, end: e.Logical + e.Length}, true
}

// rangeOverlap sums the overlap of the ranges both sides map through key.
func rangeOverlap(a, b []Extent, key rangeKey) int64 {
	ia := collectRanges(a, key)
	ib := collectRanges(b, key)
	if len(ia) == 0 || len(ib) == 0 {
		return 0
	}

	var shared int64
	i, j := 0, 0
	for i < len(ia) && j < len(ib) {
		lo := max(ia[i].start, ib[j].start)
		hi := min(ia[i].end, ib[j].end)
		if lo < hi {
			shared += int64(hi - lo) //nolint:gosec // extent lengths are bounded by the file size
		}

		if ia[i].end <= ib[j].end {
			i++
		} else {
			j++
		}
	}
	return shared
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
