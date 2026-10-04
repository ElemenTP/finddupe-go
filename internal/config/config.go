// Package config defines the configuration types for finddupe.
// It holds all CLI-driven settings in a single struct passed to the pipeline.
package config

// Mode specifies the operation mode.
type Mode int

const (
	// ModeFind scans and reports duplicates without taking action.
	ModeFind Mode = iota
	// ModeDedupe scans and eliminates duplicates.
	ModeDedupe
)

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
type Config struct {
	// Mode is the operation mode (find or dedupe).
	Mode Mode

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

	// IncludeReadonly operates on read-only files (dedupe mode, Windows).
	IncludeReadonly bool

	// SkipHardlinked skips already-hardlinked duplicate pairs in find mode (--hardlink).
	// When set, files that share the same inode (already hardlinked) are not reported
	// or counted as duplicates. Only content-duplicates with different inodes are shown.
	SkipHardlinked bool
}
