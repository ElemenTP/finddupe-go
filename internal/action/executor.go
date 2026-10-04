// Package action performs duplicate file verification and elimination.
package action

import (
	"context"
	"crypto/sha256"
	"encoding"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"

	"finddupe/internal/config"
	"finddupe/internal/dupe"
)

// MaxHardlinks is the maximum number of hardlinks per file on Windows NTFS.
const MaxHardlinks = 1023

const (
	// chunkSizeSmall is used for medium files to make early-stop cheap.
	chunkSizeSmall = 64 * 1024

	// chunkSizeLarge is used for big files to reduce syscall overhead.
	chunkSizeLarge = 1024 * 1024

	// chunkSizeMediumLimit is the largest file that uses chunkSizeSmall.
	chunkSizeMediumLimit = 16 * 1024 * 1024

	// minFilesPerExecution is the number of files a comparison or elimination
	// execution requires.
	minFilesPerExecution = 2
)

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

	// ResultAlreadyShared means the keeper and victim already share all their
	// storage (same physical file, or identical CoW extents), so no action was
	// needed.
	ResultAlreadyShared
)

// Options configures the executor's behavior.
type Options struct {
	Action          config.Action
	IncludeReadonly bool
	SkipHardlinked  bool
}

// Executor executes the work items the detector produces.
type Executor struct {
	opts Options
}

// New creates a new Executor.
func New(opts Options) *Executor {
	return &Executor{opts: opts}
}

// Outcome is the result of a single Execution. The detector consumes it to
// decide whether further work is required.
type Outcome struct {
	// Kind mirrors the executed Execution's type.
	Kind dupe.ExecutionType

	// Key is the composite key the execution belonged to.
	Key dupe.GroupKey

	// Files carries the updated FileInfo records (hash progress or final hash
	// for HashCalc/HashComp, the participating files for DupeElim/CoWDetect).
	Files []dupe.FileInfo

	// Result is set for DupeElim outcomes.
	Result Result

	// FileShared is set for CoWDetect outcomes: the number of already-shared
	// bytes for each entry of Files. It is nil when extent information is not
	// available for the group (unsupported filesystem).
	FileShared []int64
}

// chunkSizeFor returns an appropriate I/O chunk size for a file of the given size.
// Small files are read in one chunk; medium files use 64 KB chunks for fast
// early-stop; large files use 1 MB chunks to reduce syscall overhead.
func chunkSizeFor(fileSize int64) int64 {
	switch {
	case fileSize <= chunkSizeSmall:
		return fileSize // one read
	case fileSize <= chunkSizeMediumLimit:
		return chunkSizeSmall
	default:
		return chunkSizeLarge
	}
}

// hashBufferSize returns the read buffer size for a full-file hash.
func hashBufferSize(fileSize int64) int64 {
	if fileSize <= chunkSizeLarge {
		return chunkSizeSmall
	}
	return chunkSizeLarge
}

// DoExecution runs a single Execution and reports its outcome.
func (e *Executor) DoExecution(ctx context.Context, ex dupe.Execution) (Outcome, error) {
	select {
	case <-ctx.Done():
		return Outcome{Kind: ex.Type, Key: ex.Key}, ctx.Err()
	default:
	}

	switch ex.Type {
	case dupe.HashCalc:
		if len(ex.Files) < 1 {
			return Outcome{Kind: ex.Type, Key: ex.Key}, errors.New("HashCalc requires one file")
		}
		fi, err := e.hashCalc(ctx, ex.Files[0])
		return Outcome{Kind: ex.Type, Key: ex.Key, Files: []dupe.FileInfo{fi}}, err
	case dupe.HashComp:
		if len(ex.Files) < minFilesPerExecution {
			return Outcome{Kind: ex.Type, Key: ex.Key}, errors.New("HashComp requires two files")
		}
		a, b, err := hashCompare(ctx, ex.Files[0], ex.Files[1], chunkSizeFor(ex.Files[0].Size))
		return Outcome{Kind: ex.Type, Key: ex.Key, Files: []dupe.FileInfo{a, b}}, err
	case dupe.DupeElim:
		result, err := e.execute(ctx, ex)
		return Outcome{Kind: ex.Type, Key: ex.Key, Files: ex.Files, Result: result}, err
	case dupe.CoWDetect:
		return e.detectCoW(ctx, ex)
	default:
		return Outcome{Kind: ex.Type, Key: ex.Key}, fmt.Errorf("unknown execution type %s", ex.Type)
	}
}

