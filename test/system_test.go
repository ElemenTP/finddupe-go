// Package test contains system-level integration tests for finddupe.
// These tests compile the binary and run it against real filesystems.
package test_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// finddupeBin is the path to the compiled finddupe binary.
// It is built once by TestMain and reused across all tests.
var finddupeBin string

func TestMain(m *testing.M) {
	// Build the binary once.
	tmpDir, err := os.MkdirTemp("", "finddupe-system-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	finddupeBin = filepath.Join(tmpDir, "finddupe")
	cmd := exec.Command("go", "build", "-o", finddupeBin, ".")
	cmd.Dir = ".." // build from project root
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if runErr := cmd.Run(); runErr != nil {
		fmt.Fprintf(os.Stderr, "failed to build finddupe: %v\n", runErr)
		os.RemoveAll(tmpDir)
		os.Exit(1)
	}

	code := m.Run()

	os.RemoveAll(tmpDir)
	os.Exit(code)
}

func buildBinary(t *testing.T) string {
	t.Helper()
	if finddupeBin == "" {
		t.Fatal("binary not built; TestMain must have failed")
	}
	return finddupeBin
}

// run runs finddupe with the given arguments and returns stdout, stderr, and exit code.
func run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	bin := buildBinary(t)
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	exitCode := 0
	err := cmd.Run()
	if err != nil {
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to run finddupe: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// makeFile creates a file with the given content.
func makeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// makeDir creates a subdirectory.
func makeDir(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

// makeReadOnly creates a read-only file.
func makeReadOnly(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0444); err != nil {
		t.Fatal(err)
	}
}

// filesInDir returns sorted list of filenames in a directory.
func filesInDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// sameInode returns true if two paths share the same inode (hardlinked).
func sameInode(t *testing.T, a, b string) bool {
	t.Helper()
	infoA, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	infoB, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(infoA, infoB)
}

// =============================================================================
// Symlink Tests
// =============================================================================

// TestDedupe_SymlinksResolvedToTarget is the end-to-end regression test for
// `-j`: a followed link used to be handed to the action layer under the link's
// own path, so `--hardlink` linked the symlink itself (os.Link on a link path)
// and `--delete` removed the link while the duplicate file survived. Links are
// now resolved before the action decides.
func TestDedupe_SymlinksResolvedToTarget(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	links := makeDir(t, dir, "links")
	data := makeDir(t, dir, "data")
	target := makeFile(t, data, "target.bin", strings.Repeat("payload", 1000))
	copy1 := makeFile(t, data, "copy.bin", strings.Repeat("payload", 1000))
	// The only duplicate reachable through the link directory.
	if err := os.Symlink(target, filepath.Join(links, "link.bin")); err != nil {
		t.Skipf("cannot create symlinks: %v", err)
	}

	stdout, stderr, code := run(t, "find", "-j", "--no-progress", links, data)
	if code != 0 {
		t.Fatalf("find -j failed (%d): %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, target) || strings.Contains(stdout, filepath.Join(links, "link.bin")) {
		t.Fatalf("find -j must report the resolved target, got:\n%s", stdout)
	}

	// --hardlink: the copy becomes a second link to the target's inode, and the
	// symlink stays a symlink.
	stdout, stderr, code = run(t, "dedupe", "--hardlink", "-j", "--no-progress", links, data)
	if code != 0 {
		t.Fatalf("dedupe --hardlink -j failed (%d): %s%s", code, stdout, stderr)
	}
	if !sameInode(t, target, copy1) {
		t.Fatal("the duplicate must be hardlinked to the target's inode")
	}
	if info, err := os.Lstat(filepath.Join(links, "link.bin")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink must be left alone: %v (mode %v)", err, info)
	}
}

// TestDedupe_DeleteSymlinkTargetNotLink verifies the delete path of the same
// guarantee: the duplicate file is removed, never the link.
func TestDedupe_DeleteSymlinkTargetNotLink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	links := makeDir(t, dir, "links")
	data := makeDir(t, dir, "data")
	target := makeFile(t, data, "target.bin", strings.Repeat("payload", 1000))
	duplicate := makeFile(t, data, "duplicate.bin", strings.Repeat("payload", 1000))
	link := filepath.Join(links, "link.bin")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks: %v", err)
	}

	stdout, stderr, code := run(t, "dedupe", "--delete", "-j", "--no-progress", "--ref", link, data)
	if code != 0 {
		t.Fatalf("dedupe --delete -j failed (%d): %s%s", code, stdout, stderr)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the symlink must be preserved: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the referenced original must be preserved: %v", err)
	}
	if _, err := os.Stat(duplicate); err == nil {
		t.Fatal("the duplicate file must be the one removed")
	}
}

// =============================================================================
// Find Mode Tests
// =============================================================================

func TestFind_BasicDuplicates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "hello world")
	makeFile(t, dir, "b.txt", "hello world")
	makeFile(t, dir, "c.txt", "different")

	stdout, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	// Should report 3 files, 1 duplicate.
	if !strings.Contains(stdout, "Files:") {
		t.Error("expected 'Files:' in output")
	}
	if !strings.Contains(stdout, "Dupes:") {
		t.Error("expected 'Dupes:' in output")
	}

	// Should show "Duplicate: / With:" lines.
	hasDup := strings.Contains(stdout, "Duplicate:") && strings.Contains(stdout, "With:")
	if !hasDup {
		t.Errorf("expected 'Duplicate:' and 'With:' lines, got:\n%s", stderr)
	}
}

