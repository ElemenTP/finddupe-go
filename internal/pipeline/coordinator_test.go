package pipeline //nolint:testpackage // needs the unexported coordinator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"finddupe/internal/action"
	"finddupe/internal/config"
	"finddupe/internal/dupe"
)

// TestCoordinateSaturatedChannels is a regression test for the scan hang seen on
// Windows: the coordinator used to send on the execution channel from inside its
// insert/complete paths, so once that channel filled up while every executor was
// blocked writing an outcome, both sides waited on each other forever. The
// process stayed alive with an idle CPU and the progress line froze.
//
// Unbuffered channels and a single executor make the bad ordering deterministic:
// there is no buffer slack to absorb a send while an outcome is pending.
func TestCoordinateSaturatedChannels(t *testing.T) {
	t.Parallel()

	const files = 400
	const timeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	stats := dupe.NewStats()
	detector := dupe.NewDetector(stats)
	logger := slog.New(slog.DiscardHandler)

	executionCh := make(chan dupe.Execution) // unbuffered: the coordinator has no slack
	outcomeCh := make(chan action.Outcome)   // unbuffered: executors block until read
	fileInfoCh := make(chan dupe.FileInfo, files)

	// Every file shares one weak signature, so each insert produces work and the
	// coordinator and the executor keep handing executions back and forth.
	for i := range files {
		fileInfoCh <- dupe.FileInfo{
			Path:      fmt.Sprintf("file-%d", i),
			Size:      4096,
			Signature: 0xfeed,
		}
	}
	close(fileInfoCh)

	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(outcomeCh)

		sha := [32]byte{1} // pretend every file was hashed to the same content
		for ex := range executionCh {
			out := action.Outcome{Kind: ex.Type, Key: ex.Key}
			for _, fi := range ex.Files {
				fi.SHA256 = sha
				out.Files = append(out.Files, fi)
			}
			outcomeCh <- out
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- coordinate(ctx, detector, stats, logger, fileInfoCh, executionCh, outcomeCh, newReportWriter(io.Discard), nil)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("coordinate returned %v", err)
		}
	case <-time.After(timeout):
		t.Fatal("coordinate deadlocked with saturated execution/outcome channels")
	}

	wg.Wait()
}

// TestCoordinateFinalizerRounds verifies that the coordinator keeps asking the
// detector for end-of-scan work until it reports completion, that it dispatches
// every batch it returns, and that each request is bounded by the queue limit —
// a scan with millions of duplicates must not materialize every task at once.
func TestCoordinateFinalizerRounds(t *testing.T) {
	t.Parallel()

	const timeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	stats := dupe.NewStats()
	detector := dupe.NewDetector(stats)
	logger := slog.New(slog.DiscardHandler)

	executionCh := make(chan dupe.Execution, 4)
	outcomeCh := make(chan action.Outcome, 4)
	fileInfoCh := make(chan dupe.FileInfo)
	close(fileInfoCh)

	const perBatch = 3
	const batches = 2
	wantLimit := cap(executionCh) * channelBufferFactor

	var mu sync.Mutex
	calls := 0
	dispatched := 0

	final := func(limit int) ([]dupe.Execution, bool) {
		mu.Lock()
		defer mu.Unlock()

		calls++
		if limit != wantLimit {
			t.Errorf("finalizer called with limit %d, want %d", limit, wantLimit)
		}
		if calls > batches {
			return nil, true
		}
		execs := make([]dupe.Execution, 0, perBatch)
		for i := range perBatch {
			execs = append(execs, dupe.Execution{Type: dupe.DupeElim, Key: dupe.GroupKey{Size: int64(i)}})
		}
		return execs, false
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(outcomeCh)
		for ex := range executionCh {
			mu.Lock()
			dispatched++
			mu.Unlock()
			outcomeCh <- action.Outcome{Kind: ex.Type, Key: ex.Key, Result: action.ResultDeleted}
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- coordinate(
			ctx, detector, stats, logger, fileInfoCh, executionCh, outcomeCh, newReportWriter(io.Discard), final,
		)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("coordinate returned %v", err)
		}
	case <-time.After(timeout):
		t.Fatal("coordinate never finished the finalization rounds")
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if dispatched != perBatch*batches {
		t.Fatalf("dispatched %d executions, want %d", dispatched, perBatch*batches)
	}
	if calls < batches+1 {
		t.Fatalf("finalizer called %d times, want at least %d", calls, batches+1)
	}
}

// TestCoordinateCancelledContextIsReported is the regression test for an
// interrupted run passing for a completed one: the loop could drain the closing
// channels without ever selecting <-ctx.Done() and return nil, so SIGINT printed
// the summary and exited 0.
func TestCoordinateCancelledContextIsReported(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stats := dupe.NewStats()
	fileInfoCh := make(chan dupe.FileInfo)
	executionCh := make(chan dupe.Execution)
	outcomeCh := make(chan action.Outcome)
	close(fileInfoCh)
	close(outcomeCh)

	err := coordinate(ctx, dupe.NewDetector(stats), stats, slog.New(slog.DiscardHandler),
		fileInfoCh, executionCh, outcomeCh, newReportWriter(io.Discard), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("coordinate returned %v, want context.Canceled", err)
	}
}

// TestRunListLinkCancelledContextIsReported covers the same contract for
// --listlink, which used to return only the pattern misses.
func TestRunListLinkCancelledContextIsReported(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runListLink(ctx, &config.Config{Paths: []string{t.TempDir()}}, dupe.NewStats(),
		slog.New(slog.DiscardHandler), newReportWriter(io.Discard))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runListLink returned %v, want context.Canceled", err)
	}
}
