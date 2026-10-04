// Package pipeline orchestrates the full duplicate detection and elimination process.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"sync"

	"finddupe/internal/action"
	"finddupe/internal/checksum"
	"finddupe/internal/config"
	"finddupe/internal/dupe"
	"finddupe/internal/fswalker"
	"finddupe/internal/progress"
	"finddupe/internal/worker"
)

const (
	// channelBufferFactor scales pipeline channel buffers with the worker count.
	channelBufferFactor = 4

	// defaultThreadsPerCPU is the worker multiplier used when --threads is omitted.
	defaultThreadsPerCPU = 2

	// minCompareFiles is the number of files a comparison/elimination execution needs.
	minCompareFiles = 2

	// Byte units used when formatting sizes.
	bytesPerKB = 1024
	bytesPerMB = 1024 * 1024
)

// Run executes the full duplicate detection pipeline.
func Run(ctx context.Context, cfg *config.Config) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	threads := resolveThreads(cfg.Threads)

	logger := newLogger(cfg)

	stats := dupe.NewStats()
	if cfg.ShowProgress {
		prog := progress.New(stats)
		go prog.Run(ctx)
	}

	if cfg.ListLink {
		return runListLink(ctx, cfg, stats, logger)
	}

	walkResultCh := make(chan fswalker.Result, threads*channelBufferFactor)
	fileInfoCh := make(chan dupe.FileInfo, threads*channelBufferFactor)
	executionCh := make(chan dupe.Execution, threads*channelBufferFactor)
	outcomeCh := make(chan action.Outcome, threads*channelBufferFactor)

	pool := worker.New(threads)
	defer pool.Wait()

	detector := dupe.NewDetector(stats, detectorOptions(cfg)...)
	executor := action.New(action.Options{
		Action:          cfg.Action,
		IncludeReadonly: cfg.IncludeReadonly,
		SkipHardlinked:  cfg.SkipHardlinked,
	})

	// Filesystem walker → walkResultCh.
	go walkAll(ctx, cfg, stats, walkResultCh)

	// Checksum workers → fileInfoCh.
	go scanChecksums(ctx, stats, logger, pool, walkResultCh, fileInfoCh, false)

	// Executor workers: executionCh → outcomeCh.
	var executors sync.WaitGroup
	for range threads {
		executors.Go(func() {
			runExecutor(ctx, executor, logger, executionCh, outcomeCh)
		})
	}
	go func() {
		executors.Wait()
		close(outcomeCh)
	}()

	// The coordinator owns the detector and decides when work is finished.
	err := coordinate(ctx, detector, stats, logger, fileInfoCh, executionCh, outcomeCh)

	printSummary(stats)
	return err
}

// resolveThreads applies the default worker count when none is configured.
func resolveThreads(threads int) int {
	if threads <= 0 {
		return runtime.NumCPU() * defaultThreadsPerCPU
	}
	return threads
}

