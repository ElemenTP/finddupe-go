// Package config defines the configuration types for finddupe.
// It holds all CLI-driven settings in a single struct passed to the pipeline.
package config

// Action specifies what to do with confirmed duplicate files.
type Action int

const (
	// ActionReport prints duplicates but takes no action (find mode default).
	ActionReport Action = iota
	// ActionDelete removes duplicate files.
	ActionDelete
	// ActionHardlink replaces duplicates with hardlinks to the original.
	ActionHardlink
	// ActionCoWClone replaces duplicates with Copy-on-Write clones.
	ActionCoWClone
)

// Config holds all runtime configuration parsed from CLI flags.
//
// What the run does is expressed by Action plus the mode flags (ListLink,
// CoWDetect); there is no separate mode enum to keep in sync with them.
type Config struct {
	// Action specifies what to do with duplicates (dedupe mode only).
	Action Action

	// Paths is the list of file patterns / directories to scan.
	Paths []string

	// RefPaths is the list of reference patterns (compared against but never acted upon).
	RefPaths []string

	// Threads is the number of scanner worker goroutines.
	// Default: 0 (uses runtime.NumCPU).
	Threads int

	// Verbose enables detailed output.
	Verbose bool

	// ListLink enables hardlink-group listing mode (find --listlink): skip
	// duplicate detection and list files that share a physical inode.
	ListLink bool

	// CoWDetect enables CoW-group listing (find --cow): report duplicate files
	// that also share physical extents.
	CoWDetect bool

	// ShowProgress enables the progress indicator (default: true).
	ShowProgress bool

	// FollowSymlinks follows symbolic links / reparse points.
	FollowSymlinks bool

	// IncludeZeroLen includes zero-length files (skipped by default).
	IncludeZeroLen bool

	// IncludeReadonly also operates on read-only files. Without it they are
	// skipped, on every platform, because replacing one is not always permitted.
	IncludeReadonly bool

	// PreferCompressed keeps a member whose content is stored compressed as the
	// CoW clone source, so the whole group keeps the compressed layout. It only
	// applies to --cow.
	PreferCompressed bool

	// SkipHardlinked skips already-hardlinked duplicate pairs in find mode (--hardlink).
	// When set, files that share the same inode (already hardlinked) are not reported
	// or counted as duplicates. Only content-duplicates with different inodes are shown.
	SkipHardlinked bool
}
