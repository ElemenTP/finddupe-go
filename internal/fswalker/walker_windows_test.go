//go:build windows

package fswalker_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"finddupe/internal/fswalker"
)

// TestWalk_CaseInsensitiveGlob verifies the platform rule end to end: a pattern is
// matched against names that differ only in case, the way the shell on that
// platform does, so `C:\Data\*.TXT` finds `photo.txt`.
func TestWalk_CaseInsensitiveGlob(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "photo.txt", "content")

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{filepath.Join(dir, "*.TXT")}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 1 {
		t.Fatalf("expected photo.txt to match *.TXT, got %v", paths)
	}
	if !strings.HasSuffix(paths[0], "photo.txt") {
		t.Errorf("expected photo.txt, got %s", paths[0])
	}
}