// newLogger builds the stderr logger for the configured verbosity.
func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelWarn
	if cfg.Verbose {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// detectorOptions translates the config into detector options.
func detectorOptions(cfg *config.Config) []dupe.Option {
	if cfg.CoWDetect {
		return []dupe.Option{dupe.WithCoWDetect()}
	}
	return nil
}

// walkAll walks cfg.RefPaths first and then cfg.Paths, sending every result on
// ch and closing ch when done. Reference files are visited first so they become
// the "keeper" of their content group and are never proposed as victims; they
// are also flagged so the action layer never eliminates them.
func walkAll(ctx context.Context, cfg *config.Config, stats *dupe.Stats, ch chan<- fswalker.Result) {
	defer close(ch)

	walker := fswalker.New()
	opts := fswalker.WalkOptions{
		FollowSymlinks: cfg.FollowSymlinks,
		IncludeZeroLen: cfg.IncludeZeroLen,
		ZeroLen:        stats,
	}

	for result := range walker.Walk(ctx, cfg.RefPaths, opts) {
		result.Info.IsRef = true
		select {
		case ch <- result:
		case <-ctx.Done():
			return
		}
	}

	for result := range walker.Walk(ctx, cfg.Paths, opts) {
		select {
		case ch <- result:
		case <-ctx.Done():
			return
		}
	}
}

// scanChecksums consumes walk results, computes file identity/checksums in the
// worker pool, and forwards completed FileInfo records on fileCh.
//
// When inodeOnly is set the checksum can be skipped for files whose identity
// the walker already provided (Unix), which is all --listlink needs.
func scanChecksums(
	ctx context.Context,
	stats *dupe.Stats,
	logger *slog.Logger,
	pool *worker.Pool,
	walkResultCh <-chan fswalker.Result,
	fileCh chan<- dupe.FileInfo,
	inodeOnly bool,
) {
	defer close(fileCh)

	var pending sync.WaitGroup

	// seenPaths guards against overlapping patterns producing the same file
	// more than once, which would otherwise let a file be eliminated against
	// itself.
	seenPaths := make(map[string]struct{})

	// Reference files are walked first, but their checksums are computed in
	// parallel. Drain the whole reference phase before submitting any normal
	// file so reference files are always inserted (and therefore chosen as the
	// keeper) before their non-reference duplicates.
	refPhase := true

	for result := range walkResultCh {
		select {
		case <-ctx.Done():
			continue
		default:
		}

		if result.Err != nil {
			stats.CantReadFiles.Add(1)
			logger.WarnContext(ctx, "cannot read file", "path", result.Info.Path, "error", result.Err)
			continue
		}

		fi := result.Info

		if refPhase && !fi.IsRef {
			pending.Wait()
			refPhase = false
		}

		if _, ok := seenPaths[fi.Path]; ok {
			continue
		}
		seenPaths[fi.Path] = struct{}{}

		if inodeOnly && fi.Inode != 0 {
			select {
			case fileCh <- fi:
			case <-ctx.Done():
			}
			continue
		}

		submitChecksum(ctx, pool, &pending, stats, logger, fi, fileCh)
	}

	pending.Wait()
}

// submitChecksum queues one checksum task in the worker pool and forwards the
// completed metadata on fileCh.
func submitChecksum(
	ctx context.Context,
	pool *worker.Pool,
	pending *sync.WaitGroup,
	stats *dupe.Stats,
	logger *slog.Logger,
	fi dupe.FileInfo,
	fileCh chan<- dupe.FileInfo,
) {
	pending.Add(1)

	if !pool.Submit(ctx, func(ctx context.Context) {
		defer pending.Done()

		info, err := checksum.ComputeFileInfo(fi.Path, fi.Size)
		if err != nil {
			stats.CantReadFiles.Add(1)
			logger.WarnContext(ctx, "checksum failed", "path", fi.Path, "error", err)
			return
		}

		fi.Signature = info.Signature
		fi.Dev = info.Dev
		fi.Inode = info.Inode
		fi.NumLinks = info.NumLinks
		fi.SHA256 = info.SHA256

		select {
		case fileCh <- fi:
		case <-ctx.Done():
		}
	}) {
		pending.Done()
	}
}

// coordinator owns all detector state while the pipeline runs. It feeds
// insertions and executor outcomes to the detector, dispatches the resulting
// executions, and closes the execution channel once no work remains.
type coordinator struct {
	ctx         context.Context
	detector    *dupe.Detector
	stats       *dupe.Stats
	logger      *slog.Logger
	executionCh chan<- dupe.Execution
	inFlight    int
	execClosed  bool
}

// coordinate runs the coordinator loop until the input and all executor
// outcomes are drained.
//
//nolint:gocognit // the coordinator is an explicit state machine over two channels
func coordinate(
	ctx context.Context,
	detector *dupe.Detector,
	stats *dupe.Stats,
	logger *slog.Logger,
	fileInfoCh <-chan dupe.FileInfo,
	executionCh chan<- dupe.Execution,
	outcomeCh <-chan action.Outcome,
) error {
	c := &coordinator{
		ctx:         ctx,
		detector:    detector,
		stats:       stats,
		logger:      logger,
		executionCh: executionCh,
	}
	defer c.closeExec()

	fiCh := fileInfoCh
	outCh := outcomeCh

	for fiCh != nil || outCh != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case fi, ok := <-fiCh:
			if !ok {
				fiCh = nil
				if c.inFlight == 0 {
					c.closeExec()
				}
				continue
			}
			if !c.insert(fi) {
				return ctx.Err()
			}

		case out, ok := <-outCh:
			if !ok {
				outCh = nil
				continue
			}
			if !c.complete(out) {
				return ctx.Err()
			}
			if fiCh == nil && c.inFlight == 0 {
				c.closeExec()
			}
		}
	}

	return nil
}

