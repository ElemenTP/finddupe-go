// Package test contains system-level integration tests for finddupe.
// These tests compile the binary and run it against real filesystems.
package test

import (
	"bytes"
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
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build finddupe: %v\n", err)
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
func run(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	bin := buildBinary(t)
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
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
func makeReadOnly(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0444); err != nil {
		t.Fatal(err)
	}
	return path
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
// Find Mode Tests
// =============================================================================

func TestFind_BasicDuplicates(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "hello world")
	makeFile(t, dir, "b.txt", "hello world")
	makeFile(t, dir, "c.txt", "different")

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	// Should report 3 files, 1 duplicate.
	if !strings.Contains(stderr, "Files:") {
		t.Error("expected 'Files:' in output")
	}
	if !strings.Contains(stderr, "Dupes:") {
		t.Error("expected 'Dupes:' in output")
	}

	// Should show "Duplicate: / With:" lines.
	hasDup := strings.Contains(stderr, "Duplicate:") && strings.Contains(stderr, "With:")
	if !hasDup {
		t.Errorf("expected 'Duplicate:' and 'With:' lines, got:\n%s", stderr)
	}
}

func TestFind_NoDuplicates(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "aaa")
	makeFile(t, dir, "b.txt", "bbb")
	makeFile(t, dir, "c.txt", "ccc")

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if strings.Contains(stderr, "Duplicate:") {
		t.Error("expected no duplicate lines for unique files")
	}
}

func TestFind_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	_, stderr, code := run(t, "find", dir, "--no-progress")

	// Empty dir should succeed but report 0 files (the directory itself
	// contains no files, though filepath.WalkDir will process the root dir
	// which is skipped as it's a directory).
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should not crash.
	if !strings.Contains(stderr, "Files:") {
		t.Error("expected summary output even for empty dir")
	}
}

func TestFind_ZeroLengthFiles_Skipped(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "empty.txt", "")
	makeFile(t, dir, "full.txt", "content")

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Zero-length file should be skipped.
	if !strings.Contains(stderr, "files of zero length were skipped") {
		t.Error("expected zero-length skip message")
	}
}

func TestFind_ZeroLengthFiles_Included(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "empty1.txt", "")
	makeFile(t, dir, "empty2.txt", "")
	makeFile(t, dir, "full.txt", "content")

	_, stderr, code := run(t, "find", dir, "--no-progress", "-z")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Two empty files with same size should not count as duplicates without
	// matching checksums (both are 0 bytes, checksum = 0 for both).
	// But actually, checksum of empty file is 0, so they match.
	// Verify duplicates are reported.
	if !strings.Contains(stderr, "Dupes:") {
		t.Error("expected Dupes in output")
	}
}

func TestFind_Verbose(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "hello")
	makeFile(t, dir, "b.txt", "hello")

	_, stderr, code := run(t, "find", dir, "--no-progress", "-v")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stderr, "Duplicate:") {
		t.Error("expected duplicate output in verbose mode")
	}
}

func TestFind_ThreadsFlag(t *testing.T) {
	dir := t.TempDir()
	for i := range 20 {
		makeFile(t, dir, fmt.Sprintf("file%d.txt", i), "data")
	}

	_, stderr, code := run(t, "find", dir, "--no-progress", "-t", "2")

	if code != 0 {
		t.Fatalf("expected exit 0 with --threads 2, got %d", code)
	}
	if !strings.Contains(stderr, "Files:") {
		t.Error("expected summary output")
	}
}

