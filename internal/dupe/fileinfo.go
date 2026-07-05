// Package dupe provides core types and duplicate detection logic.
package dupe

// FileInfo holds all metadata needed for duplicate detection on a single file.
type FileInfo struct {
	// Path is the absolute path to the file.
	Path string

	// Size is the file size in bytes.
	Size int64

	// Signature is the 64-bit composite checksum (CRC32 << 32 | Sum32).
	Signature uint64

	// Inode is the filesystem object identifier (inode on Unix, file index on Windows).
	Inode uint64

	// NumLinks is the number of hardlinks to this file (0 if unavailable).
	NumLinks uint64

	// IsRef is true if this file is from a --ref path (never to be acted upon).
	IsRef bool
}

// DupeGroup represents a pair of files that share the same checksum.
// The executor verifies whether they are truly duplicates via full byte comparison.
type DupeGroup struct {
	// Signature is the checksum that matched.
	Signature uint64

	// Original is the first file stored with this checksum (the "kept" file).
	Original FileInfo

	// Candidate is the newly discovered file with the same checksum.
	Candidate FileInfo
}
