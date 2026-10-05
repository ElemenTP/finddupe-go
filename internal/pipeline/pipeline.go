// Package pipeline orchestrates the full duplicate detection and elimination process.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode"

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

	// percentScale converts a ratio to a percentage.
	percentScale = 100
)

// Run executes the full duplicate detection pipeline.
func Run(ctx context.Context, cfg *config.Config) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	threads := resolveThreads(cfg.Threads)

	logger := newLogger(cfg)

	stats := dupe.NewStats()

	// The progress line lives on stderr and is cleared before any result is
	// printed, so it can never mix into the (stdout) result stream.
	stopProgress := startProgress(ctx, cfg, stats)
	defer stopProgress()

	if cfg.ListLink {
		err := runListLink(ctx, cfg, stats, logger)
		stopProgress()
		printSummary(stats)
		return err
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
	misses := &patternMisses{}
	go scanChecksums(ctx, stats, logger, pool, walkResultCh, fileInfoCh, false, misses)

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
	// In CoW-detect mode, after the input is drained, it emits one CoWDetect
	// execution per identical-content group.
	var final func() []dupe.Execution
	if cfg.CoWDetect {
		final = func() []dupe.Execution { return cowGroupExecutions(detector) }
	}

	err := coordinate(ctx, detector, stats, logger, fileInfoCh, executionCh, outcomeCh, final)

	stopProgress()
	printSummary(stats)

	if err != nil {
		return err
	}
	return misses.err()
}

// startProgress draws the progress line while the scan runs and returns a stop
// function. The line is only drawn when the reporter is enabled and stderr is a
// terminal: escape sequences written into a pipe or a log file are garbage. The
// returned function is idempotent and waits for the line to be cleared, so
// results never appear next to a stale progress line.
func startProgress(ctx context.Context, cfg *config.Config, stats *dupe.Stats) func() {
	if !cfg.ShowProgress || !progress.IsTerminal(os.Stderr) {
		return func() {}
	}

	progCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		progress.New(stats).Run(progCtx)
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

// patternMisses collects the patterns that matched no files at all. A run where
// nothing matched (a typo, an empty directory, a glob that no longer hits) must
// fail loudly instead of reporting success and doing nothing.
type patternMisses struct {
	mu       sync.Mutex
	patterns []string
}

// add records a pattern that matched nothing.
func (p *patternMisses) add(pattern string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.patterns = append(p.patterns, pattern)
}

// err returns an error naming every pattern that matched nothing, or nil.
func (p *patternMisses) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.patterns) == 0 {
		return nil
	}
	quoted := make([]string, len(p.patterns))
	for i, pattern := range p.patterns {
		quoted[i] = strconv.Quote(pattern)
	}
	return fmt.Errorf("no files matched: %s", strings.Join(quoted, ", "))
}

// fileOrder is the canonical order used wherever file order must not depend on
// how the parallel hash workers happened to be scheduled: --ref files first
// (they are the files the user marked as originals), then by path. It does not
// decide which file becomes the keeper during elimination — that is still
// whichever member's hash completes first — but it makes every report
// reproducible and gives the user a way to pin the keeper for a run by naming
// paths deliberately.
func fileOrder(a, b dupe.FileInfo) bool {
	if a.IsRef != b.IsRef {
		return a.IsRef
	}
	return a.Path < b.Path
}

// resultPath renders a path for the single-quoted result lines. Control
// characters are escaped, so a crafted file name cannot forge extra result lines
// that a reader (or a script parsing the report) would attribute to other files.
func resultPath(path string) string {
	if !strings.ContainsFunc(path, unicode.IsControl) {
		return path
	}
	quoted := strconv.Quote(path)
	return quoted[1 : len(quoted)-1]
}