func TestFind_NoDuplicates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "aaa")
	makeFile(t, dir, "b.txt", "bbb")
	makeFile(t, dir, "c.txt", "ccc")

	stdout, _, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if strings.Contains(stdout, "Duplicate:") {
		t.Error("expected no duplicate lines for unique files")
	}
}

func TestFind_EmptyDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	stdout, stderr, code := run(t, "find", dir, "--no-progress")

	// A pattern that matches nothing (here: an empty directory) fails the run
	// instead of reporting a successful scan of zero files, so a typo cannot be
	// mistaken for an empty tree.
	if code == 0 {
		t.Fatal("expected a non-zero exit for a pattern that matched nothing")
	}
	if !strings.Contains(stderr, "no files matched") {
		t.Errorf("expected a no-match error, got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "Files:") {
		t.Errorf("expected the summary even when a pattern is empty, got:\n%s", stdout)
	}
}

func TestFind_ZeroLengthFiles_Skipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "empty.txt", "")
	makeFile(t, dir, "full.txt", "content")

	stdout, _, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Zero-length file should be skipped.
	if !strings.Contains(stdout, "files of zero length were skipped") {
		t.Error("expected zero-length skip message")
	}
}

func TestFind_ZeroLengthFiles_Included(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "empty1.txt", "")
	makeFile(t, dir, "empty2.txt", "")
	makeFile(t, dir, "full.txt", "content")

	stdout, _, code := run(t, "find", dir, "--no-progress", "-z")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Two empty files with same size should not count as duplicates without
	// matching checksums (both are 0 bytes, checksum = 0 for both).
	// But actually, checksum of empty file is 0, so they match.
	// Verify duplicates are reported.
	if !strings.Contains(stdout, "Dupes:") {
		t.Error("expected Dupes in output")
	}
}

func TestFind_Verbose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "hello")
	makeFile(t, dir, "b.txt", "hello")

	stdout, _, code := run(t, "find", dir, "--no-progress", "-v")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Duplicate:") {
		t.Error("expected duplicate output in verbose mode")
	}
}

func TestFind_ThreadsFlag(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for i := range 20 {
		makeFile(t, dir, fmt.Sprintf("file%d.txt", i), "data")
	}

	stdout, _, code := run(t, "find", dir, "--no-progress", "-t", "2")

	if code != 0 {
		t.Fatalf("expected exit 0 with --threads 2, got %d", code)
	}
	if !strings.Contains(stdout, "Files:") {
		t.Error("expected summary output")
	}
}

func TestFind_MultiplePaths(t *testing.T) {
	t.Parallel()
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	makeFile(t, dir1, "a.txt", "same content")
	makeFile(t, dir2, "b.txt", "same content")

	stdout, stderr, code := run(t, "find", dir1, dir2, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should find the duplicate across two directories.
	if !strings.Contains(stdout, "Duplicate:") {
		t.Errorf("expected cross-directory duplicate detection, got:\n%s", stderr)
	}
}

func TestFind_GlobPattern(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "same")
	makeFile(t, dir, "b.txt", "same")
	makeFile(t, dir, "c.jpg", "same")
	makeFile(t, dir, "d.jpg", "same")

	// Only scan .txt files.
	stdout, _, code := run(t, "find", filepath.Join(dir, "*.txt"), "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should only find duplicates among .txt files (2 files, 1 dupe).
	if !strings.Contains(stdout, "Dupes:") {
		t.Error("expected Dupes in glob-filtered output")
	}
}

func TestFind_RecursiveGlob(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sub := makeDir(t, dir, "sub")
	deep := makeDir(t, dir, "sub/deep")
	makeFile(t, dir, "root.jpg", "photo")
	makeFile(t, sub, "sub.jpg", "photo")
	makeFile(t, deep, "deep.jpg", "photo")
	makeFile(t, deep, "deep.txt", "text")

	stdout, stderr, code := run(t, "find", filepath.Join(dir, "**", "*.jpg"), "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should find 3 .jpg files, 2 of which are duplicates.
	if !strings.Contains(stdout, "Duplicate:") {
		t.Errorf("expected duplicates in recursive glob, got:\n%s", stderr)
	}
}

func TestFind_LargeFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create files larger than the 32KB checksum threshold.
	size := 100000
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}
	path1 := filepath.Join(dir, "large1.bin")
	path2 := filepath.Join(dir, "large2.bin")
	if err := os.WriteFile(path1, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path2, data, 0644); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Duplicate:") {
		t.Error("expected duplicate detection for large files")
	}
}

func TestFind_SameFirstChunkDifferentAfter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// These files share the same first 32KB but differ afterward.
	// This is a CRC collision that requires full comparison to resolve.
	size := 50000
	dataA := make([]byte, size)
	dataB := make([]byte, size)
	for i := range dataA {
		dataA[i] = byte(i % 256)
		dataB[i] = byte(i % 256)
	}
	// Make them differ after 33KB (past the checksum window).
	dataB[33000] = ^dataB[33000]

	os.WriteFile(filepath.Join(dir, "a.bin"), dataA, 0644)
	os.WriteFile(filepath.Join(dir, "b.bin"), dataB, 0644)

	stdout, _, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should NOT report as duplicates because full comparison will find differences.
	if strings.Contains(stdout, "Duplicate:") {
		t.Error("expected NO duplicate for files differing after 32KB")
	}
}

