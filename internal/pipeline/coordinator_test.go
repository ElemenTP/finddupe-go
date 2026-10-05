package pipeline //nolint:testpackage // needs the unexported coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"finddupe/internal/action"
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
		done <- coordinate(ctx, detector, stats, logger, fileInfoCh, executionCh, outcomeCh, nil)
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