// cowGroupExecutions turns the detector's identical-content groups into
// CoWDetect executions, one per group, in canonical order.
func cowGroupExecutions(detector *dupe.Detector) []dupe.Execution {
	groups := detector.CoWGroups()
	sortGroups(groups)
	execs := make([]dupe.Execution, 0, len(groups))

	for _, group := range groups {
		execs = append(execs, dupe.Execution{
			Key:   dupe.GroupKey{Signature: group[0].Signature, Size: group[0].Size},
			Type:  dupe.CoWDetect,
			Files: group,
		})
	}
	return execs
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
	misses *patternMisses,
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
			// A pattern that matched nothing is a usage error, not an
			// unreadable file; it is reported once the scan has finished.
			if noMatch, ok := errors.AsType[*fswalker.NoMatchError](result.Err); ok {
				misses.add(noMatch.Pattern)
				continue
			}
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
		// Bind the digest to the modification time observed while reading it, so
		// a later change is detected before the file is eliminated.
		fi.ModTime = info.ModTime

		select {
		case fileCh <- fi:
		case <-ctx.Done():
		}
	}) {
		pending.Done()
	}
}

// coordinator owns all detector state while the pipeline runs. It feeds
// insertions and executor outcomes to the detector, queues the executions they
// produce, and closes the execution channel once no work remains.
type coordinator struct {
	ctx         context.Context
	detector    *dupe.Detector
	stats       *dupe.Stats
	logger      *slog.Logger
	executionCh chan<- dupe.Execution
	final       func() []dupe.Execution
	finalDone   bool
	pending     []dupe.Execution
	inFlight    int
	execClosed  bool
}

// coordinate runs the coordinator loop until the input and all executor
// outcomes are drained. final, when non-nil, is called once after the input is
// drained and no work is in flight, to produce any last batch of work (the CoW
// groups).
//
// The loop never sends on the execution channel from outside the select, so it
// cannot block there while outcomes wait to be read. Blocking sends in the
// insert/complete paths would deadlock against executors blocked on a full
// outcome channel, which is exactly what a large scan can hit.
//
//nolint:gocognit // explicit state machine over input, outcomes and queued work
func coordinate(
	ctx context.Context,
	detector *dupe.Detector,
	stats *dupe.Stats,
	logger *slog.Logger,
	fileInfoCh <-chan dupe.FileInfo,
	executionCh chan<- dupe.Execution,
	outcomeCh <-chan action.Outcome,
	final func() []dupe.Execution,
) error {
	c := &coordinator{
		ctx:         ctx,
		detector:    detector,
		stats:       stats,
		logger:      logger,
		executionCh: executionCh,
		final:       final,
	}
	defer c.closeExec()

	fiCh := fileInfoCh
	outCh := outcomeCh

	// Bound the queue of executions waiting to be sent: without a blocking send
	// there is no natural back-pressure, so stop pulling new files while the
	// executors are behind. Never below one, so an unbuffered execution channel
	// still makes progress.
	backlogLimit := max(cap(executionCh)*channelBufferFactor, 1)

	for fiCh != nil || outCh != nil || len(c.pending) > 0 {
		// The send case is part of the select so that draining outcomes always
		// stays possible, even when the execution channel is full.
		var recv <-chan dupe.FileInfo
		if len(c.pending) < backlogLimit {
			recv = fiCh
		}
		var send chan<- dupe.Execution
		var next dupe.Execution
		if !c.execClosed && len(c.pending) > 0 {
			send, next = c.executionCh, c.pending[0]
		}

		select {
		case <-ctx.Done():
			return ctx.Err()

		case fi, ok := <-recv:
			if !ok {
				fiCh = nil
			} else {
				c.pending = append(c.pending, c.detector.Insert(fi)...)
			}

		case out, ok := <-outCh:
			if !ok {
				outCh = nil
			} else {
				reportOutcome(ctx, out, c.stats, c.logger)
				c.pending = append(c.pending, completeExecution(c.detector, out)...)
				c.inFlight--
			}

		case send <- next:
			c.pending = c.pending[1:]
			c.inFlight++
		}

		// Once the input is drained and nothing is queued or in flight, the
		// detector state is final: emit the one-shot batch (CoW groups) and, when
		// that produced nothing, close the execution channel.
		if fiCh == nil && len(c.pending) == 0 && c.inFlight == 0 {
			if !c.finalDone {
				c.finalDone = true
				if c.final != nil {
					c.pending = append(c.pending, c.final()...)
				}
			}
			if len(c.pending) == 0 {
				c.closeExec()
			}
		}
	}

	return nil
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
		fmt.Fprintf(os.Stdout, "Duplicate: '%s'\n", resultPath(keeper.Path))
		fmt.Fprintf(os.Stdout, "With:      '%s'\n", resultPath(victim.Path))
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(victim.Size)
	case action.ResultDeleted:
		fmt.Fprintf(os.Stdout, "Deleted:    '%s'\n", resultPath(victim.Path))
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(victim.Size)
		stats.DeletedFiles.Add(1)
	case action.ResultHardlinked:
		fmt.Fprintf(os.Stdout, "Hardlinked: '%s'\n", resultPath(victim.Path))
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(victim.Size)
		stats.HardlinkedFiles.Add(1)
	case action.ResultCoWCloned:
		fmt.Fprintf(os.Stdout, "CoW cloned: '%s'\n", resultPath(victim.Path))
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(victim.Size)
		stats.CoWClonedFiles.Add(1)
	case action.ResultSkippedRO:
		fmt.Fprintf(os.Stdout, "Skipping duplicate readonly file '%s'.\n", resultPath(victim.Path))
		stats.DuplicateFiles.Add(1)
		stats.DuplicateBytes.Add(victim.Size)
		stats.SkippedROFiles.Add(1)
	case action.ResultSkippedRef:
		stats.SkippedRefFiles.Add(1)
	case action.ResultSkippedChanged:
		fmt.Fprintf(os.Stdout,
			"Skipping '%s' (original '%s'): one of them changed during the scan.\n",
			resultPath(victim.Path), resultPath(keeper.Path))
		stats.SkippedChangedFiles.Add(1)
	case action.ResultSkippedCrossDevice:
		logger.WarnContext(ctx, "hardlink not possible across devices",
			"keeper", keeper.Path, "victim", victim.Path)
	case action.ResultAlreadyHardlinked:
		// Silently skipped; verbose mode logs the pair.
		logger.InfoContext(ctx, "already hardlinked",
			"keeper", keeper.Path, "victim", victim.Path)
	case action.ResultAlreadyShared:
		// Already a CoW clone/share; no work needed.
		logger.InfoContext(ctx, "already shared",
			"keeper", keeper.Path, "victim", victim.Path)
	case action.ResultHardlinkLimit:
		logger.WarnContext(ctx, "hardlink limit reached",
			"keeper", keeper.Path, "victim", victim.Path)
	case action.ResultError:
		logger.ErrorContext(ctx, "action failed",
			"keeper", keeper.Path, "victim", victim.Path, "error", out.Err)
	case action.ResultNotDuplicate:
		// Nothing to report.
	}
}

