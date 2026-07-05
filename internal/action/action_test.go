package action_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"finddupe/internal/action"
	"finddupe/internal/config"
	"finddupe/internal/dupe"
)

func TestVerifyFullFile_Identical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := make([]byte, 100000)
	rand.Read(data)
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, data, 0644)
	os.WriteFile(b, data, 0644)

	ok, err := action.VerifyFullFile(a, b, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected identical files to be verified as duplicates")
	}
}

func TestVerifyFullFile_Different(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, []byte("aaaa"), 0644)
	os.WriteFile(b, []byte("bbbb"), 0644)

	ok, err := action.VerifyFullFile(a, b, 4)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected different files to not be duplicates")
	}
}

func TestVerifyFullFile_DifferentSizes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, []byte("short"), 0644)
	os.WriteFile(b, []byte("longer_file"), 0644)

	ok, err := action.VerifyFullFile(a, b, 4)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected different-sized files to not be duplicates")
	}
}

func TestVerifyFullFile_LargeFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create files larger than 64KB chunk.
	data := make([]byte, 200000)
	rand.Read(data)
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, data, 0644)
	os.WriteFile(b, data, 0644)

	ok, err := action.VerifyFullFile(a, b, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected identical large files to be verified as duplicates")
	}
}

func TestVerifyFullFile_DifferAfterFirstChunk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := make([]byte, 200000)
	rand.Read(data)
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, data, 0644)
	// Make files differ after 100KB.
	data[100000] ^= 0xFF
	os.WriteFile(b, data, 0644)

	ok, err := action.VerifyFullFile(a, b, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected files differing after first chunk to not be duplicates")
	}
}

func TestDelete_Success(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	origPath := filepath.Join(dir, "original.bin")
	dupPath := filepath.Join(dir, "todelete.bin")
	data := []byte("data")
	os.WriteFile(origPath, data, 0644)
	os.WriteFile(dupPath, data, 0644)

	exec := action.New(action.Options{
		Action: config.ActionDelete,
	})
	result, err := exec.VerifyAndExecute(context.Background(), dupe.DupeGroup{
		Candidate: dupe.FileInfo{Path: dupPath, Size: int64(len(data))},
		Original:  dupe.FileInfo{Path: origPath, Size: int64(len(data))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != action.ResultDeleted {
		t.Errorf("expected ResultDeleted, got %d", result)
	}
	if _, statErr := os.Stat(dupPath); !os.IsNotExist(statErr) {
		t.Error("expected file to be deleted")
	}
}

func TestHardlink_Created(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	origPath := filepath.Join(dir, "original.bin")
	dupPath := filepath.Join(dir, "duplicate.bin")
	data := make([]byte, 1000)
	rand.Read(data)
	os.WriteFile(origPath, data, 0644)
	os.WriteFile(dupPath, data, 0644)

	exec := action.New(action.Options{
		Action: config.ActionHardlink,
	})
	result, err := exec.VerifyAndExecute(context.Background(), dupe.DupeGroup{
		Candidate: dupe.FileInfo{Path: dupPath, Size: int64(len(data))},
		Original:  dupe.FileInfo{Path: origPath, Size: int64(len(data))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != action.ResultHardlinked {
		t.Errorf("expected ResultHardlinked, got %d", result)
	}

	// Verify the duplicate path now points to the same inode as original.
	origInfo, _ := os.Stat(origPath)
	dupInfo, _ := os.Stat(dupPath)
	if !os.SameFile(origInfo, dupInfo) {
		t.Error("expected files to be hardlinked (same file)")
	}

	// Verify content is preserved.
	dupData, _ := os.ReadFile(dupPath)
	if !bytes.Equal(data, dupData) {
		t.Error("expected content to be preserved after hardlink")
	}
}

func TestHardlink_NotDuplicate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	origPath := filepath.Join(dir, "original.bin")
	dupPath := filepath.Join(dir, "different.bin")
	os.WriteFile(origPath, []byte("aaaa"), 0644)
	os.WriteFile(dupPath, []byte("bbbb"), 0644)

	exec := action.New(action.Options{
		Action: config.ActionHardlink,
	})
	result, err := exec.VerifyAndExecute(context.Background(), dupe.DupeGroup{
		Candidate: dupe.FileInfo{Path: dupPath, Size: 4},
		Original:  dupe.FileInfo{Path: origPath, Size: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != action.ResultNotDuplicate {
		t.Errorf("expected ResultNotDuplicate, got %d", result)
	}
}

func TestCoW_Unsupported(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	origPath := filepath.Join(dir, "original.bin")
	dupPath := filepath.Join(dir, "duplicate.bin")
	data := []byte("test data for cow")
	os.WriteFile(origPath, data, 0644)
	os.WriteFile(dupPath, data, 0644)

	exec := action.New(action.Options{
		Action: config.ActionCoWClone,
	})
	_, err := exec.VerifyAndExecute(context.Background(), dupe.DupeGroup{
		Candidate: dupe.FileInfo{Path: dupPath, Size: int64(len(data))},
		Original:  dupe.FileInfo{Path: origPath, Size: int64(len(data))},
	})
	if err == nil {
		t.Error("expected CoW to return error (not implemented)")
	}
}

func TestReport_NoAction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	origPath := filepath.Join(dir, "original.bin")
	dupPath := filepath.Join(dir, "duplicate.bin")
	data := []byte("same content")
	os.WriteFile(origPath, data, 0644)
	os.WriteFile(dupPath, data, 0644)

	exec := action.New(action.Options{
		Action: config.ActionReport,
	})
	result, err := exec.VerifyAndExecute(context.Background(), dupe.DupeGroup{
		Candidate: dupe.FileInfo{Path: dupPath, Size: int64(len(data))},
		Original:  dupe.FileInfo{Path: origPath, Size: int64(len(data))},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Report mode returns ResultVerifiedDuplicate (confirmed duplicate, no action taken).
	if result != action.ResultVerifiedDuplicate {
		t.Errorf("expected ResultVerifiedDuplicate for report mode, got %d", result)
	}
	// Verify dupe file still exists.
	if _, statErr := os.Stat(dupPath); statErr != nil {
		t.Error("expected duplicate file to still exist in report mode")
	}
}
