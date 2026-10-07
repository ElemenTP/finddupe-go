package dupe

import "sync/atomic"

// Stats holds scan and action statistics with lock-free concurrent access.
// All fields use atomic operations for safe access from multiple goroutines.
type Stats struct {
	// TotalFiles is the number of files successfully processed.
	TotalFiles atomic.Int64

	// TotalBytes is the sum of sizes of all processed files.
	TotalBytes atomic.Int64

	// DuplicateFiles is the number of confirmed duplicate files found.
	DuplicateFiles atomic.Int64

	// DuplicateBytes is the total size of confirmed duplicate files.
	DuplicateBytes atomic.Int64

	// HardlinkGroups is the number of hardlink groups found (hardlink search mode).
	HardlinkGroups atomic.Int64

	// CantReadFiles is the number of files that could not be read or opened.
	CantReadFiles atomic.Int64

	// ZeroLengthFiles is the number of zero-length files.
	ZeroLengthFiles atomic.Int64

	// DeletedFiles is the number of duplicate files deleted (dedupe --delete).
	DeletedFiles atomic.Int64

	// HardlinkedFiles is the number of duplicate files replaced with hardlinks.
	HardlinkedFiles atomic.Int64

	// CoWClonedFiles is the number of duplicate files replaced with CoW clones.
	CoWClonedFiles atomic.Int64

	// CoWGroups is the number of CoW (shared-extent) groups found by find --cow.
	CoWGroups atomic.Int64

	// CoWSharedBytes is the total number of physically shared bytes found, summed
	// over the members of every group: a byte shared by two members of one group is
	// therefore counted twice, and the number answers "how much of these files is
	// already shared", not "how much storage is saved".
	CoWSharedBytes atomic.Int64

	// SkippedROFiles is the number of read-only files skipped.
	SkippedROFiles atomic.Int64

	// SkippedRefFiles is the number of reference files skipped.
	SkippedRefFiles atomic.Int64

	// SkippedChangedFiles is the number of duplicate pairs left alone because
	// one of the files changed after its content had been hashed.
	SkippedChangedFiles atomic.Int64

	// FailedFiles counts victims whose elimination was attempted and failed
	// (for example a CoW clone on a volume that refuses it). The pair is left
	// untouched; the run continues.
	FailedFiles atomic.Int64
}

// NewStats creates a new zero-initialized Stats.
func NewStats() *Stats {
	return &Stats{}
}

// AddZeroLen increments the zero-length file counter.
// Implements the fswalker.ZeroLenCounter interface.
func (s *Stats) AddZeroLen(delta int64) {
	s.ZeroLengthFiles.Add(delta)
}
