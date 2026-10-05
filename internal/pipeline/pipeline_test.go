package pipeline_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"finddupe/internal/config"
	"finddupe/internal/pipeline"
)

// TestRun_CancelledContext verifies the pipeline shuts down promptly and
// reports cancellation instead of deadlocking.
func TestRun_CancelledContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := &config.Config{
		Action:       config.ActionReport,
		Paths:        []string{dir},
		ShowProgress: false,
	}

	err := pipeline.Run(ctx, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v, want context.Canceled", err)
	}
}

// TestRun_EmptyPatterns verifies the pipeline completes normally when there is
// nothing to scan.
func TestRun_EmptyPatterns(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Action:       config.ActionReport,
		ShowProgress: false,
	}

	if err := pipeline.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
}

// TestRun_NoMatchFails verifies that a pattern which matches nothing fails the
// run instead of reporting a successful scan of zero files.
func TestRun_NoMatchFails(t *testing.T) {
	t.Parallel()

	empty := t.TempDir()
	cfg := &config.Config{
		Action:       config.ActionReport,
		Paths:        []string{empty},
		ShowProgress: false,
	}

	err := pipeline.Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run() = nil, want a no-match error")
	}
	if !strings.Contains(err.Error(), "no files matched") || !strings.Contains(err.Error(), empty) {
		t.Fatalf("Run() = %v, want it to name the unmatched pattern %s", err, empty)
	}
}

// TestRun_RepeatedNoMatchPatternIsNamedOnce verifies that a pattern given twice
// is reported once: the message is read to find out which argument was wrong, and
// seeing the same path twice suggests two different patterns failed.
func TestRun_RepeatedNoMatchPatternIsNamedOnce(t *testing.T) {
	t.Parallel()

	empty := t.TempDir()
	cfg := &config.Config{
		Action:       config.ActionReport,
		Paths:        []string{empty, empty},
		ShowProgress: false,
	}

	err := pipeline.Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run() = nil, want a no-match error")
	}
	if got := strings.Count(err.Error(), empty); got != 1 {
		t.Fatalf("Run() = %v, want the pattern named once, got %d times", err, got)
	}
}

// TestRun_ListLink smoke-tests the hardlink listing mode.
func TestRun_ListLink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	orig := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(orig, []byte("hardlink content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Link(orig, filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("link: %v", err)
	}

	cfg := &config.Config{
		Paths:        []string{dir},
		ListLink:     true,
		ShowProgress: false,
	}

	if err := pipeline.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
}
