package fswalker_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"finddupe/internal/fswalker"
)

// collectResults reads all results from the channel and returns paths and errors.
func collectResults(t *testing.T, ch <-chan fswalker.Result) ([]string, []error) {
	t.Helper()

	var (
		paths []string
		errs  []error
	)

	for r := range ch {
		if r.Err != nil {
			errs = append(errs, r.Err)
		} else {
			paths = append(paths, r.Info.Path)
		}
	}
	sort.Strings(paths)
	return paths, errs
}

func TestWalk_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	// An empty directory matches nothing, which the caller turns into a failed
	// run rather than a silent success.
	if len(errs) != 1 || !isNoMatch(errs[0], dir) {
		t.Errorf("expected one no-match error for %s, got %v", dir, errs)
	}
	if len(paths) != 0 {
		t.Errorf("expected 0 files, got %d: %v", len(paths), paths)
	}
}

// isNoMatch reports whether err is a NoMatchError for the given pattern.
func isNoMatch(err error, pattern string) bool {
	var noMatch *fswalker.NoMatchError
	return errors.As(err, &noMatch) && noMatch.Pattern == pattern
}

// TestWalk_NoMatchError covers the patterns that must be reported as matching
// nothing instead of quietly succeeding.
func TestWalk_NoMatchError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "content")

	patterns := []string{
		filepath.Join(dir, "does-not-exist"),
		filepath.Join(dir, "*.jpg"),
		filepath.Join(t.TempDir(), "empty-subdir"),
	}

	for _, pattern := range patterns {
		t.Run(filepath.Base(pattern), func(t *testing.T) {
			t.Parallel()
			w := fswalker.New()
			ch := w.Walk(context.Background(), []string{pattern}, fswalker.WalkOptions{})
			_, errs := collectResults(t, ch)

			if len(errs) != 1 || !isNoMatch(errs[0], pattern) {
				t.Fatalf("pattern %s: expected one no-match error, got %v", pattern, errs)
			}
		})
	}
}

// TestWalk_MatchingPatternIsNotReported verifies that a pattern which matched a
// file (even one skipped as empty) is not reported as a miss.
func TestWalk_MatchingPatternIsNotReported(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "empty.bin", "")

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(paths) != 0 {
		t.Fatalf("zero-length files are skipped by default, got %v", paths)
	}
	if len(errs) != 0 {
		t.Fatalf("a pattern that matched a (skipped) file must not report a miss: %v", errs)
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

// collectInfo reads all results and returns the reported size per path.
func collectInfo(t *testing.T, ch <-chan fswalker.Result) (map[string]int64, []error) {
	t.Helper()

	sizes := make(map[string]int64)
	var errs []error
	for r := range ch {
		if r.Err != nil {
			errs = append(errs, r.Err)
			continue
		}
		if _, dup := sizes[r.Info.Path]; dup {
			t.Errorf("path reported twice: %s", r.Info.Path)
		}
		sizes[r.Info.Path] = r.Info.Size
	}
	return sizes, errs
}

// symlinkTo creates a symbolic link, skipping the test where that is not
// permitted (Windows without developer mode).
func symlinkTo(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
}

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

// TestWalk_SymlinkToFile_SkippedWithoutFollow verifies the default policy:
// entries that are symbolic links are not reported unless -j is set.
func TestWalk_SymlinkToFile_SkippedWithoutFollow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	writeFile(t, dir, "target.bin", strings.Repeat("x", 4096))
	symlinkTo(t, target, filepath.Join(dir, "link.bin"))

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "target.bin") {
		t.Fatalf("expected only target.bin, got %v", paths)
	}
}

// TestWalk_SymlinkToFile_FollowedUsesTargetMetadata is the regression test for
// a link being reported with its own size (the length of the target path) and
// its target's harvested content.
func TestWalk_SymlinkToFile_FollowedUsesTargetMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const size = 4096
	target := filepath.Join(dir, "target.bin")
	writeFile(t, dir, "target.bin", strings.Repeat("x", size))
	link := filepath.Join(dir, "link.bin")
	symlinkTo(t, target, link)

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{FollowSymlinks: true})
	sizes, errs := collectInfo(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(sizes) != 2 {
		t.Fatalf("expected target and link, got %v", sizes)
	}
	if got := sizes[link]; got != size {
		t.Errorf("link size = %d, want the target's %d (not the link length)", got, size)
	}
	if got := sizes[target]; got != size {
		t.Errorf("target size = %d, want %d", got, size)
	}
}

// TestWalk_SymlinkToDir_Followed verifies that -j descends into a directory
// symlink exactly once, even when its target is also part of the tree.
func TestWalk_SymlinkToDir_Followed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	mkSubdir(t, dir, "real")
	writeFile(t, dir, "real/inside.txt", "content")
	symlinkTo(t, target, filepath.Join(dir, "link"))

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{FollowSymlinks: true})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(paths) != 1 {
		t.Fatalf("expected the file under the target exactly once, got %v", paths)
	}
	if !strings.HasSuffix(paths[0], "inside.txt") {
		t.Errorf("expected inside.txt, got %s", paths[0])
	}
}