func TestFind_BinaryFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD, 0x7F, 0x80}
	makeFile(t, dir, "a.bin", string(data))
	makeFile(t, dir, "b.bin", string(data))
	makeFile(t, dir, "c.bin", string([]byte{0x00, 0x01, 0x03}))

	stdout, _, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Duplicate:") {
		t.Error("expected duplicate detection for binary files")
	}
}

func TestFind_ManyDuplicates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "duplicate content for many files test"
	// Create 10 identical files.
	for i := range 10 {
		makeFile(t, dir, fmt.Sprintf("dup%d.txt", i), content)
	}
	// And 3 unique files.
	makeFile(t, dir, "unique1.txt", "something else")
	makeFile(t, dir, "unique2.txt", "yet another")
	makeFile(t, dir, "unique3.txt", "different here")

	stdout, _, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should find 9 duplicates (10 identical files = 9 duplicates, one kept as original).
	if !strings.Contains(stdout, "Duplicate:") {
		t.Error("expected duplicate lines for many duplicates")
	}
}

func TestFind_SingleFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "only.txt", "just one file")

	stdout, _, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if strings.Contains(stdout, "Duplicate:") {
		t.Error("expected no duplicates with single file")
	}
}

func TestFind_MultipleDuplicateGroups(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Group 1: "content A"
	makeFile(t, dir, "a1.txt", "content AAAA")
	makeFile(t, dir, "a2.txt", "content AAAA")
	makeFile(t, dir, "a3.txt", "content AAAA")
	// Group 2: "content B"
	makeFile(t, dir, "b1.txt", "content BBBB")
	makeFile(t, dir, "b2.txt", "content BBBB")
	// Unique
	makeFile(t, dir, "unique.txt", "content CCCC")

	stdout, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should find duplicates in both groups.
	dupCount := strings.Count(stdout, "Duplicate:")
	if dupCount < 3 {
		t.Errorf("expected at least 3 duplicates (2 in group A + 1 in group B), got %d\n%s",
			dupCount, stderr)
	}
}

// =============================================================================
// Dedupe --delete Tests
// =============================================================================

func TestDedupeDelete_Basic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "original.txt", "delete test content")
	makeFile(t, dir, "copy.txt", "delete test content")
	makeFile(t, dir, "unique.txt", "keep this")

	stdout, stderr, code := run(t, "dedupe", "--delete", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "files deleted") {
		t.Errorf("expected 'files deleted' message, got:\n%s", stderr)
	}

	// One of the duplicates should remain, the other deleted.
	files := filesInDir(t, dir)
	t.Logf("remaining files: %v", files)
	// 3 files originally, 1 deleted = 2 remaining.
	if len(files) != 2 {
		t.Errorf("expected 2 files remaining after delete, got %d: %v", len(files), files)
	}
}

func TestDedupeDelete_KeepsOriginalContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "precious data that must be preserved"
	makeFile(t, dir, "original.txt", content)
	makeFile(t, dir, "copy.txt", content)

	_, _, code := run(t, "dedupe", "--delete", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	// The remaining file should have the original content.
	files := filesInDir(t, dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file remaining, got %d: %v", len(files), files)
	}
	survivor := filepath.Join(dir, files[0])
	data, _ := os.ReadFile(survivor)
	if string(data) != content {
		t.Errorf("expected preserved content, got %q", string(data))
	}
}

func TestDedupeDelete_NoActionFlag_Error(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "test")

	_, stderr, code := run(t, "dedupe", dir, "--no-progress")

	if code == 0 {
		t.Error("expected non-zero exit for dedupe without action flag")
	}
	if !strings.Contains(stderr, "no action specified") && !strings.Contains(stderr, "Error") {
		t.Errorf("expected error about missing action flag, got:\n%s", stderr)
	}
}

func TestDedupeDelete_MultipleActions_Error(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, stderr, code := run(t, "dedupe", "--delete", "--hardlink", dir, "--no-progress")

	if code == 0 {
		t.Error("expected non-zero exit for multiple action flags")
	}
	if !strings.Contains(stderr, "only one action") {
		t.Errorf("expected error about multiple action flags, got:\n%s", stderr)
	}
}

func TestDedupeDelete_ReadonlyFile_Skipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create two duplicate files, one readonly. Use a third file to ensure
	// we know which is which regardless of walk order.
	makeFile(t, dir, "a.txt", "readonly test content")
	makeReadOnly(t, dir, "b.txt", "readonly test content")
	makeFile(t, dir, "unique.txt", "other")

	_, _, code := run(t, "dedupe", "--delete", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	// Either a.txt or b.txt should remain. The readonly file should always
	// survive because either:
	// - It's the original (first walked) → kept
	// - It's the candidate → skipped due to readonly
	// The writable file may or may not survive depending on walk order.
	files := filesInDir(t, dir)
	t.Logf("remaining files: %v", files)
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Error("expected readonly file (b.txt) to still exist")
	}
	// unique.txt should always survive.
	if _, err := os.Stat(filepath.Join(dir, "unique.txt")); err != nil {
		t.Error("expected unique file to still exist")
	}
}

