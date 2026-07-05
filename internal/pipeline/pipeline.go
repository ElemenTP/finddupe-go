// Package pipeline orchestrates the full duplicate detection and elimination process.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sync"

	"finddupe/internal/action"
	"finddupe/internal/checksum"
	"finddupe/internal/config"
	"finddupe/internal/dupe"
	"finddupe/internal/fswalker"
	"finddupe/internal/progress"
	"finddupe/internal/worker"
)

// Run executes the full duplicate detection pipeline.
func Run(ctx context.Context, cfg *config.Config) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	threads := cfg.Threads
	if threads <= 0 {
		threads = runtime.NumCPU() * 2
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	if cfg.Verbose {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
	}

	stats := dupe.NewStats()
	detector := dupe.NewDetector(stats)
	walker := fswalker.New()
	pool := worker.New(threads)

	execOpts := action.Options{
		Action:          cfg.Action,
		IncludeReadonly: cfg.IncludeReadonly,
		SkipHardlinked:  cfg.SkipHardlinked,
	}
	executor := action.New(execOpts)

	walkResultCh := make(chan fswalker.Result, threads*4)
	fileCh := make(chan dupe.FileInfo, threads*4)
	groupCh := make(chan dupe.DupeGroup, threads)
	errCh := make(chan error, 1)

	if cfg.ShowProgress {
		prog := progress.New(stats)
		go prog.Run(ctx)
	}

	// Filesystem walker.
	go func() {
		defer close(walkResultCh)
		opts := fswalker.WalkOptions{
			FollowSymlinks: cfg.FollowSymlinks,
			IncludeZeroLen: cfg.IncludeZeroLen,
			ZeroLen:        stats,
		}
		for result := range walker.Walk(ctx, cfg.Paths, opts) {
			select {
			case walkResultCh <- result:
			case <-ctx.Done():
				return
			}
		}
		for result := range walker.Walk(ctx, cfg.RefPaths, opts) {
			result.Info.IsRef = true
			select {
			case walkResultCh <- result:
			case <-ctx.Done():
				return
			}
		}
	}()

	runNormalMode(ctx, walkResultCh, fileCh, groupCh, errCh,
		pool, stats, detector, executor, logger)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	printSummary(stats)

	return nil
}

func runNormalMode(
	ctx context.Context,
	walkResultCh <-chan fswalker.Result,
	fileCh chan dupe.FileInfo,
	groupCh chan dupe.DupeGroup,
	errCh chan<- error,
	pool *worker.Pool,
	stats *dupe.Stats,
	detector *dupe.Detector,
	executor *action.Executor,
	logger *slog.Logger,
) {
	// Scanner: compute CRC (+ SHA-256 for ≤32KB files) in the worker pool.
	go func() {
		var pending sync.WaitGroup
		defer func() {
			pending.Wait()
			close(fileCh)
		}()
		for result := range walkResultCh {
			if result.Err != nil {
				stats.CantReadFiles.Add(1)
				logger.WarnContext(ctx, "cannot read file", "path", result.Info.Path, "error", result.Err)
				continue
			}

			fi := result.Info

			select {
			case <-ctx.Done():
				continue
			default:
			}

			pending.Add(1)

			if !pool.Submit(ctx, func(ctx context.Context) {
				defer pending.Done()

				sig, inode, numLinks, sha256sum, err := checksum.ComputeFileInfo(fi.Path, fi.Size)
				if err != nil {
					stats.CantReadFiles.Add(1)
					logger.WarnContext(ctx, "checksum failed", "path", fi.Path, "error", err)
					return
				}
				fi.Signature = sig
				fi.Inode = inode
				fi.NumLinks = numLinks
				fi.SHA256 = sha256sum

				select {
				case fileCh <- fi:
				case <-ctx.Done():
				}
			}) {
				pending.Done()
			}
		}
	}()

	// Detector: insert files, emit DupeGroups, trigger mass SHA-256 for 3+ groups.
	go func() {
		defer close(groupCh)
		for fi := range fileCh {
			groups := detector.Insert(fi)
			for _, g := range groups {
				select {
				case groupCh <- g:
				case <-ctx.Done():
					return
				}
			}

			// Strategy 4: if group has 3+ files, submit mass SHA-256 computation
			// for all files in the zero-SHA bucket. These run concurrently in the
			// pool. Completed hashes are recorded via detector.UpdateFileState.
			key := dupe.GroupKey{Signature: fi.Signature, Size: fi.Size}
			if detector.GroupSize(key) >= 3 {
				for _, uf := range detector.UnhashedFiles(key) {
					f := uf
					pool.Submit(ctx, func(ctx context.Context) {
						computeFullSHA(ctx, &f, detector)
					})
				}
			}
		}
	}()

	// Executor: verify DupeGroups and execute actions, all in the worker pool.
	go func() {
		var verifyWg sync.WaitGroup
		defer func() {
			verifyWg.Wait()
			errCh <- nil
			close(errCh)
		}()
		for group := range groupCh {
			g := group
			verifyWg.Add(1)

			if !pool.Submit(ctx, func(ctx context.Context) {
				defer verifyWg.Done()
				handleDupeGroup(ctx, g, executor, detector, stats, logger)
			}) {
				verifyWg.Done()
			}
		}
	}()
}

