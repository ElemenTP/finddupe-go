package action

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"finddupe/internal/checksum"
	"finddupe/internal/config"
	"finddupe/internal/dupe"
	"finddupe/internal/extent"
	"strconv"
	"time"
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

// TestCloneFile_CrossDeviceIsSkipped verifies the pre-check that keeps a --cow
// run from attempting a clone across a volume boundary at all. The attempt could
// only fail (storage blocks cannot be shared across volumes) and the error was
// reported as "the filesystem does not support CoW", which reads like a
// limitation of the filesystem instead of of the pair. Unknown devices stay
// conservative (see sameDevice) and are still attempted.
func TestCloneFile_CrossDeviceIsSkipped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	keeperPath := filepath.Join(dir, "keeper.bin")
	victimPath := filepath.Join(dir, "victim.bin")
	content := []byte("identical content for the cross-device test")
	for _, path := range []string{keeperPath, victimPath} {
		if writeErr := os.WriteFile(path, content, 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	keeperInfo, err := os.Lstat(keeperPath)
	if err != nil {
		t.Fatal(err)
	}
	victimInfo, err := os.Lstat(victimPath)
	if err != nil {
		t.Fatal(err)
	}

	exec := New(Options{Action: config.ActionCoWClone})
	result, cloneErr := exec.cloneFile(dupe.Execution{
		Type: dupe.DupeElim,
		Files: []dupe.FileInfo{
			{Path: keeperPath, Size: int64(len(content)), Dev: 7, Inode: 1},
			{Path: victimPath, Size: int64(len(content)), Dev: 8, Inode: 2},
		},
	}, keeperInfo, victimInfo)
	if cloneErr != nil {
		t.Fatalf("cloneFile() error = %v, want a skip", cloneErr)
	}
	if result != ResultSkippedCrossDevice {
		t.Fatalf("cloneFile() = %v, want ResultSkippedCrossDevice", result)
	}
	if _, statErr := os.Lstat(victimPath); statErr != nil {
		t.Fatalf("the victim must be left untouched: %v", statErr)
	}
}

// TestPhysicalIdentityMissing covers when a group simply has no answer: a volume
// that describes extents but gives them no physical identity cannot be compared,
// and publishing 0% would claim that nothing is shared. A group with no extents at
// all (fully sparse files) is a real answer — it shares nothing.
func TestPhysicalIdentityMissing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		group [][]extent.Extent
		want  bool
	}{
		{name: "no extents at all", group: [][]extent.Extent{nil, nil}, want: false},
		{name: "extents with identity", group: [][]extent.Extent{{{Physical: 4096, Length: 100}}}, want: false},
		{name: "extents without identity", group: [][]extent.Extent{{{Length: 100}}}, want: true},
		{name: "mixed, one member has identity", group: [][]extent.Extent{
			{{Length: 100}},
			{{Physical: 4096, Length: 100}},
		}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := physicalIdentityMissing(tc.group); got != tc.want {
				t.Errorf("physicalIdentityMissing(%v) = %v, want %v", tc.group, got, tc.want)
			}
		})
	}
}

// TestKeeperLayoutCache covers the cache that keeps a group of n files from
// querying the same keeper n−1 times: an entry only answers for the exact version
// of the file it was stored for, and the map is bounded.
func TestKeeperLayoutCache(t *testing.T) {
	t.Parallel()

	var cache keeperLayoutCache
	extents := []extent.Extent{{Logical: 0, Physical: 4096, Length: 100}}
	modTime := time.Unix(1700000000, 0)
	keeper := dupe.FileInfo{Path: "/data/keeper.bin", Size: 100, ModTime: modTime}

	if _, ok := cache.get(keeper); ok {
		t.Fatal("an empty cache must not answer")
	}

	cache.put(keeper, extents)
	got, ok := cache.get(keeper)
	if !ok || !extent.Equal(got, extents) {
		t.Fatalf("cache get = (%v, %v), want the stored layout", got, ok)
	}

	// A file that changed since it was scanned must not be answered from the old
	// layout: the decision (skip the clone) would be made on storage that no longer
	// holds this content.
	grown := keeper
	grown.Size = 200
	if _, hit := cache.get(grown); hit {
		t.Error("a different size must miss the cache")
	}
	touched := keeper
	touched.ModTime = modTime.Add(time.Second)
	if _, hit := cache.get(touched); hit {
		t.Error("a different modification time must miss the cache")
	}

	// The cache is bounded: filling it past the limit must not grow without end.
	for i := range layoutCacheLimit + 1 {
		cache.put(dupe.FileInfo{Path: filepath.Join("/data", strconv.Itoa(i)), Size: 1}, extents)
	}
	cache.mu.Lock()
	size := len(cache.entries)
	cache.mu.Unlock()
	if size > layoutCacheLimit {
		t.Errorf("cache holds %d entries, want at most %d", size, layoutCacheLimit)
	}
}
