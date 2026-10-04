// Package extent queries the physical extent information backing files so that
// Copy-on-Write clones (which share storage) can be distinguished from
// independent copies.
package extent

import (
	"errors"
	"os"
	"sort"
)

// ErrUnsupported reports that extent querying is not available for this
// filesystem or platform.
var ErrUnsupported = errors.New("extent query not supported on this filesystem")

// Query returns the extent information for path as this platform can report it.
//
// On Linux this is FIEMAP, on Windows FSCTL_GET_RETRIEVAL_POINTERS, and on
// macOS a single synthetic extent carrying the APFS clone ID (see
// Extent.Physical). A non-empty file for which the platform reports no extents
// is treated as unsupported rather than as "shares nothing", so callers can
// distinguish "0% shared" from "cannot tell".
func Query(path string) ([]Extent, error) {
	extents, err := query(path)
	if err != nil {
		return nil, err
	}

	if len(extents) == 0 {
		if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
			return nil, ErrUnsupported
		}
	}
	return extents, nil
}

// Extent is one run of a file's data.
type Extent struct {
	// Logical is the byte offset of the run within the file.
	Logical uint64

	// Physical is the platform's sharing identity for this run. On Linux and
	// Windows it is the device-relative physical offset in bytes; on macOS it
	// is the APFS clone ID (so files of one clone family share a value). It is
	// only meaningful when Encoded is false.
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

// SharedWithOthers returns the number of bytes of own whose physical start is
// also present in one of the other extent lists. Extents without a known
// physical address are ignored, and a match is capped to the shorter extent so
// a longer own extent is not over-counted.
//
// It is meant for in-group diagnostics on filesystems that do not expose a
// "shared" flag: identity is the physical start, as reported by the platform.
func SharedWithOthers(own []Extent, others [][]Extent) int64 {
	if len(own) == 0 || len(others) == 0 {
		return 0
	}

	shortest := make(map[uint64]uint64, len(own))
	for _, list := range others {
		for _, e := range list {
			if e.Physical == 0 || e.Length == 0 {
				continue
			}
			if cur, ok := shortest[e.Physical]; !ok || e.Length < cur {
				shortest[e.Physical] = e.Length
			}
		}
	}

	var n int64
	for _, e := range own {
		if e.Physical == 0 || e.Length == 0 {
			continue
		}
		other, ok := shortest[e.Physical]
		if !ok {
			continue
		}
		shared := min(e.Length, other)
		n += int64(shared) //nolint:gosec // extent lengths are bounded by the file size
	}
	return n
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