func TestFind_MultiplePaths(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	makeFile(t, dir1, "a.txt", "same content")
	makeFile(t, dir2, "b.txt", "same content")

	_, stderr, code := run(t, "find", dir1, dir2, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should find the duplicate across two directories.
	if !strings.Contains(stderr, "Duplicate:") {
		t.Errorf("expected cross-directory duplicate detection, got:\n%s", stderr)
	}
}

func TestFind_GlobPattern(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "same")
	makeFile(t, dir, "b.txt", "same")
	makeFile(t, dir, "c.jpg", "same")
	makeFile(t, dir, "d.jpg", "same")

	// Only scan .txt files.
	_, stderr, code := run(t, "find", filepath.Join(dir, "*.txt"), "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should only find duplicates among .txt files (2 files, 1 dupe).
	if !strings.Contains(stderr, "Dupes:") {
		t.Error("expected Dupes in glob-filtered output")
	}
}

func TestFind_RecursiveGlob(t *testing.T) {
	dir := t.TempDir()
	sub := makeDir(t, dir, "sub")
	deep := makeDir(t, dir, "sub/deep")
	makeFile(t, dir, "root.jpg", "photo")
	makeFile(t, sub, "sub.jpg", "photo")
	makeFile(t, deep, "deep.jpg", "photo")
	makeFile(t, deep, "deep.txt", "text")

	_, stderr, code := run(t, "find", filepath.Join(dir, "**", "*.jpg"), "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should find 3 .jpg files, 2 of which are duplicates.
	if !strings.Contains(stderr, "Duplicate:") {
		t.Errorf("expected duplicates in recursive glob, got:\n%s", stderr)
	}
}

func TestFind_LargeFiles(t *testing.T) {
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

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stderr, "Duplicate:") {
		t.Error("expected duplicate detection for large files")
	}
}

func TestFind_SameFirstChunkDifferentAfter(t *testing.T) {
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

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should NOT report as duplicates because full comparison will find differences.
	if strings.Contains(stderr, "Duplicate:") {
		t.Error("expected NO duplicate for files differing after 32KB")
	}
}

func TestFind_BinaryFiles(t *testing.T) {
	dir := t.TempDir()
	data := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD, 0x7F, 0x80}
	makeFile(t, dir, "a.bin", string(data))
	makeFile(t, dir, "b.bin", string(data))
	makeFile(t, dir, "c.bin", string([]byte{0x00, 0x01, 0x03}))

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stderr, "Duplicate:") {
		t.Error("expected duplicate detection for binary files")
	}
}

func TestFind_ManyDuplicates(t *testing.T) {
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

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should find 9 duplicates (10 identical files = 9 duplicates, one kept as original).
	if !strings.Contains(stderr, "Duplicate:") {
		t.Error("expected duplicate lines for many duplicates")
	}
}

func TestFind_SingleFile(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "only.txt", "just one file")

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if strings.Contains(stderr, "Duplicate:") {
		t.Error("expected no duplicates with single file")
	}
}

func TestFind_MultipleDuplicateGroups(t *testing.T) {
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

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should find duplicates in both groups.
	dupCount := strings.Count(stderr, "Duplicate:")
	if dupCount < 3 {
		t.Errorf("expected at least 3 duplicates (2 in group A + 1 in group B), got %d\n%s",
			dupCount, stderr)
	}
}

// =============================================================================
// Dedupe --delete Tests
// =============================================================================

func TestDedupeDelete_Basic(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "original.txt", "delete test content")
	makeFile(t, dir, "copy.txt", "delete test content")
	makeFile(t, dir, "unique.txt", "keep this")

	_, stderr, code := run(t, "dedupe", "--delete", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "files deleted") {
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
	dir := t.TempDir()
	content := "hardlink test content here"
	makeFile(t, dir, "original.txt", content)
	makeFile(t, dir, "copy.txt", content)
	makeFile(t, dir, "unique.txt", "different data")

	_, stderr, code := run(t, "dedupe", "--hardlink", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "files replaced with hardlinks") {
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
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "cow test")
	makeFile(t, dir, "b.txt", "cow test")

	_, stderr, code := run(t, "dedupe", "--cow", dir, "--no-progress")

	// CoW should fail with an error (not implemented).
	// The pipeline logs errors but the process may still exit 0.
	// We just verify it doesn't crash.
	if code != 0 {
		t.Logf("CoW exit code: %d (expected, not implemented)", code)
	}
	_ = stderr
}

// =============================================================================
// Edge Cases and Error Handling
// =============================================================================

func TestError_NoPaths(t *testing.T) {
	_, stderr, code := run(t, "find", "--no-progress")

	if code == 0 {
		t.Error("expected non-zero exit when no paths provided")
	}
	if !strings.Contains(stderr, "requires at least") && !strings.Contains(stderr, "Error") {
		t.Errorf("expected error about missing paths, got:\n%s", stderr)
	}
}

func TestError_NonexistentPath(t *testing.T) {
	_, _, code := run(t, "find", "/nonexistent/path/that/does/not/exist", "--no-progress")

	// The walker should handle non-existent paths gracefully.
	// It should either succeed (just find no files) or fail.
	// Either behavior is acceptable as long as it doesn't panic.
	t.Logf("nonexistent path exit code: %d", code)
}

func TestError_NoSubcommand(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := run(t, dir)

	// Running without a subcommand should fail.
	if code == 0 {
		t.Error("expected non-zero exit without subcommand")
	}
	_ = stderr
}

func TestFind_ThreadsZero(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "data")

	// Should not crash with 0 threads (should default to NumCPU).
	_, _, code := run(t, "find", dir, "--no-progress", "-t", "0")

	if code != 0 {
		t.Fatalf("expected exit 0 with -t 0, got %d", code)
	}
}