// hashCalc computes the full SHA-256 of a file, resuming from saved state when
// a previous run stopped early. On a read error the partial state is preserved
// so a later attempt can continue instead of re-reading from the start.
func (e *Executor) hashCalc(ctx context.Context, fi dupe.FileInfo) (dupe.FileInfo, error) {
	f, err := os.Open(fi.Path)
	if err != nil {
		return fi, err
	}
	defer f.Close()

	h, err := restoreHasher(fi.HashState)
	if err != nil {
		return fi, err
	}

	if fi.HashOffset > 0 {
		if _, seekErr := f.Seek(fi.HashOffset, io.SeekStart); seekErr != nil {
			return fi, seekErr
		}
	}

	buf := make([]byte, hashBufferSize(fi.Size))
	remaining := fi.Size - fi.HashOffset

	for remaining > 0 {
		select {
		case <-ctx.Done():
			storeHashState(&fi, h)
			return fi, ctx.Err()
		default:
		}

		toRead := min(int64(len(buf)), remaining)

		n, readErr := io.ReadFull(f, buf[:toRead])
		if n > 0 {
			h.Write(buf[:n])
			fi.HashOffset += int64(n)
			remaining -= int64(n)
		}

		if readErr != nil {
			storeHashState(&fi, h)
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
				// The file shrank: do not claim a complete hash.
				return fi, nil
			}
			return fi, readErr
		}
	}

	finishHash(h, &fi.SHA256)
	fi.HashState = nil
	fi.HashOffset = fi.Size
	return fi, nil
}

// hashCompare reads two files chunk by chunk, updating SHA-256 hashers and
// comparing accumulated hashes after each chunk for early-stop. Saved hash state
// is resumed if available.
func hashCompare(
	ctx context.Context, orig, cand dupe.FileInfo, chunkSize int64,
) (dupe.FileInfo, dupe.FileInfo, error) {
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

	hOrig, err := restoreHasher(orig.HashState)
	if err != nil {
		return orig, cand, err
	}
	hCand, err := restoreHasher(cand.HashState)
	if err != nil {
		return orig, cand, err
	}

	if _, seekErr := fOrig.Seek(orig.HashOffset, io.SeekStart); seekErr != nil {
		return orig, cand, seekErr
	}
	if _, seekErr := fCand.Seek(cand.HashOffset, io.SeekStart); seekErr != nil {
		return orig, cand, seekErr
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

		toRead := min(chunkSize, remaining)

		nOrig, _ := io.ReadFull(fOrig, bufOrig[:toRead])
		nCand, _ := io.ReadFull(fCand, bufCand[:toRead])

		if nOrig == 0 || nCand == 0 || nOrig != nCand {
			// Truncated file(s) — not a duplicate. Keep whatever was read.
			orig.HashOffset += int64(nOrig)
			cand.HashOffset += int64(nCand)
			storeHashState(&orig, hOrig)
			storeHashState(&cand, hCand)
			return orig, cand, nil
		}

		hOrig.Write(bufOrig[:nOrig])
		hCand.Write(bufCand[:nCand])

		if !hashesEqual(hOrig, hCand) {
			orig.HashOffset += int64(nOrig)
			cand.HashOffset += int64(nCand)
			storeHashState(&orig, hOrig)
			storeHashState(&cand, hCand)
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
func (e *Executor) execute(ctx context.Context, ex dupe.Execution) (Result, error) {
	select {
	case <-ctx.Done():
		return ResultError, ctx.Err()
	default:
	}

	if len(ex.Files) < minFilesPerExecution {
		return ResultError, errors.New("elimination requires a keeper and a victim")
	}

	keeper := ex.Files[0]
	victim := ex.Files[1]

	// Never act on the same physical file. In report mode a hardlinked pair is
	// still a duplicate worth reporting unless --hardlink asked to skip it.
	if samePhysicalFile(keeper, victim) {
		if e.opts.Action != config.ActionReport || e.opts.SkipHardlinked {
			return ResultAlreadyHardlinked, nil
		}
		return ResultVerifiedDuplicate, nil
	}

	if victim.IsRef && e.opts.Action != config.ActionReport {
		return ResultSkippedRef, nil
	}

	switch e.opts.Action {
	case config.ActionReport:
		return ResultVerifiedDuplicate, nil
	case config.ActionDelete:
		return e.deleteFile(ex)
	case config.ActionHardlink:
		return e.createHardlink(ex)
	case config.ActionCoWClone:
		return e.cloneFile(ex)
	default:
		return ResultNotDuplicate, nil
	}
}

// samePhysicalFile reports whether two FileInfo records describe the same
// physical file (a hardlink of each other, or the same path).
func samePhysicalFile(a, b dupe.FileInfo) bool {
	return a.Inode != 0 && a.Dev == b.Dev && a.Inode == b.Inode
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

// storeHashState marshals the hasher's state into the FileInfo so hashing can
// resume later. It does not touch HashOffset (callers advance it themselves).
func storeHashState(fi *dupe.FileInfo, h hash.Hash) {
	if m, ok := h.(encoding.BinaryMarshaler); ok {
		if state, err := m.MarshalBinary(); err == nil {
			fi.HashState = state
		}
	}
}

// hashesEqual compares the current accumulated hashes of two SHA-256 digests.
func hashesEqual(a, b hash.Hash) bool {
	return string(a.Sum(nil)) == string(b.Sum(nil))
}

// finishHash finalizes a [hash.Hash] into a [32]byte.
func finishHash(h hash.Hash, out *[32]byte) {
	h.Sum((*out)[:0])
}
