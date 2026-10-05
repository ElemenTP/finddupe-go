package checksum_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"finddupe/internal/checksum"
	"finddupe/internal/dupe"
)

func TestCompute_EmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}

	sig := signature(t, path, 0)
	// Empty file: crc=0, sum=0+0=0 → signature=0
	if sig != 0 {
		t.Errorf("expected signature 0 for empty file, got %016x", sig)
	}
}

func TestCompute_SmallFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := []byte("hello")
	path := filepath.Join(dir, "small.bin")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	sig := signature(t, path, int64(len(data)))
	if sig == 0 {
		t.Error("expected non-zero signature for non-empty file")
	}
}

func TestCompute_Deterministic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i % 256)
	}
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, data, 0644)
	os.WriteFile(b, data, 0644)

	sigA := signature(t, a, int64(len(data)))
	sigB := signature(t, b, int64(len(data)))

	if sigA != sigB {
		t.Errorf("expected same signature for identical files: %016x != %016x", sigA, sigB)
	}
}

func TestCompute_DifferentFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, []byte("aaaa"), 0644)
	os.WriteFile(b, []byte("bbbb"), 0644)

	sigA := signature(t, a, 4)
	sigB := signature(t, b, 4)

	if sigA == sigB {
		t.Errorf("expected different signatures for different files")
	}
}

func TestCompute_FileSizeFoldedIn(t *testing.T) {
	t.Parallel()
	// Same content, different reported sizes → different signatures. The
	// reader-based path takes the size as given, which is where the folding
	// itself is exercised; ComputeFileInfo requires the size to be accurate.
	data := []byte("abc")

	sigA, err := checksum.ComputeFromReader(bytes.NewReader(data), 3)
	if err != nil {
		t.Fatal(err)
	}
	sigB, err := checksum.ComputeFromReader(bytes.NewReader(data), 1000)
	if err != nil {
		t.Fatal(err)
	}

	if sigA == sigB {
		t.Errorf("expected different signatures when file sizes differ")
	}
}

func TestComputeFileInfo_SizeChanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "small.bin")
	if err := os.WriteFile(path, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}

	// The file is 3 bytes but the scan saw 1000: hashing the first 3 bytes and
	// calling it a 1000-byte file would let a later elimination act on content
	// that was never compared.
	_, err := checksum.ComputeFileInfo(path, 1000)
	if !errors.Is(err, dupe.ErrFileChanged) {
		t.Fatalf("ComputeFileInfo with a stale size: err = %v, want ErrFileChanged", err)
	}
}

func TestComputeFileInfo_ModTime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "stamped.bin")
	if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := checksum.ComputeFileInfo(path, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	if !got.ModTime.Equal(info.ModTime()) {
		t.Errorf("ModTime = %v, want %v", got.ModTime, info.ModTime())
	}
}

func TestCompute_LargeFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "large.bin")
	// Create file larger than BytesToChecksum.
	data := make([]byte, checksum.BytesToChecksum+10000)
	for i := range data {
		data[i] = byte(i % 256)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	sig := signature(t, path, int64(len(data)))

	// Now create a file with same first 32KB but different data after.
	differentTail := make([]byte, len(data))
	copy(differentTail, data)
	differentTail[checksum.BytesToChecksum] = ^differentTail[checksum.BytesToChecksum]
	path2 := filepath.Join(dir, "large2.bin")
	os.WriteFile(path2, differentTail, 0644)

	sig2 := signature(t, path2, int64(len(differentTail)))
	// Same first 32KB → same signature (collision by design).
	if sig != sig2 {
		t.Errorf("expected same signature for files with same first 32KB: %016x != %016x", sig, sig2)
	}
}

func TestComputeFromReader(t *testing.T) {
	t.Parallel()
	data := []byte("test data for reader-based computation")
	r := bytes.NewReader(data)
	sig, err := checksum.ComputeFromReader(r, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if sig == 0 {
		t.Error("expected non-zero signature")
	}
}

func TestCompute_NonExistentFile(t *testing.T) {
	t.Parallel()
	if _, err := checksum.ComputeFileInfo("/nonexistent/file/path.bin", 0); err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestCompute_CRCAndSumComponents(t *testing.T) {
	t.Parallel()
	// Verify the signature packs crc and sum correctly.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	data := []byte{0x01, 0x02, 0x03, 0x04}
	os.WriteFile(path, data, 0644)

	sig := signature(t, path, int64(len(data)))

	crc := uint32(sig >> 32)
	sum := uint32(sig & 0xFFFFFFFF)
	// Both components should be non-zero for this input.
	if crc == 0 {
		t.Error("expected non-zero CRC component")
	}
	if sum == 0 {
		t.Error("expected non-zero sum component")
	}
	t.Logf("signature=%016x crc=%08x sum=%08x", sig, crc, sum)
}

// signature returns the checksum of path through the production entry point, which
// reports the file's identity and modification time in the same pass.
func signature(t *testing.T, path string, size int64) uint64 {
	t.Helper()

	info, err := checksum.ComputeFileInfo(path, size)
	if err != nil {
		t.Fatalf("ComputeFileInfo(%s): %v", path, err)
	}
	return info.Signature
}
