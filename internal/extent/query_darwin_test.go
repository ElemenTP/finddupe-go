//go:build darwin

package extent_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"finddupe/internal/extent"
)

// TestQuery_CloneSharesExtents verifies that a clonefile(2) copy maps to the
// same physical extents as its original while an independently written file does
// not. Uncompressed files go through F_LOG2PHYS_EXT; compressed files fall back
// to the APFS clone ID, and both make Equal report the sharing.
func TestQuery_CloneSharesExtents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := randomData(t, 256*1024)

	orig := filepath.Join(dir, "orig.bin")
	if err := os.WriteFile(orig, data, 0o644); err != nil {
		t.Fatalf("write orig: %v", err)
	}

	clone := filepath.Join(dir, "clone.bin")
	if out, err := exec.Command("cp", "-c", orig, clone).CombinedOutput(); err != nil {
		t.Skipf("cp -c unavailable: %v (%s)", err, out)
	}

	copied := filepath.Join(dir, "copy.bin")
	if err := os.WriteFile(copied, data, 0o644); err != nil {
		t.Fatalf("write copy: %v", err)
	}

	origExtents := queryOrSkip(t, orig)
	cloneExtents := queryOrSkip(t, clone)
	copyExtents := queryOrSkip(t, copied)

	if !extent.Equal(origExtents, cloneExtents) {
		t.Fatalf("clone must share the physical extents:\n orig=%+v\nclone=%+v", origExtents, cloneExtents)
	}
	if extent.Equal(origExtents, copyExtents) {
		t.Fatalf("independent copy must not share the physical extents:\n orig=%+v\n copy=%+v", origExtents, copyExtents)
	}
}

// TestQuery_PartialClone verifies that rewriting part of a clone with identical
// bytes (which forces APFS to copy that range on write) is reported as partial
// sharing rather than all-or-nothing.
func TestQuery_PartialClone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const size = 512 * 1024
	data := randomData(t, size)

	orig := filepath.Join(dir, "orig.bin")
	if err := os.WriteFile(orig, data, 0o644); err != nil {
		t.Fatalf("write orig: %v", err)
	}

	clone := filepath.Join(dir, "clone.bin")
	if out, err := exec.Command("cp", "-c", orig, clone).CombinedOutput(); err != nil {
		t.Skipf("cp -c unavailable: %v (%s)", err, out)
	}

	// Rewrite the first half of the clone with the very same bytes: the content
	// stays identical, but the filesystem must stop sharing that range.
	f, err := os.OpenFile(clone, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open clone: %v", err)
	}
	if _, err := f.WriteAt(data[:size/2], 0); err != nil {
		f.Close()
		t.Fatalf("rewrite clone: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close clone: %v", err)
	}

	origExtents := queryOrSkip(t, orig)
	cloneExtents := queryOrSkip(t, clone)

	if extent.Equal(origExtents, cloneExtents) {
		t.Skipf("filesystem did not split the extents after the rewrite:\n orig=%+v\nclone=%+v",
			origExtents, cloneExtents)
	}

	shared := extent.SharedWithGroup([][]extent.Extent{origExtents, cloneExtents})[0]
	if shared <= 0 || shared >= size {
		t.Fatalf("expected partial sharing in (0, %d), got %d\n orig=%+v\nclone=%+v",
			size, shared, origExtents, cloneExtents)
	}
}
