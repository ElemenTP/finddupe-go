//go:build unix

package fswalker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"finddupe/internal/fswalker"
)

// TestWalk_Fifo_NotOpened is the regression test for the scan hanging on a
// named pipe: with --zero a FIFO has size 0, and opening it read-only blocks
// until a writer appears. It must be filtered out as a non-regular file before
// any open happens.
func TestWalk_Fifo_NotOpened(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "content")
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}

	done := make(chan struct{})
	var paths []string
	var errs []error
	go func() {
		defer close(done)
		w := fswalker.New()
		ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{IncludeZeroLen: true})
		paths, errs = collectResults(t, ch)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("walk blocked on a FIFO (it must never be opened)")
	}

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "a.txt") {
		t.Fatalf("expected only a.txt, got %v", paths)
	}
}

// TestWalk_ExplicitFifoArgument verifies that naming a FIFO directly does not
// open it either.
func TestWalk_ExplicitFifoArgument(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if _, err := os.Stat(fifo); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var paths []string
	var errs []error
	go func() {
		defer close(done)
		w := fswalker.New()
		ch := w.Walk(context.Background(), []string{fifo}, fswalker.WalkOptions{IncludeZeroLen: true})
		paths, errs = collectResults(t, ch)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("walk blocked on an explicitly named FIFO")
	}

	if len(paths) != 0 {
		t.Fatalf("a FIFO must not be reported as a file, got %v", paths)
	}
	if len(errs) != 1 || !isNoMatch(errs[0], fifo) {
		t.Fatalf("a FIFO-only pattern must report no match, got %v", errs)
	}
}