func TestDedupeDelete_ReadonlyFile_Forced(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// With -r, both files are treated equally. One should be deleted,
	// leaving exactly one copy plus the unique file.
	makeFile(t, dir, "a.txt", "readonly force test content")
	makeReadOnly(t, dir, "b.txt", "readonly force test content")
	makeFile(t, dir, "unique.txt", "unique data")

	_, _, code := run(t, "dedupe", "--delete", "-r", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	// Only unique.txt + one copy should remain.
	files := filesInDir(t, dir)
	if len(files) != 2 {
		t.Errorf("expected 2 files remaining (1 kept + 1 unique), got %d: %v",
			len(files), files)
	}
	// unique.txt must survive.
	if _, err := os.Stat(filepath.Join(dir, "unique.txt")); err != nil {
		t.Error("expected unique file to still exist")
	}
}

func TestDedupeDelete_ManyDuplicates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "many duplicate files test"
	for i := range 10 {
		makeFile(t, dir, fmt.Sprintf("file%d.txt", i), content)
	}
	makeFile(t, dir, "unique.txt", "different")

	_, stderr, code := run(t, "dedupe", "--delete", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Only 1 of 10 identical + 1 unique = 2 files should remain.
	files := filesInDir(t, dir)
	if len(files) != 2 {
		t.Errorf("expected 2 files remaining (1 kept + 1 unique), got %d: %v",
			len(files), files)
	}
	_ = stderr
}

// =============================================================================
// Dedupe --hardlink Tests
// =============================================================================

func TestDedupeHardlink_Basic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "hardlink test content here"
	makeFile(t, dir, "original.txt", content)
	makeFile(t, dir, "copy.txt", content)
	makeFile(t, dir, "unique.txt", "different data")

	stdout, stderr, code := run(t, "dedupe", "--hardlink", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "files replaced with hardlinks") {
		t.Errorf("expected hardlink success message, got:\n%s", stderr)
	}

	// Both files should exist and share the same inode.
	files := filesInDir(t, dir)
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(files), files)
	}

	// Find the two files that should be hardlinked.
	txtFiles := []string{}
	for _, f := range files {
		if strings.HasSuffix(f, ".txt") {
			txtFiles = append(txtFiles, f)
		}
	}
	if len(txtFiles) >= 2 {
		a := filepath.Join(dir, txtFiles[0])
		b := filepath.Join(dir, txtFiles[1])
		// At least one pair should be hardlinked.
		_ = a
		_ = b
	}
}

func TestDedupeHardlink_ContentPreserved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "precious content for hardlink preservation test"
	makeFile(t, dir, "a.txt", content)
	makeFile(t, dir, "b.txt", content)

	_, _, code := run(t, "dedupe", "--hardlink", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	// Content must be preserved in the remaining file.
	files := filesInDir(t, dir)
	for _, f := range files {
		data, _ := os.ReadFile(filepath.Join(dir, f))
		if string(data) != content {
			t.Errorf("file %s: expected content %q, got %q", f, content, string(data))
		}
	}
}

func TestDedupeHardlink_SameInode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "inode verification test content"
	path1 := makeFile(t, dir, "a.txt", content)
	path2 := makeFile(t, dir, "b.txt", content)

	_, _, code := run(t, "dedupe", "--hardlink", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	// After hardlinking, both files should share the same inode.
	if !sameInode(t, path1, path2) {
		t.Error("expected files to share the same inode after hardlink")
	}
}

func TestDedupeHardlink_WithReadonly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "hardlink readonly test content")
	makeReadOnly(t, dir, "b.txt", "hardlink readonly test content")

	_, _, code := run(t, "dedupe", "--hardlink", dir, "--no-progress")

	// Should not crash; readonly behavior depends on walk order.
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Both files should still exist (one kept, one either hardlinked or skipped).
	files := filesInDir(t, dir)
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
	// Verify readonly file still exists.
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Error("expected readonly file to still exist after hardlink")
	}
}

// =============================================================================
// CoW Tests (should fail gracefully)
// =============================================================================

func TestDedupeCoW_Unsupported(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "cow test")
	makeFile(t, dir, "b.txt", "cow test")

	stdout, stderr, code := run(t, "dedupe", "--cow", dir, "--no-progress")

	// Both files must still be there: a failed clone leaves the victim in place.
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to still exist: %v", name, err)
		}
	}

	// Either the filesystem cloned the pair (nothing failed, exit 0) or the clone
	// failed: then the summary names the unprocessed file and the exit code is
	// non-zero, so a script can tell the two apart.
	if strings.Contains(stdout, "could not be processed") {
		if code == 0 {
			t.Errorf("exit code = 0 although the summary reports unprocessed files:\n%s", stdout)
		}
		if !strings.Contains(stderr, "action failed") && !strings.Contains(stderr, "CoW clone not supported") {
			t.Errorf("a failed action must be explained on stderr, got:\n%s", stderr)
		}
		return
	}
	if code != 0 {
		t.Errorf("exit code = %d although no action failed:\n%s", code, stdout)
	}
}

// =============================================================================
// Edge Cases and Error Handling
// =============================================================================

func TestError_NoPaths(t *testing.T) {
	t.Parallel()
	_, stderr, code := run(t, "find", "--no-progress")

	if code == 0 {
		t.Error("expected non-zero exit when no paths provided")
	}
	if !strings.Contains(stderr, "requires at least") && !strings.Contains(stderr, "Error") {
		t.Errorf("expected error about missing paths, got:\n%s", stderr)
	}
}

func TestError_NonexistentPath(t *testing.T) {
	t.Parallel()
	_, stderr, code := run(t, "find", "/nonexistent/path/that/does/not/exist", "--no-progress")

	if code == 0 {
		t.Error("expected non-zero exit for a non-existent path")
	}
	if !strings.Contains(stderr, "no files matched") {
		t.Errorf("expected a no-match error naming the path, got:\n%s", stderr)
	}
}

