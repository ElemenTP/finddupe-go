//go:build darwin

package extent_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"finddupe/internal/extent"
)

// TestQuery_APFSCloneID verifies that a clonefile(2) copy shares the APFS clone
// ID with its original while an independently written file does not.
func TestQuery_APFSCloneID(t *testing.T) {
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
		t.Fatalf("clone must share the clone id:\n orig=%+v\nclone=%+v", origExtents, cloneExtents)
	}
	if extent.Equal(origExtents, copyExtents) {
		t.Fatalf("independent copy must not share the clone id:\n orig=%+v\n copy=%+v", origExtents, copyExtents)
	}
}