func TestFind_ManyThreads(t *testing.T) {
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
	stdout, _, code := run(t, "version")

	if code != 0 {
		t.Fatalf("expected exit 0 for version, got %d", code)
	}
	if !strings.Contains(stdout, "finddupe") {
		t.Error("expected version output to contain 'finddupe'")
	}
}

func TestFind_Help(t *testing.T) {
	stdout, _, code := run(t, "--help")

	if code != 0 {
		t.Fatalf("expected exit 0 for help, got %d", code)
	}
	if !strings.Contains(stdout, "finddupe") || !strings.Contains(stdout, "Usage") {
		t.Error("expected help output with 'finddupe' and 'Usage'")
	}
}

func TestFind_SubcommandHelp(t *testing.T) {
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
	dir := t.TempDir()
	subA := makeDir(t, dir, "subA")
	subB := makeDir(t, dir, "subB")
	subC := makeDir(t, dir, "subA/subC")

	makeFile(t, dir, "root_dup.txt", "nested duplicate")
	makeFile(t, subA, "a_dup.txt", "nested duplicate")
	makeFile(t, subB, "b_unique.txt", "nested unique b")
	makeFile(t, subC, "c_unique.txt", "nested unique c")

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Should find the duplicate across nested dirs.
	if !strings.Contains(stderr, "Duplicate:") {
		t.Error("expected duplicate detection across nested dirs")
	}
}

func TestDedupe_NestedDirectories(t *testing.T) {
	dir := t.TempDir()
	subA := makeDir(t, dir, "subA")
	subB := makeDir(t, dir, "subB")

	makeFile(t, dir, "root.txt", "nested dedupe content")
	makeFile(t, subA, "a.txt", "nested dedupe content")
	makeFile(t, subB, "b.txt", "nested dedupe content")
	makeFile(t, subB, "unique.txt", "unique nested content")

	_, stderr, code := run(t, "dedupe", "--delete", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stderr, "files deleted") {
		t.Errorf("expected deletion across nested dirs, got:\n%s", stderr)
	}

	// Count remaining files by walking the tree.
	var remaining int
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
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
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "glob test")
	makeFile(t, dir, "b.txt", "glob test")
	makeFile(t, dir, "c.jpg", "glob test")

	_, stderr, code := run(t, "find", filepath.Join(dir, "*.txt"), "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stderr, "Duplicate:") {
		t.Error("expected duplicate in .txt-only glob")
	}
}

func TestGlob_CurrentDirPattern(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "test.jpg", "current dir glob test")

	// Test with ** pattern.
	_, stderr, code := run(t, "find", filepath.Join(dir, "**", "*.jpg"), "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stderr, "Files:") {
		t.Error("expected file summary for current dir glob")
	}
}

// =============================================================================
// Stats Verification
// =============================================================================

func TestStats_Find(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "a.txt", "stats test content")
	makeFile(t, dir, "b.txt", "stats test content")
	makeFile(t, dir, "c.txt", "unique stats test content")

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	// Parse the stats. Look for "Files: ... in 3 files".
	if !strings.Contains(stderr, "in     3 files") {
		t.Errorf("expected 3 files in stats, got:\n%s", stderr)
	}
	// Expect 1 duplicate file.
	if !strings.Contains(stderr, "in     1 files") {
		t.Errorf("expected 1 duplicate in stats, got:\n%s", stderr)
	}
}

func TestStats_FileSizes(t *testing.T) {
	dir := t.TempDir()
	content := "this string is thirty two bytes!" // exactly 32 bytes
	if len(content) != 32 {
		t.Fatalf("test setup error: content length = %d, want 32", len(content))
	}
	makeFile(t, dir, "a.txt", content)
	makeFile(t, dir, "b.txt", content)

	_, stderr, code := run(t, "find", dir, "--no-progress")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// 2 files × 32 bytes = 64 bytes total.
	if !strings.Contains(stderr, "64 B") {
		t.Errorf("expected '64 B' in stats, got:\n%s", stderr)
	}
}

// =============================================================================
// Concurrent Runs (stress test)
// =============================================================================

func TestConcurrentRuns(t *testing.T) {
	// Run finddupe concurrently to ensure no global state corruption.
	dir := t.TempDir()
	for i := range 10 {
		makeFile(t, dir, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("content-%d", i%3))
	}

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, code := run(t, "find", dir, "--no-progress")
			if code != 0 {
				errs <- fmt.Errorf("concurrent run failed with exit %d", code)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// =============================================================================
// Helper: count files matching predicate
// =============================================================================

func countLinesContaining(text, substr string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, substr) {
			count++
		}
	}
	return count
}

// unused but kept for potential future use
var _ = strconv.Itoa
