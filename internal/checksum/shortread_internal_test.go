package checksum

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"finddupe/internal/dupe"
)

// TestComputeSignature_ShortReadIsRefused verifies that a file which ends before
// the size it was reported with is refused instead of being signed from the bytes
// that are left: the scanned size would be folded into a checksum of content the
// caller never grouped (a file that shrank between the walk and the read).
func TestComputeSignature_ShortReadIsRefused(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "shorter.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, _, shortErr := computeSignature(f, 1000); !errors.Is(shortErr, dupe.ErrFileChanged) {
		t.Fatalf("err = %v, want dupe.ErrFileChanged for a short read", shortErr)
	}

	// The same file with the size it really has signs normally.
	if _, seekErr := f.Seek(0, 0); seekErr != nil {
		t.Fatal(seekErr)
	}
	if _, sha, signErr := computeSignature(f, 3); signErr != nil || sha == [32]byte{} {
		t.Fatalf("computeSignature(3) = (%v, %v), want a digest and no error", sha, signErr)
	}
}