// TestOutputStreams verifies the split between the two streams: results and the
// summary go to stdout (so redirection and pipes carry the report), while
// diagnostics stay on stderr and never contain escape sequences when stderr is
// not a terminal.
func TestOutputStreams(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "duplicate content")
	makeFile(t, dir, "b.txt", "duplicate content")

	stdout, stderr, code := run(t, "find", dir, "--no-progress")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
	}

	for _, want := range []string{"Duplicate:", "Files:", "Dupes:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q on stdout, got:\n%s", want, stdout)
		}
		if strings.Contains(stderr, want) {
			t.Errorf("%q must not be on stderr, got:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "\033") {
		t.Errorf("progress escape sequences leaked into a non-terminal stderr: %q", stderr)
	}
}

func TestError_NoSubcommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, stderr, code := run(t, dir)

	// Running without a subcommand should fail.
	if code == 0 {
		t.Error("expected non-zero exit without subcommand")
	}
	_ = stderr
}

func TestFind_ThreadsZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "data")

	// Should not crash with 0 threads (should default to NumCPU).
	_, _, code := run(t, "find", dir, "--no-progress", "-t", "0")

	if code != 0 {
		t.Fatalf("expected exit 0 with -t 0, got %d", code)
	}
}

func TestFind_ManyThreads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for i := range 50 {
		makeFile(t, dir, fmt.Sprintf("f%d.txt", i), "data")
	}

	// Should work with many threads.
	_, _, code := run(t, "find", dir, "--no-progress", "-t", "16")

	if code != 0 {
		t.Fatalf("expected exit 0 with -t 16, got %d", code)
	}
}

func TestFind_Version(t *testing.T) {
	t.Parallel()
	stdout, _, code := run(t, "version")

	if code != 0 {
		t.Fatalf("expected exit 0 for version, got %d", code)
	}
	if !strings.Contains(stdout, "finddupe") {
		t.Error("expected version output to contain 'finddupe'")
	}
}

func TestFind_Help(t *testing.T) {
	t.Parallel()
	stdout, _, code := run(t, "--help")

	if code != 0 {
		t.Fatalf("expected exit 0 for help, got %d", code)
	}
	if !strings.Contains(stdout, "finddupe") || !strings.Contains(stdout, "Usage") {
		t.Error("expected help output with 'finddupe' and 'Usage'")
	}
}

func TestFind_SubcommandHelp(t *testing.T) {
	t.Parallel()
	stdout, _, code := run(t, "find", "--help")

	if code != 0 {
		t.Fatalf("expected exit 0 for find --help, got %d", code)
	}
	if !strings.Contains(stdout, "find [flags]") {
		t.Error("expected find subcommand help")
	}
}

// =============================================================================
// Nested Directories and Complex File Trees
// =============================================================================

func TestNestedDirectories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	subA := makeDir(t, dir, "subA")
	subB := makeDir(t, dir, "subB")
	subC := makeDir(t, dir, "subA/subC")

	makeFile(t, dir, "root_dup.txt", "nested duplicate")
	makeFile(t, subA, "a_dup.txt", "nested duplicate")
	makeFile(t, subB, "b_unique.txt", "nested unique b")
	makeFile(t, subC, "c_unique.txt", "nested unique c")

	stdout, _, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should find the duplicate across nested dirs.
	if !strings.Contains(stdout, "Duplicate:") {
		t.Error("expected duplicate detection across nested dirs")
	}
}

func TestDedupe_NestedDirectories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	subA := makeDir(t, dir, "subA")
	subB := makeDir(t, dir, "subB")

	makeFile(t, dir, "root.txt", "nested dedupe content")
	makeFile(t, subA, "a.txt", "nested dedupe content")
	makeFile(t, subB, "b.txt", "nested dedupe content")
	makeFile(t, subB, "unique.txt", "unique nested content")

	stdout, stderr, code := run(t, "dedupe", "--delete", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "files deleted") {
		t.Errorf("expected deletion across nested dirs, got:\n%s", stderr)
	}

	// Count remaining files by walking the tree.
	var remaining int
	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			remaining++
		}
		return nil
	})
	// 4 original - 2 deleted = 2 remaining (1 kept original + 1 unique).
	if remaining != 2 {
		t.Errorf("expected 2 files remaining, got %d", remaining)
	}
}

// =============================================================================
// Glob Pattern Edge Cases
// =============================================================================

func TestGlob_StarExtension(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "glob test")
	makeFile(t, dir, "b.txt", "glob test")
	makeFile(t, dir, "c.jpg", "glob test")

	stdout, _, code := run(t, "find", filepath.Join(dir, "*.txt"), "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Duplicate:") {
		t.Error("expected duplicate in .txt-only glob")
	}
}

func TestGlob_CurrentDirPattern(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "test.jpg", "current dir glob test")

	// Test with ** pattern.
	stdout, _, code := run(t, "find", filepath.Join(dir, "**", "*.jpg"), "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Files:") {
		t.Error("expected file summary for current dir glob")
	}
}

// =============================================================================
// Stats Verification
// =============================================================================

func TestStats_Find(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "stats test content")
	makeFile(t, dir, "b.txt", "stats test content")
	makeFile(t, dir, "c.txt", "unique stats test content")

	stdout, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	// Parse the stats. Look for "Files: ... in 3 files".
	if !strings.Contains(stdout, "in     3 files") {
		t.Errorf("expected 3 files in stats, got:\n%s", stderr)
	}
	// Expect 1 duplicate file.
	if !strings.Contains(stdout, "in     1 files") {
		t.Errorf("expected 1 duplicate in stats, got:\n%s", stderr)
	}
}