// reportCoW prints one identical-content group and, for every member, how much
// of it is already shared with the rest of the group. The group is what should
// end up sharing storage (CoW); the ratios show what still needs to be done.
func reportCoW(out action.Outcome, stats *dupe.Stats) {
	if len(out.Files) < minCompareFiles {
		return
	}

	stats.CoWGroups.Add(1)
	stats.DuplicateFiles.Add(int64(len(out.Files) - 1))
	stats.DuplicateBytes.Add(int64(len(out.Files)-1) * out.Files[0].Size)

	fmt.Fprintf(os.Stdout, "CoW candidate group (%d files, identical content):\n", len(out.Files))

	if out.FileShared == nil {
		fmt.Fprintln(os.Stdout, "    extent information unavailable on this filesystem; listing members only")
		for _, fi := range out.Files {
			fmt.Fprintf(os.Stdout, "    '%s'\n", resultPath(fi.Path))
		}
		return
	}

	for i, fi := range out.Files {
		shared := int64(0)
		if i < len(out.FileShared) {
			shared = out.FileShared[i]
		}
		stats.CoWSharedBytes.Add(shared)

		percent := 0.0
		if fi.Size > 0 {
			percent = float64(shared) / float64(fi.Size) * percentScale
		}
		fmt.Fprintf(os.Stdout, "    '%s'  shared: %5.1f%% (%s of %s)\n",
			resultPath(fi.Path), percent, formatSize(shared), formatSize(fi.Size))
	}
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

	misses := &patternMisses{}
	go walkAll(ctx, cfg, stats, walkResultCh)
	go scanChecksums(ctx, stats, logger, pool, walkResultCh, fileInfoCh, true, misses)

	for fi := range fileInfoCh {
		stats.TotalFiles.Add(1)
		stats.TotalBytes.Add(fi.Size)
		detector.InsertInode(fi)
	}

	groups := detector.InodeGroups()
	sortGroups(groups)

	for _, group := range groups {
		fmt.Fprintf(os.Stdout, "Hardlink group, %d hardlinked instances found:\n", len(group))
		for _, fi := range group {
			fmt.Fprintf(os.Stdout, "    '%s'\n", resultPath(fi.Path))
		}
		stats.HardlinkGroups.Add(1)
	}

	return misses.err()
}

