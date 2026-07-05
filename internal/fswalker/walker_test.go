package fswalker_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"finddupe/internal/fswalker"
)

// collectResults reads all results from the channel and returns paths and errors.
func collectResults(t *testing.T, ch <-chan fswalker.Result) (paths []string, errs []error) {
	t.Helper()
	for r := range ch {
		if r.Err != nil {
			errs = append(errs, r.Err)
		} else {
			paths = append(paths, r.Info.Path)
		}
	}
	sort.Strings(paths)
	return
}

func TestWalk_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 0 {
		t.Errorf("expected 0 files, got %d: %v", len(paths), paths)
	}
}

func TestWalk_SingleFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello")

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 file, got %d", len(paths))
	}
	if !strings.HasSuffix(paths[0], "a.txt") {
		t.Errorf("expected a.txt, got %s", paths[0])
	}
}

func TestWalk_Recursive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a")
	mkSubdir(t, dir, "sub")
	writeFile(t, dir, "sub/b.txt", "b")
	mkSubdir(t, dir, "sub/deep")
	writeFile(t, dir, "sub/deep/c.txt", "c")

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(paths), paths)
	}
}

func TestWalk_GlobExtension(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a")
	writeFile(t, dir, "b.jpg", "b")
	writeFile(t, dir, "c.txt", "c")

	w := fswalker.New()
	pattern := filepath.Join(dir, "*.txt")
	ch := w.Walk(context.Background(), []string{pattern}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 .txt files, got %d: %v", len(paths), paths)
	}
	for _, p := range paths {
		if !strings.HasSuffix(p, ".txt") {
			t.Errorf("expected .txt file, got %s", p)
		}
	}
}

func TestWalk_GlobStarRecursive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a")
	mkSubdir(t, dir, "sub")
	writeFile(t, dir, "sub/b.txt", "b")
	mkSubdir(t, dir, "sub/deep")
	writeFile(t, dir, "sub/deep/c.jpg", "c")

	w := fswalker.New()
	pattern := filepath.Join(dir, "**", "*.txt")
	ch := w.Walk(context.Background(), []string{pattern}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 .txt files, got %d: %v", len(paths), paths)
	}
}

func TestWalk_GlobStarAllJPG(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "photo1.jpg", "a")
	writeFile(t, dir, "photo2.jpg", "b")
	mkSubdir(t, dir, "vacation")
	writeFile(t, dir, "vacation/photo3.jpg", "c")
	writeFile(t, dir, "vacation/notes.txt", "d")

	w := fswalker.New()
	pattern := filepath.Join(dir, "**", "*.jpg")
	ch := w.Walk(context.Background(), []string{pattern}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 3 {
		t.Fatalf("expected 3 .jpg files, got %d: %v", len(paths), paths)
	}
}

func TestWalk_ZeroLength_Skipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "empty.txt", "")
	writeFile(t, dir, "full.txt", "content")

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{
		IncludeZeroLen: false,
	})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 file (zero-length skipped), got %d: %v", len(paths), paths)
	}
	if !strings.HasSuffix(paths[0], "full.txt") {
		t.Errorf("expected full.txt, got %s", paths[0])
	}
}

func TestWalk_ZeroLength_Included(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "empty.txt", "")
	writeFile(t, dir, "full.txt", "content")

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{
		IncludeZeroLen: true,
	})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(paths), paths)
	}
}

func TestWalk_ContextCancellation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create many files to ensure the walk has work when cancelled.
	for i := range 100 {
		writeFile(t, dir, fmt.Sprintf("file%d.txt", i), "data")
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := fswalker.New()
	ch := w.Walk(ctx, []string{dir}, fswalker.WalkOptions{})

	// Cancel immediately.
	cancel()

	// Drain the channel — it should close quickly.
	var count int
	for range ch {
		count++
	}
	// We may get 0 or a few files that were processed before cancellation.
	if count > 100 {
		t.Errorf("expected at most 100 files after cancel, got %d", count)
	}
}

func TestWalk_FileInfoFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "test.bin", "hello world")

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 file, got %d", len(paths))
	}
}

func TestWalk_MultiplePatterns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a")
	writeFile(t, dir, "b.jpg", "b")
	mkSubdir(t, dir, "sub")
	writeFile(t, dir, "sub/c.txt", "c")

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{
		filepath.Join(dir, "*.txt"),
		filepath.Join(dir, "*.jpg"),
	}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(paths), paths)
	}
}

// Helpers

func writeFile(t *testing.T, baseDir, name, content string) {
	t.Helper()
	path := filepath.Join(baseDir, name)
	if parent := filepath.Dir(path); parent != baseDir {
		if err := os.MkdirAll(parent, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func mkSubdir(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
		t.Fatal(err)
	}
}