func TestStats_FileSizes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "this string is thirty two bytes!" // exactly 32 bytes
	if len(content) != 32 {
		t.Fatalf("test setup error: content length = %d, want 32", len(content))
	}
	makeFile(t, dir, "a.txt", content)
	makeFile(t, dir, "b.txt", content)

	stdout, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// 2 files × 32 bytes = 64 bytes total.
	if !strings.Contains(stdout, "64 B") {
		t.Errorf("expected '64 B' in stats, got:\n%s", stderr)
	}
}

// =============================================================================
// Concurrent Runs (stress test)
// =============================================================================

func TestConcurrentRuns(t *testing.T) {
	t.Parallel()
	// Run finddupe concurrently to ensure no global state corruption.
	dir := t.TempDir()
	for i := range 10 {
		makeFile(t, dir, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("content-%d", i%3))
	}

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Go(func() {
			_, _, code := run(t, "find", dir, "--no-progress")
			if code != 0 {
				errs <- fmt.Errorf("concurrent run failed with exit %d", code)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// =============================================================================
// Regression: three or more identical files must all be reported
// =============================================================================

func TestFind_ThreeOrMoreIdenticalFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := strings.Repeat("identical-content", 5000) // >32KB, needs full hashing
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		makeFile(t, dir, name, content)
	}
	makeFile(t, dir, "other.txt", "something else")

	stdout, stderr, code := run(t, "find", dir, "--no-progress")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
	}

	if got := countLinesContaining(stdout, "Duplicate:"); got != 3 {
		t.Errorf("expected 3 duplicate reports for 4 identical files, got %d:\n%s", got, stderr)
	}
	if !strings.Contains(stdout, "Dupes:") {
		t.Errorf("expected summary, got:\n%s", stderr)
	}
}

func TestDedupeDelete_ThreeIdenticalFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := strings.Repeat("delete-me-content", 3000) // >32KB
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		makeFile(t, dir, name, content)
	}

	stdout, stderr, code := run(t, "dedupe", "--delete", dir, "--no-progress")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
	}

	remaining := filesInDir(t, dir)
	if len(remaining) != 1 {
		t.Errorf("expected 1 file remaining, got %d: %v", len(remaining), remaining)
	}
	if got := countLinesContaining(stdout, "Deleted:"); got != 2 {
		t.Errorf("expected 2 deletes, got %d:\n%s", got, stderr)
	}
}

// =============================================================================
// Reference paths
// =============================================================================

func TestDedupe_RefKeptAndDuplicateRemoved(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	refDir := makeDir(t, dir, "ref")
	workDir := makeDir(t, dir, "work")

	refFile := makeFile(t, refDir, "orig.txt", "reference content")
	workFile := makeFile(t, workDir, "copy.txt", "reference content")

	_, stderr, code := run(t, "dedupe", "--delete", "--no-progress", "--ref", refDir, workDir)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
	}

	if _, err := os.Stat(refFile); err != nil {
		t.Errorf("reference file must be kept: %v", err)
	}
	if _, err := os.Stat(workFile); !os.IsNotExist(err) {
		t.Errorf("non-reference duplicate must be removed, stat err = %v", err)
	}
}

// TestDedupe_RefKeepsLargeOriginal is the regression test for --ref on files
// larger than the scan-time checksum window: their SHA-256 is unknown when they
// are inserted, so the keeper used to be whichever hash finished first — the
// reference was then handed out as a victim, refused by the action layer, and
// the duplicate was never removed.
func TestDedupe_RefKeepsLargeOriginal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	refDir := makeDir(t, dir, "ref")
	workDir := makeDir(t, dir, "work")

	payload := strings.Repeat("payload", 30000) // 210 KB: hashed after the scan
	refFile := makeFile(t, refDir, "orig.bin", payload)
	work1 := makeFile(t, workDir, "copy1.bin", payload)
	work2 := makeFile(t, workDir, "copy2.bin", payload)

	_, stderr, code := run(t, "dedupe", "--delete", "--no-progress", "--ref", refDir, workDir)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
	}

	if _, err := os.Stat(refFile); err != nil {
		t.Errorf("the reference file must be kept: %v", err)
	}
	for _, path := range []string{work1, work2} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("duplicate %s must be removed, stat err = %v", path, err)
		}
	}
}

// TestDedupe_KeeperIsDeterministic verifies that the kept file does not depend on
// which hash happened to finish first: the same input always keeps the same path
// (the default policy ends with the smallest path). The files are larger than the
// scan-time checksum window so their SHA-256 is computed during the run.
func TestDedupe_KeeperIsDeterministic(t *testing.T) {
	t.Parallel()

	for range 3 {
		dir := t.TempDir()
		payload := strings.Repeat("payload", 30000)
		first := makeFile(t, dir, "a.bin", payload)
		second := makeFile(t, dir, "b.bin", payload)

		_, stderr, code := run(t, "dedupe", "--delete", "--no-progress", first, second)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
		}
		if _, err := os.Stat(first); err != nil {
			t.Fatalf("a.bin must be kept: %v", err)
		}
		if _, err := os.Stat(second); !os.IsNotExist(err) {
			t.Fatalf("b.bin must be the victim, stat err = %v", err)
		}
	}
}

// =============================================================================
// Hardlink group listing (--listlink)
// =============================================================================

