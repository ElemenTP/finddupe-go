package action

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"finddupe/internal/checksum"
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

// TestUnchanged_DetectsReplacedFile covers the re-check every destructive action
// runs first. A replacement that keeps the size and the modification time —
// `cp -p`, `rsync -a`, `tar -xp`, or a temporary file renamed over the path —
// satisfied the size+mtime comparison on its own, so the action would have
// eliminated a duplicate against content that is no longer there. When the
// replaced file was the group's keeper, the victim being destroyed is the last
// copy of that content.
func TestUnchanged_DetectsReplacedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "file.bin")
	original := bytes.Repeat([]byte("original content "), 4)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	// Build the record the way the scanner does, so the identity fields are the
	// ones production compares.
	scanned, err := checksum.ComputeFileInfo(path, int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	if scanned.Inode == 0 && scanned.Dev == 0 {
		t.Skip("this platform records no file identity for the scan")
	}
	fi := dupe.FileInfo{
		Path:    path,
		Size:    int64(len(original)),
		ModTime: scanned.ModTime,
		Dev:     scanned.Dev,
		Inode:   scanned.Inode,
	}

	if _, ok := unchanged(fi); !ok {
		t.Fatal("a file that was just scanned must verify")
	}

	// Replace it with different bytes of the same size and restore the
	// modification time, the way a metadata-preserving copy would, then rename
	// over the path so the inode changes too.
	replacement := bytes.Repeat([]byte("replacement data "), 4)
	if len(replacement) != len(original) {
		t.Fatalf("the test needs a same-size replacement: %d vs %d", len(replacement), len(original))
	}
	tmp := filepath.Join(dir, "replacement.tmp")
	if writeErr := os.WriteFile(tmp, replacement, 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if timeErr := os.Chtimes(tmp, scanned.ModTime, scanned.ModTime); timeErr != nil {
		t.Fatal(timeErr)
	}
	if renameErr := os.Rename(tmp, path); renameErr != nil {
		t.Fatal(renameErr)
	}

	replaced, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if replaced.Size() != fi.Size || !replaced.ModTime().Equal(fi.ModTime) {
		t.Fatalf("the test did not reproduce a same-size, same-mtime replacement (size %d, mtime %v)",
			replaced.Size(), replaced.ModTime())
	}

	if _, ok := unchanged(fi); ok {
		t.Error("a replaced file whose size and modification time still match must not verify: " +
			"the content the decision was made on is gone")
	}
}
