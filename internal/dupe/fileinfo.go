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

	// SHA256 is the SHA-256 hash of the full file content.
	// Zero value means "not yet computed".
	SHA256 [32]byte

	// HashState is the marshaled state of an in-progress SHA-256 digest,
	// used to resume hashing from HashOffset without re-reading the file
	// from the beginning. nil means hashing has not started.
	HashState []byte

	// HashOffset is the number of bytes already fed into the SHA-256 digest.
	// Used with HashState to resume incremental hashing.
	HashOffset int64

	// Inode is the filesystem object identifier (inode on Unix, file index on Windows).
	Inode uint64

	// NumLinks is the number of hardlinks to this file (0 if unavailable).
	NumLinks uint64

	// IsRef is true if this file is from a --ref path (never to be acted upon).
	IsRef bool
}

// GroupKey is the composite key for grouping files by weak checksum and size.
type GroupKey struct {
	Signature uint64
	Size      int64
}
type ExecutionType int

const (
	HashCalc ExecutionType = iota
	HashComp
	DupeElim
)

// Execution represents specific opeartion for executor to execute.
// SHA-256 calculation, comparison, and dupe file elimination.
type Execution struct {
	// Key is the composite (signature, size) key that matched.
	Key GroupKey

	// Type of this exection.
	Type ExecutionType

	// List of files to execute on.
	Files []FileInfo
}