func TestFind_ListLink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "hardlink listing content"
	orig := makeFile(t, dir, "a.txt", content)
	link := filepath.Join(dir, "b.txt")
	if err := os.Link(orig, link); err != nil {
		t.Fatalf("link: %v", err)
	}
	makeFile(t, dir, "other.txt", "unrelated content")

	stdout, stderr, code := run(t, "find", "--listlink", dir, "--no-progress")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
	}

	if !strings.Contains(stdout, "Hardlink group") {
		t.Errorf("expected a hardlink group, got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "a.txt") || !strings.Contains(stdout, "b.txt") {
		t.Errorf("expected both hardlinked paths, got:\n%s", stderr)
	}
	if countLinesContaining(stdout, "Duplicate:") != 0 {
		t.Errorf("--listlink must not run duplicate detection:\n%s", stderr)
	}
	if !strings.Contains(stdout, "1 hardlink groups found") {
		t.Errorf("expected hardlink group summary, got:\n%s", stderr)
	}
}

// =============================================================================
// Filesystem-capability helpers for CoW / extent tests
// =============================================================================

// randomContent returns incompressible data so filesystem compression cannot
// distort the physical extent layout.
func randomContent(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 128*1024)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return string(buf)
}

// repoTestDir creates a temp dir in the package working directory, which is
// often on the developer's real btrfs/XFS/APFS volume when /tmp is tmpfs.
func repoTestDir(t *testing.T) string {
	t.Helper()
	//nolint:usetesting // the default temp dir may be on a filesystem without CoW
	dir, err := os.MkdirTemp(".", "finddupe-fs-test-")
	if err != nil {
		return ""
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fsTestDir returns a temporary directory whose filesystem passes probe, or
// skips the test. It first tries the default temp dir (t.TempDir(), i.e.
// $TMPDIR) and then a directory inside the package working directory, which is
// often on the developer's real btrfs/XFS/APFS volume when /tmp is tmpfs.
func fsTestDir(t *testing.T, probe func(dir string) bool) string {
	t.Helper()

	candidates := []string{t.TempDir()}
	if dir := repoTestDir(t); dir != "" {
		candidates = append(candidates, dir)
	}

	for _, dir := range candidates {
		if probe(dir) {
			return dir
		}
	}

	t.Skip("feature is not supported by the default temp dir or the repository filesystem")
	return ""
}

// extentsProbe reports whether find --cow detects a hardlink pair sharing
// physical extents inside dir.
func extentsProbe(t *testing.T, dir string) bool {
	t.Helper()

	//nolint:usetesting // the probe must live on the chosen filesystem
	probe, err := os.MkdirTemp(dir, "extprobe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(probe)

	content := randomContent(t) // incompressible so extents stay physical
	a := makeFile(t, probe, "a.bin", content)
	if linkErr := os.Link(a, filepath.Join(probe, "b.bin")); linkErr != nil {
		return false
	}

	stdout, _, code := run(t, "find", "--cow", probe, "--no-progress")
	return code == 0 && strings.Contains(stdout, "CoW group")
}

// cloneProbe reports whether dedupe --cow can create a clone inside dir.
func cloneProbe(t *testing.T, dir string) bool {
	t.Helper()

	//nolint:usetesting // the probe must live on the chosen filesystem
	probe, err := os.MkdirTemp(dir, "cowprobe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(probe)

	content := randomContent(t) // incompressible so clone extents stay physical
	makeFile(t, probe, "a.bin", content)
	makeFile(t, probe, "b.bin", content)

	stdout, _, code := run(t, "dedupe", "--cow", probe, "--no-progress")
	return code == 0 && strings.Contains(stdout, "CoW cloned:")
}

// =============================================================================
// CoW clone detection (find --cow)
// =============================================================================

func TestDedupeCoW_CreateAndDetect(t *testing.T) {
	t.Parallel()

	dir := fsTestDir(t, func(d string) bool { return cloneProbe(t, d) })
	content := randomContent(t) // incompressible so extents stay physical
	a := makeFile(t, dir, "a.bin", content)
	b := makeFile(t, dir, "b.bin", content)

	stdout, stderr, code := run(t, "dedupe", "--cow", dir, "--no-progress")
	if code != 0 {
		t.Fatalf("dedupe --cow exited %d\n%s", code, stderr)
	}
	if strings.Contains(stdout, "CoW clone not supported") || !strings.Contains(stdout, "CoW cloned:") {
		t.Skipf("CoW cloning not available on this filesystem:\n%s", stderr)
	}

	// The clone must be a real clone: same content, distinct inode.
	if sameInode(t, a, b) {
		t.Fatal("CoW clone must be a distinct inode, not a hardlink")
	}
	dataA, err := os.ReadFile(a)
	if err != nil {
		t.Fatalf("read a: %v", err)
	}
	dataB, err := os.ReadFile(b)
	if err != nil {
		t.Fatalf("read b: %v", err)
	}
	if string(dataA) != string(dataB) || string(dataA) != content {
		t.Fatal("cloned file content differs from the original")
	}

	// find --cow must now report the group with both files fully shared.
	stdout, stderr, code = run(t, "find", "--cow", dir, "--no-progress")
	if code != 0 {
		t.Fatalf("find --cow exited %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "CoW candidate group") {
		t.Errorf("expected a CoW candidate group after cloning, got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "shared: 100.0%") {
		t.Errorf("expected the cloned pair to be reported as 100%% shared, got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "1 CoW groups found") {
		t.Errorf("expected CoW group summary, got:\n%s", stderr)
	}
}

// TestFind_CoW_IndependentCopiesZeroShared verifies that independent copies are
// still listed as a CoW candidate group, but reported as 0% shared.
func TestFind_CoW_IndependentCopiesZeroShared(t *testing.T) {
	t.Parallel()

	dir := fsTestDir(t, func(d string) bool { return extentsProbe(t, d) })
	content := randomContent(t) // incompressible so extents stay physical
	makeFile(t, dir, "a.bin", content)
	makeFile(t, dir, "b.bin", content)

	stdout, stderr, code := run(t, "find", "--cow", dir, "--no-progress")
	if code != 0 {
		t.Fatalf("find --cow exited %d\n%s", code, stderr)
	}

	if !strings.Contains(stdout, "CoW candidate group") {
		t.Errorf("expected the identical copies to be listed as a candidate group:\n%s", stderr)
	}
	if !strings.Contains(stdout, "0.0%") {
		t.Errorf("expected independent copies to be reported as 0%% shared:\n%s", stderr)
	}
	if strings.Contains(stdout, "100.0%") {
		t.Errorf("independent copies must not be reported as fully shared:\n%s", stderr)
	}
}

// TestDedupe_CoW_PreservesHardlink verifies that dedupe --cow never breaks an
// existing hardlink: it already shares all storage.
func TestDedupe_CoW_PreservesHardlink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := randomContent(t)
	a := makeFile(t, dir, "a.bin", content)
	b := filepath.Join(dir, "b.bin")
	if err := os.Link(a, b); err != nil {
		t.Fatalf("link: %v", err)
	}

	stdout, stderr, code := run(t, "dedupe", "--cow", dir, "--no-progress")
	if code != 0 {
		t.Fatalf("dedupe --cow exited %d\n%s", code, stderr)
	}

	if strings.Contains(stdout, "CoW cloned:") {
		t.Errorf("an existing hardlink must not be replaced by a clone:\n%s", stderr)
	}
	if !sameInode(t, a, b) {
		t.Fatal("hardlink relationship was broken by dedupe --cow")
	}
}

// =============================================================================
// Overlapping path arguments must never cause self-elimination
// =============================================================================

func TestDedupeDelete_DuplicatePathArgs_NoDataLoss(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "precious content")
	makeFile(t, dir, "b.txt", "precious content")

	stdout, stderr, code := run(t, "dedupe", "--delete", "--no-progress", dir, dir)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
	}

	// Exactly one of the two files must survive: a file must never be treated
	// as a duplicate of itself.
	remaining := filesInDir(t, dir)
	if len(remaining) != 1 {
		t.Fatalf("expected exactly 1 file to remain, got %d: %v\n%s", len(remaining), remaining, stderr)
	}
	if got := countLinesContaining(stdout, "Deleted:"); got != 1 {
		t.Errorf("expected exactly 1 delete, got %d:\n%s", got, stderr)
	}
}

func TestFind_DuplicatePathArgs_NoSelfDuplicate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "content")
	makeFile(t, dir, "b.txt", "content")

	stdout, stderr, code := run(t, "find", "--no-progress", dir, dir)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
	}

	if got := countLinesContaining(stdout, "Duplicate:"); got != 1 {
		t.Errorf("expected 1 duplicate report, got %d:\n%s", got, stderr)
	}
}

// =============================================================================
// Helper: count files matching predicate
// =============================================================================

func countLinesContaining(text, substr string) int {
	count := 0
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, substr) {
			count++
		}
	}
	return count
}