// TestWalk_SymlinkDir_SkippedWithoutFollow verifies that a directory symlink is
// not descended into (and does not spill its contents in as files) by default.
func TestWalk_SymlinkDir_SkippedWithoutFollow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mkSubdir(t, dir, "outside")
	writeFile(t, dir, "outside/hidden.txt", "content")
	symlinkTo(t, filepath.Join(dir, "outside"), filepath.Join(dir, "link"))

	pattern := filepath.Join(dir, "link")
	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{pattern}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	// Nothing matched (the link was not followed), so the pattern is reported
	// as a miss rather than quietly doing nothing.
	if len(errs) != 1 || !isNoMatch(errs[0], pattern) {
		t.Errorf("expected one no-match error for the unfollowed link, got %v", errs)
	}
	if len(paths) != 0 {
		t.Fatalf("a directory symlink must not be walked without -j, got %v", paths)
	}
}

// TestWalk_SymlinkLoop_Terminates verifies that a link pointing at an ancestor
// directory does not make the walk recurse forever.
func TestWalk_SymlinkLoop_Terminates(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mkSubdir(t, dir, "sub")
	writeFile(t, dir, "sub/file.txt", "content")
	symlinkTo(t, dir, filepath.Join(dir, "sub", "up"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		w := fswalker.New()
		ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{FollowSymlinks: true})
		for range ch {
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("walk did not terminate on a symlink loop")
	}
}

// TestWalk_BrokenSymlink_FollowedIsIgnored verifies that a dangling link is not
// reported as an error or as a file.
func TestWalk_BrokenSymlink_FollowedIsIgnored(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "content")
	symlinkTo(t, filepath.Join(dir, "does-not-exist"), filepath.Join(dir, "dangling"))

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{FollowSymlinks: true})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Errorf("a dangling link must not produce an error: %v", errs)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "a.txt") {
		t.Fatalf("expected only a.txt, got %v", paths)
	}
}

// TestWalk_ExplicitSymlinkArgument verifies that a link named directly on the
// command line is resolved to its target.
func TestWalk_ExplicitSymlinkArgument(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	writeFile(t, dir, "target.bin", strings.Repeat("y", 128))
	link := filepath.Join(dir, "link.bin")
	symlinkTo(t, target, link)

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{link}, fswalker.WalkOptions{})
	sizes, errs := collectInfo(t, ch)

	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if got := sizes[link]; got != 128 {
		t.Fatalf("explicit link argument reported size %d, want 128 (%v)", got, sizes)
	}
}

// TestWalk_LiteralPathWithGlobCharacters verifies that a path containing glob
// metacharacters is scanned as written when it exists, instead of being split
// into a pattern that matches nothing.
func TestWalk_LiteralPathWithGlobCharacters(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bracketed := filepath.Join(dir, "[2020] photos")
	mkSubdir(t, dir, "[2020] photos")
	writeFile(t, dir, "[2020] photos/a.txt", "content")

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{bracketed}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "a.txt") {
		t.Fatalf("expected a.txt under the bracketed directory, got %v", paths)
	}
}

// TestWalk_GlobInsideGlobNamedDir verifies that a pattern whose base directory
// contains metacharacters is split at the existing directory, not at the first
// bracket.
func TestWalk_GlobInsideGlobNamedDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mkSubdir(t, dir, "[2020] photos")
	writeFile(t, dir, "[2020] photos/a.txt", "content")
	writeFile(t, dir, "[2020] photos/b.jpg", "image")

	w := fswalker.New()
	pattern := filepath.Join(dir, "[2020] photos", "*.txt")
	ch := w.Walk(context.Background(), []string{pattern}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "a.txt") {
		t.Fatalf("expected only a.txt, got %v", paths)
	}
}

// TestWalk_GlobClassStillMatches verifies that a real character class is still
// treated as a glob.
func TestWalk_GlobClassStillMatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a")
	writeFile(t, dir, "b.txt", "b")
	writeFile(t, dir, "c.txt", "c")

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{filepath.Join(dir, "[ab].txt")}, fswalker.WalkOptions{})
	paths, errs := collectResults(t, ch)

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(paths) != 2 {
		t.Fatalf("expected a.txt and b.txt, got %v", paths)
	}
}

// TestWalk_FileInfoCarriesModTime verifies that the walker records the
// modification time the executor later re-checks.
func TestWalk_FileInfoCarriesModTime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "content")
	info, err := os.Stat(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}

	w := fswalker.New()
	ch := w.Walk(context.Background(), []string{dir}, fswalker.WalkOptions{})
	for r := range ch {
		if r.Err != nil {
			t.Fatalf("unexpected error: %v", r.Err)
		}
		if !r.Info.ModTime.Equal(info.ModTime()) {
			t.Errorf("ModTime = %v, want %v", r.Info.ModTime, info.ModTime())
		}
	}
}
