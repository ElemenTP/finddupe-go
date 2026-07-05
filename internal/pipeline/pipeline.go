// Package pipeline orchestrates the full duplicate detection and elimination process.
package pipeline

import (
	"context"
	"fmt"
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
	// Set up signal handling.
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	// Determine thread count.
	threads := cfg.Threads
	if threads <= 0 {
		threads = runtime.NumCPU()
	}

	// Set up logging.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	if cfg.Verbose {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
	}

	// Create components.
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

	// Create channels.
	walkResultCh := make(chan fswalker.Result, threads*4)
	fileCh := make(chan dupe.FileInfo, threads*4)
	groupCh := make(chan dupe.DupeGroup, threads)
	errCh := make(chan error, 1)

	// Start progress reporter if enabled.
	if cfg.ShowProgress {
		prog := progress.New(stats)
		go prog.Run(ctx)
	}

	// Start the filesystem walker.
	go func() {
		defer close(walkResultCh)
		opts := fswalker.WalkOptions{
			FollowSymlinks: cfg.FollowSymlinks,
			IncludeZeroLen: cfg.IncludeZeroLen,
			ZeroLen:        stats,
		}

		// Walk regular paths (not reference files).
		for result := range walker.Walk(ctx, cfg.Paths, opts) {
			select {
			case walkResultCh <- result:
			case <-ctx.Done():
				return
			}
		}

		// Walk reference paths — these files are compared against but never
		// acted upon (deleted, hardlinked, etc.).
		for result := range walker.Walk(ctx, cfg.RefPaths, opts) {
			result.Info.IsRef = true
			select {
			case walkResultCh <- result:
			case <-ctx.Done():
				return
			}
		}
	}()

	runNormalMode(ctx, cfg, walkResultCh, fileCh, groupCh, errCh,
		pool, stats, detector, executor, logger)

	// Wait for completion or error.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	// Print summary.
	printSummary(cfg, stats, detector)

	return nil
}

// runNormalMode runs the checksum-based duplicate detection pipeline.
// Checksums are computed in the worker pool for parallelism.
func runNormalMode(
	ctx context.Context,
	cfg *config.Config,
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
	// Scanner workers: compute checksums for walked files in the pool.
	// Inode info is also retrieved from the already-open file handle
	// (important on Windows where this requires a system call).
	go func() {
		var pending sync.WaitGroup
		defer func() {
			pending.Wait()
			close(fileCh)
		}()
		for result := range walkResultCh {
			if result.Err != nil {
				stats.CantReadFiles.Add(1)
				logger.Warn("cannot read file", "path", result.Info.Path, "error", result.Err)
				continue
			}

			fi := result.Info

			// Check ctx before incrementing pending to avoid deadlock:
			// if Submit returns false, the callback is never executed and
			// pending.Done would never be called.
			select {
			case <-ctx.Done():
				continue
			default:
			}

			pending.Add(1)

			if !pool.Submit(ctx, func(ctx context.Context) {
				defer pending.Done()

				// Open file once: compute checksum AND retrieve inode/link info.
				sig, inode, numLinks, err := checksum.ComputeFileInfo(fi.Path, fi.Size)
				if err != nil {
					stats.CantReadFiles.Add(1)
					logger.Warn("checksum failed", "path", fi.Path, "error", err)
					return
				}
				fi.Signature = sig
				fi.Inode = inode
				fi.NumLinks = numLinks

				select {
				case fileCh <- fi:
				case <-ctx.Done():
				}
			}) {
				pending.Done() // Submit declined due to cancellation.
			}
		}
	}()

	// Detector: insert files into the duplicate detector.
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
		}
	}()

	// Executor: verify and execute actions on duplicate groups.
	go func() {
		defer func() {
			errCh <- nil
			close(errCh)
		}()
		for group := range groupCh {
			result, err := executor.VerifyAndExecute(ctx, group)
			if err != nil {
				logger.Error("action failed",
					"original", group.Original.Path,
					"candidate", group.Candidate.Path,
					"error", err,
				)
				continue
			}

			// Update stats based on result.
			switch result {
			case action.ResultNotDuplicate:
				continue
			case action.ResultAlreadyHardlinked:
				// Already hardlinked files skipped via --hardlink flag.
				if cfg.Verbose {
					fmt.Fprintf(os.Stderr, "Already hardlinked: '%s' and '%s'\n",
						group.Original.Path, group.Candidate.Path)
				}
				continue
			case action.ResultError:
				logger.Error("action error", "candidate", group.Candidate.Path)
				continue
			case action.ResultHardlinkLimit:
				logger.Warn("hardlink limit reached",
					"original", group.Original.Path,
					"candidate", group.Candidate.Path,
				)
				continue
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
	}()
}

// printSummary outputs the final statistics.
func printSummary(cfg *config.Config, stats *dupe.Stats, detector *dupe.Detector) {
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

// formatSize formats a byte count for human display.
func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%d kB", bytes/1024)
	}
	return fmt.Sprintf("%d MB", bytes/(1024*1024))
}
