package action

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"finddupe/internal/dupe"
)

// TestSameDevice covers the guard that keeps a hardlink from being attempted
// across volumes (which would fail after the victim was already destroyed).
func TestSameDevice(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b uint64
		want bool
	}{
		{name: "same device", a: 7, b: 7, want: true},
		{name: "different device", a: 7, b: 8, want: false},
		{name: "unknown keeper", a: 0, b: 8, want: true},
		{name: "unknown victim", a: 7, b: 0, want: true},
		{name: "both unknown", a: 0, b: 0, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sameDevice(dupe.FileInfo{Dev: tc.a}, dupe.FileInfo{Dev: tc.b})
			if got != tc.want {
				t.Errorf("sameDevice(%d, %d) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestHardlinkLimitReached verifies the fresh limit check: a normal file is
// never refused (on Unix the filesystem enforces its own limit) and a missing
// keeper is not mistaken for a full one.
func TestHardlinkLimitReached(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if hardlinkLimitReached(path) {
		t.Error("a file with one link must not report the hardlink limit as reached")
	}

	second := filepath.Join(dir, "b.bin")
	if err := os.Link(path, second); err != nil {
		t.Fatalf("link: %v", err)
	}
	if hardlinkLimitReached(path) {
		t.Error("a file with two links must not report the hardlink limit as reached")
	}

	if hardlinkLimitReached(filepath.Join(dir, "missing.bin")) {
		t.Error("a missing file must not report the hardlink limit as reached")
	}
}

// TestCopyTailAt_DestinationOffset is the regression test for the Windows block
// clone corrupting its result: the trailing partial cluster used to be written
// at offset 0 instead of its own offset, so the first bytes of the clone were
// replaced by the file's last bytes.
func TestCopyTailAt_DestinationOffset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.bin")
	dstPath := filepath.Join(dir, "dst.bin")

	src := []byte("AAAABBBBCCCCDDDD")
	if err := os.WriteFile(srcPath, src, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := []byte("................")
	if err := os.WriteFile(dstPath, dst, 0o644); err != nil {
		t.Fatal(err)
	}

	srcFile, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dstPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer dstFile.Close()

	// Copy the last 4 bytes of src to offset 12 of dst.
	if copyErr := copyTailAt(dstFile, srcFile, 12, 12, 4); copyErr != nil {
		t.Fatalf("copyTailAt: %v", copyErr)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("............DDDD")
	if !bytes.Equal(got, want) {
		t.Fatalf("destination = %q, want %q (tail written at the wrong offset)", got, want)
	}
}
