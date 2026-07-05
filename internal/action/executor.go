// Package action performs duplicate file verification and elimination.
package action

import (
	"context"
	"crypto/sha256"
	"encoding"
	"fmt"
	"hash"
	"io"
	"os"

	"finddupe/internal/config"
	"finddupe/internal/dupe"
)

// MaxHardlinks is the maximum number of hardlinks per file on Windows NTFS.
const MaxHardlinks = 1023

// Result describes the outcome of processing a duplicate file.
type Result int

const (
	ResultVerifiedDuplicate Result = iota
	ResultAlreadyHardlinked
	ResultDeleted
	ResultHardlinked
	ResultCoWCloned
	ResultSkippedRO
	ResultSkippedRef
	ResultHardlinkLimit
	ResultNotDuplicate
	ResultError
)

// Options configures the executor's behavior.
type Options struct {
	Action          config.Action
	IncludeReadonly bool
	SkipHardlinked  bool
}

// Executor verifies and eliminates duplicate files.
type Executor struct {
	opts Options
}

// New creates a new Executor.
func New(opts Options) *Executor {
	return &Executor{opts: opts}
}

// chunkSizeFor returns an appropriate I/O chunk size for a file of the given size.
// Small files are read in one chunk; medium files use 64 KB chunks for fast
// early-stop; large files use 1 MB chunks to reduce syscall overhead.
func chunkSizeFor(fileSize int64) int64 {
	switch {
	case fileSize <= 64*1024:
		return fileSize // one read
	case fileSize <= 16*1024*1024:
		return 64 * 1024
	default:
		return 1024 * 1024
	}
}

// VerifyChunked performs chunked SHA-256 comparison between the candidate and
// original file. If either file has saved hash state (from a previous early-stop),
// hashing resumes from the saved offset to avoid re-reading.
//
// Returns updated FileInfo for both files with current hash state and offset.
// If the files are identical and the full file was hashed, SHA-256 is complete.
func (e *Executor) VerifyChunked(
	ctx context.Context, group dupe.DupeGroup,
) (Result, dupe.FileInfo, dupe.FileInfo, error) {
	select {
	case <-ctx.Done():
		return ResultError, group.Original, group.Candidate, ctx.Err()
	default:
	}

	// Skip already-hardlinked pairs (same inode).
	if e.opts.SkipHardlinked && group.Original.Inode != 0 &&
		group.Original.Inode == group.Candidate.Inode &&
		group.Original.NumLinks > 1 {
		return ResultAlreadyHardlinked, group.Original, group.Candidate, nil
	}

	chunkSize := chunkSizeFor(group.Candidate.Size)

	orig, cand, err := hashCompare(ctx, group.Original, group.Candidate, chunkSize)
	if err != nil {
		return ResultError, orig, cand, fmt.Errorf("verify %s: %w", group.Candidate.Path, err)
	}

	// If either file's SHA-256 is still incomplete, the files are not duplicates.
	if orig.SHA256 == ([32]byte{}) || cand.SHA256 == ([32]byte{}) {
		return ResultNotDuplicate, orig, cand, nil
	}

	// Both files have complete SHA-256 and match — confirmed duplicates.
	if group.Candidate.IsRef {
		return ResultSkippedRef, orig, cand, nil
	}

	result, err := e.execute(ctx, group)
	return result, orig, cand, err
}