// insert feeds a scanned file to the detector and dispatches the work it
// produces. It reports false when the context is cancelled.
func (c *coordinator) insert(fi dupe.FileInfo) bool {
	for _, ex := range c.detector.Insert(fi) {
		if !c.dispatch(ex) {
			return false
		}
	}
	return true
}

// complete reports an executor outcome, feeds it back to the detector and
// dispatches the follow-up work. It reports false when the context is cancelled.
func (c *coordinator) complete(out action.Outcome) bool {
	reportOutcome(c.ctx, out, c.stats, c.logger)

	for _, ex := range completeExecution(c.detector, out) {
		if !c.dispatch(ex) {
			return false
		}
	}
	c.inFlight--
	return true
}

// dispatch sends one execution and counts it as in flight.
func (c *coordinator) dispatch(ex dupe.Execution) bool {
	select {
	case c.executionCh <- ex:
		c.inFlight++
		return true
	case <-c.ctx.Done():
		return false
	}
}

// closeExec closes the execution channel exactly once.
func (c *coordinator) closeExec() {
	if !c.execClosed {
		close(c.executionCh)
		c.execClosed = true
	}
}

// completeExecution feeds an executor outcome back into the detector and
// returns the follow-up work it produced.
func completeExecution(detector *dupe.Detector, out action.Outcome) []dupe.Execution {
	switch out.Kind {
	case dupe.HashCalc:
		if len(out.Files) < 1 {
			return nil
		}
		return detector.OnHashDone(out.Key, out.Files[0])
	case dupe.HashComp:
		if len(out.Files) < minCompareFiles {
			return nil
		}
		return detector.OnCompareDone(out.Key, out.Files[0], out.Files[1])
	case dupe.DupeElim, dupe.CoWDetect:
		return nil
	default:
		return nil
	}
}

// runExecutor executes work items until the execution channel closes or the
// context is cancelled.
func runExecutor(
	ctx context.Context,
	executor *action.Executor,
	logger *slog.Logger,
	executionCh <-chan dupe.Execution,
	outcomeCh chan<- action.Outcome,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case ex, ok := <-executionCh:
			if !ok {
				return
			}

			out, err := executor.DoExecution(ctx, ex)
			if err != nil && ctx.Err() == nil && ex.Type != dupe.DupeElim {
				// DupeElim errors are reported by reportElimination; hashing
				// errors are only logged here.
				logger.WarnContext(ctx, "execution failed",
					"type", ex.Type.String(), "error", err)
			}

			select {
			case outcomeCh <- out:
			case <-ctx.Done():
				return
			}
		}
	}
}

// reportOutcome prints results and updates statistics for a finished execution.
func reportOutcome(ctx context.Context, out action.Outcome, stats *dupe.Stats, logger *slog.Logger) {
	switch out.Kind {
	case dupe.DupeElim:
		reportElimination(ctx, out, stats, logger)
	case dupe.CoWDetect:
		reportCoW(out, stats)
	case dupe.HashCalc, dupe.HashComp:
		// No user-visible result.
	}
}

