// Package dupe provides core types and duplicate detection logic.
package dupe

import (
	"errors"
	"time"
)

// ErrFileChanged reports that a file no longer matches the size (or, when a
// partial digest is being resumed, the modification time) recorded when it was
// scanned. A stale hash must never be used to eliminate a file, so readers
// return this instead of a digest computed from the wrong bytes.
var ErrFileChanged = errors.New("file changed since it was scanned")

// FileInfo holds all metadata needed for duplicate detection on a single file.
type FileInfo struct {
	// Path is the absolute path to the file.
	Path string

	// Size is the file size in bytes.
	Size int64

	// ModTime is the file's modification time when its content was read (or,
	// for files that were never read, when they were scanned). Together with
	// Size it is re-checked immediately before a file is eliminated, so a file
	// that changed after its hash was computed is never acted upon.
	ModTime time.Time

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

	// Dev is the filesystem/volume identifier (st_dev on Unix, volume serial
	// number on Windows). Together with Inode it identifies a physical file.
	Dev uint64

	// Inode is the filesystem object identifier (inode on Unix, file index
	// on Windows). Only unique in combination with Dev.
	Inode uint64

	// NumLinks is the number of hardlinks to this file (0 if unavailable).
	NumLinks uint64

	// IsRef is true if this file is from a --ref path (never to be acted upon).
	IsRef bool
}

// InodeKey identifies a physical file across the whole scan.
// Inode numbers are only unique per device, so Dev must be part of the key.
type InodeKey struct {
	Dev   uint64
	Inode uint64
}

// GroupKey is the composite key for grouping files by weak checksum and size.
type GroupKey struct {
	Signature uint64
	Size      int64
}

// ExecutionType identifies the kind of work an Execution carries.
type ExecutionType uint8

const (
	// HashCalc computes the full SHA-256 of Files[0].
	HashCalc ExecutionType = iota
	// HashComp compares Files[0] and Files[1] in chunks with early-stop.
	HashComp
	// DupeElim eliminates Files[1:] keeping Files[0] as the original.
	DupeElim
	// CoWDetect reports whether the identical Files share physical extents.
	CoWDetect
)

// String implements [fmt.Stringer] for logging and tests.
func (t ExecutionType) String() string {
	switch t {
	case HashCalc:
		return "HashCalc"
	case HashComp:
		return "HashComp"
	case DupeElim:
		return "DupeElim"
	case CoWDetect:
		return "CoWDetect"
	default:
		return "Unknown"
	}
}

// Execution represents a concrete unit of work for the executor:
// a hash computation, a chunked comparison, or a duplicate elimination.
type Execution struct {
	// Key is the composite (signature, size) key the files belong to.
	Key GroupKey

	// Type is the kind of work to perform.
	Type ExecutionType

	// Files are the files this execution operates on.
	// For DupeElim, Files[0] is the keeper and Files[1:] are the victims.
	Files []FileInfo
}
