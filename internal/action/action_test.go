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

func TestVerifyChunked_Identical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := make([]byte, 200000)
	rand.Read(data)
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, data, 0644)
	os.WriteFile(b, data, 0644)

	exec := action.New(action.Options{Action: config.ActionReport})
	result, upOrig, upCand, err := exec.VerifyChunked(context.Background(), dupe.DupeGroup{
		Key:       dupe.GroupKey{Signature: 1, Size: int64(len(data))},
		Candidate: dupe.FileInfo{Path: b, Size: int64(len(data))},
		Original:  dupe.FileInfo{Path: a, Size: int64(len(data))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != action.ResultVerifiedDuplicate {
		t.Errorf("expected ResultVerifiedDuplicate, got %d", result)
	}
	if upCand.SHA256 == ([32]byte{}) {
		t.Error("expected complete SHA-256 on candidate")
	}
	if upCand.SHA256 != upOrig.SHA256 {
		t.Error("expected matching SHA-256 for identical files")
	}
}

func TestVerifyChunked_Different(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, []byte("aaaa"), 0644)
	os.WriteFile(b, []byte("bbbb"), 0644)

	exec := action.New(action.Options{Action: config.ActionReport})
	result, _, upCand, err := exec.VerifyChunked(context.Background(), dupe.DupeGroup{
		Key:       dupe.GroupKey{Signature: 1, Size: 4},
		Candidate: dupe.FileInfo{Path: b, Size: 4},
		Original:  dupe.FileInfo{Path: a, Size: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != action.ResultNotDuplicate {
		t.Errorf("expected ResultNotDuplicate, got %d", result)
	}
	// Early-stop: SHA-256 should be incomplete, hash state saved.
	if upCand.SHA256 != ([32]byte{}) {
		t.Error("expected incomplete SHA-256 after early-stop")
	}
	if len(upCand.HashState) == 0 {
		t.Error("expected hash state saved after early-stop")
	}
	if upCand.HashOffset == 0 {
		t.Error("expected non-zero hash offset after early-stop")
	}
}

func TestVerifyChunked_DifferentSizes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, []byte("short"), 0644)
	os.WriteFile(b, []byte("longer_file"), 0644)

	exec := action.New(action.Options{Action: config.ActionReport})
	result, _, _, err := exec.VerifyChunked(context.Background(), dupe.DupeGroup{
		Key:       dupe.GroupKey{Signature: 1, Size: 5},
		Candidate: dupe.FileInfo{Path: b, Size: 11},
		Original:  dupe.FileInfo{Path: a, Size: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != action.ResultNotDuplicate {
		t.Errorf("expected ResultNotDuplicate for different sizes, got %d", result)
	}
}

func TestVerifyChunked_PartialStateResume(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := make([]byte, 200000)
	rand.Read(data)
	a := filepath.Join(dir, "a.bin")
	os.WriteFile(a, data, 0644)

	// b has same first 64KB, different after.
	dataB := make([]byte, 200000)
	copy(dataB, data)
	dataB[100000] ^= 0xFF
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(b, dataB, 0644)

	exec := action.New(action.Options{Action: config.ActionReport})

	// First comparison: should early-stop, saving partial state.
	_, upOrig, _, err := exec.VerifyChunked(context.Background(), dupe.DupeGroup{
		Key:       dupe.GroupKey{Signature: 1, Size: int64(len(data))},
		Candidate: dupe.FileInfo{Path: b, Size: int64(len(dataB))},
		Original:  dupe.FileInfo{Path: a, Size: int64(len(data))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upOrig.HashOffset == 0 {
		t.Error("expected partial hash state after early-stop")
	}

	// Second comparison: resume from partial state.
	// c matches b exactly.
	c := filepath.Join(dir, "c.bin")
	os.WriteFile(c, dataB, 0644)

	_, upOrig2, _, err := exec.VerifyChunked(context.Background(), dupe.DupeGroup{
		Key:       dupe.GroupKey{Signature: 1, Size: int64(len(dataB))},
		Candidate: dupe.FileInfo{Path: c, Size: int64(len(dataB))},
		Original:  upOrig, // has partial state from first comparison
	})
	if err != nil {
		t.Fatal(err)
	}
	if upOrig2.HashOffset <= upOrig.HashOffset {
		t.Error("expected hash offset to increase after resume")
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

	exec := action.New(action.Options{Action: config.ActionDelete})
	result, _, _, err := exec.VerifyChunked(context.Background(), dupe.DupeGroup{
		Key:       dupe.GroupKey{Signature: 1, Size: int64(len(data))},
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

	exec := action.New(action.Options{Action: config.ActionHardlink})
	result, _, _, err := exec.VerifyChunked(context.Background(), dupe.DupeGroup{
		Key:       dupe.GroupKey{Signature: 1, Size: int64(len(data))},
		Candidate: dupe.FileInfo{Path: dupPath, Size: int64(len(data))},
		Original:  dupe.FileInfo{Path: origPath, Size: int64(len(data))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != action.ResultHardlinked {
		t.Errorf("expected ResultHardlinked, got %d", result)
	}

	origInfo, _ := os.Stat(origPath)
	dupInfo, _ := os.Stat(dupPath)
	if !os.SameFile(origInfo, dupInfo) {
		t.Error("expected files to be hardlinked (same file)")
	}

	dupData, _ := os.ReadFile(dupPath)
	if !bytes.Equal(data, dupData) {
		t.Error("expected content to be preserved after hardlink")
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

	exec := action.New(action.Options{Action: config.ActionReport})
	result, _, _, err := exec.VerifyChunked(context.Background(), dupe.DupeGroup{
		Key:       dupe.GroupKey{Signature: 1, Size: int64(len(data))},
		Candidate: dupe.FileInfo{Path: dupPath, Size: int64(len(data))},
		Original:  dupe.FileInfo{Path: origPath, Size: int64(len(data))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != action.ResultVerifiedDuplicate {
		t.Errorf("expected ResultVerifiedDuplicate, got %d", result)
	}
	if _, statErr := os.Stat(dupPath); statErr != nil {
		t.Error("expected duplicate file to still exist in report mode")
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

	exec := action.New(action.Options{Action: config.ActionCoWClone})
	_, _, _, err := exec.VerifyChunked(context.Background(), dupe.DupeGroup{
		Key:       dupe.GroupKey{Signature: 1, Size: int64(len(data))},
		Candidate: dupe.FileInfo{Path: dupPath, Size: int64(len(data))},
		Original:  dupe.FileInfo{Path: origPath, Size: int64(len(data))},
	})
	if err == nil {
		t.Error("expected CoW to return error (not implemented)")
	}
}