// reportElimination handles the reported result of a DupeElim execution.
func reportElimination(ctx context.Context, out action.Outcome, stats *dupe.Stats, logger *slog.Logger) {
	if len(out.Files) < minCompareFiles {
		return
	}

	keeper := out.Files[0]
	victim := out.Files[1]

	switch out.Result {
	case action.ResultVerifiedDuplicate:
		fmt.Fprintf(os.Stderr, "Duplicate: '%s'\n", keeper.Path)
		fmt.Fprintf(os.Stderr, "With:      '%s'\n", victim.Path)
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(victim.Size)
	case action.ResultDeleted:
		fmt.Fprintf(os.Stderr, "Deleted:    '%s'\n", victim.Path)
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(victim.Size)
		stats.DeletedFiles.Add(1)
	case action.ResultHardlinked:
		fmt.Fprintf(os.Stderr, "Hardlinked: '%s'\n", victim.Path)
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(victim.Size)
		stats.HardlinkedFiles.Add(1)
	case action.ResultCoWCloned:
		fmt.Fprintf(os.Stderr, "CoW cloned: '%s'\n", victim.Path)
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(victim.Size)
		stats.CoWClonedFiles.Add(1)
	case action.ResultSkippedRO:
		fmt.Fprintf(os.Stderr, "Skipping duplicate readonly file '%s'.\n", victim.Path)
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(victim.Size)
		stats.SkippedROFiles.Add(1)
	case action.ResultSkippedRef:
		stats.SkippedRefFiles.Add(1)
	case action.ResultAlreadyHardlinked:
		// Silently skipped; verbose mode logs the pair.
		logger.InfoContext(ctx, "already hardlinked",
			"keeper", keeper.Path, "victim", victim.Path)
	case action.ResultHardlinkLimit:
		logger.WarnContext(ctx, "hardlink limit reached",
			"keeper", keeper.Path, "victim", victim.Path)
	case action.ResultError:
		logger.ErrorContext(ctx, "action error", "victim", victim.Path)
	case action.ResultNotDuplicate:
		// Nothing to report.
	}
}

// reportCoW records a CoWDetect outcome.
func reportCoW(out action.Outcome, stats *dupe.Stats) {
	if len(out.Files) < minCompareFiles {
		return
	}

	keeper := out.Files[0]
	victim := out.Files[1]

	stats.DuplicateFiles.Add(1)
	stats.DuplicateBytes.Add(victim.Size)

	if !out.Shared {
		return
	}

	stats.CoWGroups.Add(1)
	stats.CoWSharedBytes.Add(out.SharedBytes)
	fmt.Fprintf(os.Stderr, "CoW group: '%s' and '%s' share %s\n",
		keeper.Path, victim.Path, formatSize(out.SharedBytes))
}

// runListLink implements find --listlink: enumerate files that share a physical
// inode without running duplicate detection.
func runListLink(ctx context.Context, cfg *config.Config, stats *dupe.Stats, logger *slog.Logger) error {
	threads := resolveThreads(cfg.Threads)

	walkResultCh := make(chan fswalker.Result, threads*channelBufferFactor)
	fileInfoCh := make(chan dupe.FileInfo, threads*channelBufferFactor)

	pool := worker.New(threads)
	defer pool.Wait()

	detector := dupe.NewDetector(stats)

	go walkAll(ctx, cfg, stats, walkResultCh)
	go scanChecksums(ctx, stats, logger, pool, walkResultCh, fileInfoCh, true)

	for fi := range fileInfoCh {
		stats.TotalFiles.Add(1)
		stats.TotalBytes.Add(fi.Size)
		detector.InsertInode(fi)
	}

	groups := detector.InodeGroups()
	sort.Slice(groups, func(i, j int) bool { return groups[i][0].Path < groups[j][0].Path })

	for _, group := range groups {
		fmt.Fprintf(os.Stderr, "Hardlink group, %d hardlinked instances found:\n", len(group))
		for _, fi := range group {
			fmt.Fprintf(os.Stderr, "    '%s'\n", fi.Path)
		}
		stats.HardlinkGroups.Add(1)
	}

	printSummary(stats)
	return nil
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
	if n := stats.HardlinkGroups.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d hardlink groups found\n", n)
	}
	if n := stats.CoWGroups.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d CoW groups found, %s shared\n",
			n, formatSize(stats.CoWSharedBytes.Load()))
	}
	if n := stats.SkippedROFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d read-only files skipped\n", n)
	}
	if n := stats.SkippedRefFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "  %d reference files skipped\n", n)
	}
}

// formatSize renders a byte count in a compact human-readable form.
func formatSize(bytes int64) string {
	if bytes < bytesPerKB {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < bytesPerMB {
		return fmt.Sprintf("%d kB", bytes/bytesPerKB)
	}
	return fmt.Sprintf("%d MB", bytes/bytesPerMB)
}