// hashCompare reads two files chunk by chunk, updating SHA-256 hashers and
// comparing accumulated hashes after each chunk for early-stop. Saved hash state
// is resumed if available.
func hashCompare(
	ctx context.Context, orig, cand dupe.FileInfo, chunkSize int64,
) (dupe.FileInfo, dupe.FileInfo, error) {
	// Open files.
	fOrig, err := os.Open(orig.Path)
	if err != nil {
		return orig, cand, err
	}
	defer fOrig.Close()

	fCand, err := os.Open(cand.Path)
	if err != nil {
		return orig, cand, err
	}
	defer fCand.Close()

	// Restore or create hashers.
	hOrig, err := restoreHasher(orig.HashState)
	if err != nil {
		return orig, cand, err
	}
	hCand, err := restoreHasher(cand.HashState)
	if err != nil {
		return orig, cand, err
	}

	// Seek to saved offsets.
	if _, err := fOrig.Seek(orig.HashOffset, io.SeekStart); err != nil {
		return orig, cand, err
	}
	if _, err := fCand.Seek(cand.HashOffset, io.SeekStart); err != nil {
		return orig, cand, err
	}

	remaining := orig.Size - orig.HashOffset
	bufOrig := make([]byte, chunkSize)
	bufCand := make([]byte, chunkSize)

	for remaining > 0 {
		select {
		case <-ctx.Done():
			return orig, cand, ctx.Err()
		default:
		}

		toRead := chunkSize
		if toRead > remaining {
			toRead = remaining
		}

		nOrig, _ := io.ReadFull(fOrig, bufOrig[:toRead])
		nCand, _ := io.ReadFull(fCand, bufCand[:toRead])

		if nOrig != nCand {
			// Truncated file — not a duplicate.
			saveHashState(&orig, hOrig, nOrig)
			saveHashState(&cand, hCand, nCand)
			return orig, cand, nil
		}

		hOrig.Write(bufOrig[:nOrig])
		hCand.Write(bufCand[:nCand])

		// Early-stop: compare accumulated SHA-256 after each chunk.
		if !hashesEqual(hOrig, hCand) {
			saveHashState(&orig, hOrig, nOrig)
			saveHashState(&cand, hCand, nCand)
			return orig, cand, nil
		}

		remaining -= int64(nOrig)
	}

	// End of file reached with matching hashes — SHA-256 is complete.
	finishHash(hOrig, &orig.SHA256)
	finishHash(hCand, &cand.SHA256)
	orig.HashOffset = orig.Size
	cand.HashOffset = cand.Size
	orig.HashState = nil // no longer needed, SHA-256 is complete
	cand.HashState = nil
	return orig, cand, nil
}

// execute performs the configured action on the duplicate file.
func (e *Executor) execute(ctx context.Context, group dupe.DupeGroup) (Result, error) {
	select {
	case <-ctx.Done():
		return ResultError, ctx.Err()
	default:
	}
	switch e.opts.Action {
	case config.ActionReport:
		return ResultVerifiedDuplicate, nil
	case config.ActionDelete:
		return e.deleteFile(group)
	case config.ActionHardlink:
		return e.createHardlink(group)
	case config.ActionCoWClone:
		return e.cloneFile(group)
	default:
		return ResultNotDuplicate, nil
	}
}

// restoreHasher creates a new SHA-256 hasher. If state is non-nil, it restores
// the hasher to the saved state (resuming incremental hashing).
func restoreHasher(state []byte) (hash.Hash, error) {
	h := sha256.New()
	if len(state) > 0 {
		if m, ok := h.(encoding.BinaryUnmarshaler); ok {
			if err := m.UnmarshalBinary(state); err != nil {
				return nil, err
			}
		}
	}
	return h, nil
}

// saveHashState marshals the hasher state and updates the FileInfo's hash progress.
func saveHashState(fi *dupe.FileInfo, h hash.Hash, bytesRead int) {
	if m, ok := h.(encoding.BinaryMarshaler); ok {
		state, err := m.MarshalBinary()
		if err == nil {
			fi.HashState = state
		}
	}
	fi.HashOffset += int64(bytesRead)
}

// hashesEqual compares the current accumulated hashes of two SHA-256 digests.
func hashesEqual(a, b hash.Hash) bool {
	return string(a.Sum(nil)) == string(b.Sum(nil))
}

// finishHash finalizes a hash.Hash into a [32]byte.
func finishHash(h hash.Hash, out *[32]byte) {
	h.Sum((*out)[:0])
}