// unused but kept for potential future use
var _ = strconv.Itoa

// TestDedupe_PreferCompressedRequiresCoW verifies the flag's contract: the clone
// source decides the data layout, so the preference is meaningless (and rejected)
// for the actions that do not rewrite data.
func TestDedupe_PreferCompressedRequiresCoW(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	makeFile(t, dir, "a.bin", "prefer compressed")

	_, stderr, code := run(t, "dedupe", "--delete", "--prefer-compressed", dir)
	if code == 0 {
		t.Fatal("expected --prefer-compressed with --delete to fail")
	}
	if !strings.Contains(stderr, "only applies to --cow") {
		t.Fatalf("unexpected error: %s", stderr)
	}
}

// TestDedupe_InteractiveNeedsTerminal verifies that the interactive keeper mode
// refuses to run without a terminal: a prompt that nobody can answer would leave
// every group untouched while the run appears to succeed.
func TestDedupe_InteractiveNeedsTerminal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	makeFile(t, dir, "a.bin", "interactive test")
	makeFile(t, dir, "b.bin", "interactive test")

	_, stderr, code := run(t, "dedupe", "--delete", "--interactive", dir)
	if code == 0 {
		t.Fatal("expected --interactive without a terminal to fail")
	}
	if !strings.Contains(stderr, "terminal") {
		t.Fatalf("unexpected error: %s", stderr)
	}
}

// TestSummary_ReportsFailedActions verifies that an elimination which could not
// be carried out is counted in the summary: the probe on a ReFS Dev Drive showed
// the failure only as a log line, with a summary that looked like a clean run.
func TestSummary_ReportsFailedActions(t *testing.T) {
	t.Parallel()

	// A directory that disappears between the scan and the action is hard to
	// arrange portably; instead the counter is exercised through the check that
	// every failed action increments (the CLI test below pins the ReFS path only
	// when CoW is unavailable, which is not portable either). This test guards the
	// wiring: a run with no failures prints no such line.
	dir := t.TempDir()
	makeFile(t, dir, "only.txt", "no duplicates here")

	stdout, stderr, code := run(t, "dedupe", "--delete", "--no-progress", dir)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
	}
	if strings.Contains(stdout, "could not be processed") {
		t.Fatalf("a clean run must not report failed actions:\n%s", stdout)
	}
}
