// Package action performs duplicate file verification and elimination.
package action

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"finddupe/internal/config"
	"finddupe/internal/dupe"
)

// chunkSize is the buffer size for full file verification.
const chunkSize = 0x10000 // 64KB, matching C version's CHUNK_SIZE

// MaxHardlinks is the maximum number of hardlinks per file on Windows NTFS.
const MaxHardlinks = 1023

// Result describes the outcome of processing a duplicate file.
type Result int

const (
	// ResultVerifiedDuplicate means files are confirmed duplicates and the action succeeded.
	// In find mode (ActionReport), this is the final result after verification.
	ResultVerifiedDuplicate Result = iota
	// ResultAlreadyHardlinked means the files share the same inode (already hardlinked)
	// and were skipped due to --hardlink flag in find mode.
	ResultAlreadyHardlinked
	// ResultDeleted means the duplicate file was deleted.
	ResultDeleted
	// ResultHardlinked means the duplicate was replaced with a hardlink.
	ResultHardlinked
	// ResultCoWCloned means the duplicate was replaced with a CoW clone.
	ResultCoWCloned
	// ResultSkippedRO means the file was skipped because it is read-only.
	ResultSkippedRO
	// ResultSkippedRef means the file was skipped because it is a reference.
	ResultSkippedRef
	// ResultHardlinkLimit means linking was skipped due to the NTFS link limit.
	ResultHardlinkLimit
	// ResultNotDuplicate means the files differ on full comparison (CRC collision).
	ResultNotDuplicate
	// ResultError means the action failed with an error.
	ResultError
)

// Options configures the executor's behavior.
type Options struct {
	Action          config.Action
	IncludeReadonly bool
	// SkipHardlinked skips duplicate pairs that are already hardlinked (same inode).
	SkipHardlinked bool
}

// Executor verifies and eliminates duplicate files.
type Executor struct {
	opts Options
}

// New creates a new Executor.
func New(opts Options) *Executor {
	return &Executor{opts: opts}
}

// VerifyAndExecute performs a full byte-by-byte comparison between the candidate
// and the original file. If confirmed, it executes the configured action.
func (e *Executor) VerifyAndExecute(ctx context.Context, group dupe.DupeGroup) (Result, error) {
	select {
	case <-ctx.Done():
		return ResultError, ctx.Err()
	default:
	}

	// If SkipHardlinked is set and both files share the same inode (already
	// hardlinked to each other), skip them — they're the same physical file.
	if e.opts.SkipHardlinked && group.Original.Inode != 0 &&
		group.Original.Inode == group.Candidate.Inode &&
		group.Original.NumLinks > 1 {
		return ResultAlreadyHardlinked, nil
	}

	// Verify full file content.
	isDuplicate, err := VerifyFullFile(group.Candidate.Path, group.Original.Path, group.Candidate.Size)
	if err != nil {
		return ResultError, fmt.Errorf("verify %s: %w", group.Candidate.Path, err)
	}
	if !isDuplicate {
		return ResultNotDuplicate, nil
	}

	// If candidate is a reference file, skip it.
	if group.Candidate.IsRef {
		return ResultSkippedRef, nil
	}

	// Execute the configured action (files are confirmed duplicates at this point).
	return e.execute(ctx, group)
}

// execute performs the configured action on the duplicate file.
// The files have already been confirmed as duplicates.
func (e *Executor) execute(ctx context.Context, group dupe.DupeGroup) (Result, error) {
	select {
	case <-ctx.Done():
		return ResultError, ctx.Err()
	default:
	}
	switch e.opts.Action {
	case config.ActionReport:
		return ResultVerifiedDuplicate, nil // Confirmed duplicate, no action taken.
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

// VerifyFullFile performs a byte-by-byte comparison of two files.
// Returns true if the files are identical.
func VerifyFullFile(pathA, pathB string, expectedSize int64) (bool, error) {
	// Quick check: sizes must match.
	infoA, err := os.Stat(pathA)
	if err != nil {
		return false, err
	}
	infoB, err := os.Stat(pathB)
	if err != nil {
		return false, err
	}
	if infoA.Size() != infoB.Size() {
		return false, nil
	}

	// Check if they are the same file (same inode).
	if os.SameFile(infoA, infoB) {
		return true, nil
	}

	// Reset expectedSize to actual size if mismatch.
	if expectedSize != infoA.Size() {
		expectedSize = infoA.Size()
	}

	return compareFiles(pathA, pathB, expectedSize)
}

// compareFiles reads both files in chunks and compares each chunk.
func compareFiles(pathA, pathB string, size int64) (bool, error) {
	fA, err := os.Open(pathA)
	if err != nil {
		return false, err
	}
	defer fA.Close()

	fB, err := os.Open(pathB)
	if err != nil {
		return false, err
	}
	defer fB.Close()

	bufA := make([]byte, chunkSize)
	bufB := make([]byte, chunkSize)

	remaining := size
	for remaining > 0 {
		toRead := chunkSize
		if int64(toRead) > remaining {
			toRead = int(remaining)
		}

		nA, errA := fA.Read(bufA[:toRead])
		nB, errB := fB.Read(bufB[:toRead])

		if nA != nB || !bytes.Equal(bufA[:nA], bufB[:nB]) {
			return false, nil
		}

		// If either read had an unexpected EOF, treat as mismatch.
		if (errA != nil && nA < toRead) || (errB != nil && nB < toRead) {
			return false, nil
		}

		remaining -= int64(toRead)
	}

	return true, nil
}