// handleDupeGroup verifies a DupeGroup and executes the configured action.
// Called from the worker pool.
func handleDupeGroup(
	ctx context.Context,
	group dupe.DupeGroup,
	executor *action.Executor,
	detector *dupe.Detector,
	stats *dupe.Stats,
	logger *slog.Logger,
) {
	result, updatedOrig, updatedCand, err := executor.VerifyChunked(ctx, group)
	if err != nil {
		logger.ErrorContext(ctx, "action failed",
			"original", group.Original.Path,
			"candidate", group.Candidate.Path,
			"error", err,
		)
		return
	}

	// Persist hash progress so future comparisons can resume from the saved state.
	detector.UpdateFileState(group.Key, updatedOrig.Path,
		updatedOrig.HashState, updatedOrig.HashOffset, updatedOrig.SHA256)
	detector.UpdateFileState(group.Key, updatedCand.Path,
		updatedCand.HashState, updatedCand.HashOffset, updatedCand.SHA256)

	switch result {
	case action.ResultNotDuplicate:
		return
	case action.ResultAlreadyHardlinked:
		return
	case action.ResultError:
		logger.ErrorContext(ctx, "action error", "candidate", group.Candidate.Path)
		return
	case action.ResultHardlinkLimit:
		logger.WarnContext(ctx, "hardlink limit reached",
			"original", group.Original.Path,
			"candidate", group.Candidate.Path,
		)
		return
	case action.ResultVerifiedDuplicate:
		fmt.Fprintf(os.Stderr, "Duplicate: '%s'\n", group.Original.Path)
		fmt.Fprintf(os.Stderr, "With:      '%s'\n", group.Candidate.Path)
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(group.Candidate.Size)
	case action.ResultDeleted:
		fmt.Fprintf(os.Stderr, "Deleted:    '%s'\n", group.Candidate.Path)
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(group.Candidate.Size)
		stats.DeletedFiles.Add(1)
	case action.ResultHardlinked:
		fmt.Fprintf(os.Stderr, "Hardlinked: '%s'\n", group.Candidate.Path)
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(group.Candidate.Size)
		stats.HardlinkedFiles.Add(1)
	case action.ResultCoWCloned:
		fmt.Fprintf(os.Stderr, "CoW cloned: '%s'\n", group.Candidate.Path)
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(group.Candidate.Size)
		stats.CoWClonedFiles.Add(1)
	case action.ResultSkippedRO:
		fmt.Fprintf(os.Stderr, "Skipping duplicate readonly file '%s'.\n", group.Candidate.Path)
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(group.Candidate.Size)
		stats.SkippedROFiles.Add(1)
	case action.ResultSkippedRef:
		stats.SkippedRefFiles.Add(1)
	}
}

// computeFullSHA reads the remainder of a file and computes its complete SHA-256.
// If the file has partial hash state, hashing resumes from the saved offset.
// The completed hash is recorded via detector.UpdateFileState.
func computeFullSHA(ctx context.Context, fi *dupe.FileInfo, detector *dupe.Detector) {
	f, err := os.Open(fi.Path)
	if err != nil {
		return
	}
	defer f.Close()

	h := sha256.New()
	if len(fi.HashState) > 0 {
		if m, ok := h.(encoding.BinaryUnmarshaler); ok {
			if err := m.UnmarshalBinary(fi.HashState); err != nil {
				return
			}
		}
	}

	if _, err := f.Seek(fi.HashOffset, io.SeekStart); err != nil {
		return
	}

	remaining := fi.Size - fi.HashOffset
	buf := make([]byte, 1024*1024) // 1 MB buffer
	for remaining > 0 {
		select {
		case <-ctx.Done():
			return
		default:
		}
		toRead := int64(len(buf))
		if toRead > remaining {
			toRead = remaining
		}
		n, _ := io.ReadFull(f, buf[:toRead])
		h.Write(buf[:n])
		remaining -= int64(n)
	}

	var sha256sum [32]byte
	h.Sum(sha256sum[:0])

	key := dupe.GroupKey{Signature: fi.Signature, Size: fi.Size}
	detector.UpdateFileState(key, fi.Path, nil, fi.Size, sha256sum)
}

// printSummary outputs the final statistics.
func printSummary(stats *dupe.Stats) {
	totalFiles := stats.TotalFiles.Load()
	totalBytes := stats.TotalBytes.Load()

	dupFiles := stats.DuplicateFiles.Load()
	dupBytes := stats.DuplicateBytes.Load()

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "Files: %8s in %5d files\n",
		formatSize(totalBytes), totalFiles)
	fmt.Fprintf(os.Stderr, "Dupes: %8s in %5d files\n",
		formatSize(dupBytes), dupFiles)

	if n := stats.ZeroLengthFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d files of zero length were skipped\n", n)
	}
	if n := stats.CantReadFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d files could not be opened\n", n)
	}
	if n := stats.DeletedFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d files deleted\n", n)
	}
	if n := stats.HardlinkedFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d files replaced with hardlinks\n", n)
	}
	if n := stats.CoWClonedFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d files replaced with CoW clones\n", n)
	}
	if n := stats.SkippedROFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d read-only files skipped\n", n)
	}
	if n := stats.SkippedRefFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d reference files skipped\n", n)
	}
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%d kB", bytes/1024)
	}
	return fmt.Sprintf("%d MB", bytes/(1024*1024))
}