// sortGroups orders groups and their members by the canonical file order. The
// detector hands groups out in map order and members in arrival order, both of
// which are scheduling artifacts; sorting keeps the report reproducible.
func sortGroups(groups [][]dupe.FileInfo) {
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return fileOrder(group[i], group[j]) })
	}
	sort.Slice(groups, func(i, j int) bool {
		return fileOrder(groups[i][0], groups[j][0])
	})
}

// printSummary outputs the final statistics.
func printSummary(stats *dupe.Stats) {
	totalFiles := stats.TotalFiles.Load()
	totalBytes := stats.TotalBytes.Load()

	dupFiles := stats.DuplicateFiles.Load()
	dupBytes := stats.DuplicateBytes.Load()

	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintf(os.Stdout, "Files: %8s in %5d files\n",
		formatSize(totalBytes), totalFiles)
	fmt.Fprintf(os.Stdout, "Dupes: %8s in %5d files\n",
		formatSize(dupBytes), dupFiles)

	if n := stats.ZeroLengthFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stdout, "  %d files of zero length were skipped\n", n)
	}
	if n := stats.CantReadFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stdout, "  %d files could not be opened\n", n)
	}
	if n := stats.DeletedFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stdout, "  %d files deleted\n", n)
	}
	if n := stats.HardlinkedFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stdout, "  %d files replaced with hardlinks\n", n)
	}
	if n := stats.CoWClonedFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stdout, "  %d files replaced with CoW clones\n", n)
	}
	if n := stats.HardlinkGroups.Load(); n > 0 {
		fmt.Fprintf(os.Stdout, "  %d hardlink groups found\n", n)
	}
	if n := stats.CoWGroups.Load(); n > 0 {
		// CoWSharedBytes is a per-file sum and would double-count storage, so
		// only the group count is shown here; per-file ratios are printed with
		// each group.
		fmt.Fprintf(os.Stdout, "  %d CoW groups found (%s of file bytes already shared)\n",
			n, formatSize(stats.CoWSharedBytes.Load()))
	}
	if n := stats.SkippedROFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stdout, "  %d read-only files skipped\n", n)
	}
	if n := stats.SkippedRefFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stdout, "  %d reference files skipped\n", n)
	}
	if n := stats.SkippedChangedFiles.Load(); n > 0 {
		fmt.Fprintf(os.Stdout, "  %d files skipped (changed during the scan)\n", n)
	}
}

// formatSize renders a byte count in a compact human-readable form. Exact
// multiples stay whole ("768 kB"); anything else keeps one decimal ("1.9 MB") so
// a value just below a unit boundary is not truncated to the smaller unit.
func formatSize(bytes int64) string {
	switch {
	case bytes < bytesPerKB:
		return fmt.Sprintf("%d B", bytes)
	case bytes < bytesPerMB:
		if bytes%bytesPerKB == 0 {
			return fmt.Sprintf("%d kB", bytes/bytesPerKB)
		}
		return fmt.Sprintf("%.1f kB", float64(bytes)/float64(bytesPerKB))
	default:
		if bytes%bytesPerMB == 0 {
			return fmt.Sprintf("%d MB", bytes/bytesPerMB)
		}
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(bytesPerMB))
	}
}
