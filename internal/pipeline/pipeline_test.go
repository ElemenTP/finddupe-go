package pipeline_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
		Mode:         config.ModeFind,
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
		Mode:         config.ModeFind,
		Action:       config.ActionReport,
		ShowProgress: false,
	}

	if err := pipeline.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
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
		Mode:         config.ModeFind,
		Paths:        []string{dir},
		ListLink:     true,
		ShowProgress: false,
	}

	if err := pipeline.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
}
