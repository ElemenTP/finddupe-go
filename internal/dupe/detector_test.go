package dupe_test

import (
	"crypto/sha256"
	"testing"

	"finddupe/internal/dupe"
)

// key1 is a reusable composite key for tests.
var key1 = dupe.GroupKey{Signature: 0xABCD, Size: 4096}

// fi builds a FileInfo for the shared test key.
func fi(path string, size int64) dupe.FileInfo {
	return dupe.FileInfo{Path: path, Size: size, Signature: key1.Signature}
}

// shaOf returns a non-zero SHA-256 for the given seed.
func shaOf(seed byte) [32]byte {
	return sha256.Sum256([]byte{seed})
}

// execTypes lists the execution types in order.
func execTypes(execs []dupe.Execution) []dupe.ExecutionType {
	out := make([]dupe.ExecutionType, len(execs))
	for i, ex := range execs {
		out[i] = ex.Type
	}
	return out
}

func TestDetector_FirstInsert(t *testing.T) {
	t.Parallel()

	stats := dupe.NewStats()
	d := dupe.NewDetector(stats)

	if execs := d.Insert(fi("/a", 4096)); len(execs) != 0 {
		t.Fatalf("first insert returned %v, want no executions", execTypes(execs))
	}
	if d.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", d.Len())
	}
	if stats.TotalFiles.Load() != 1 || stats.TotalBytes.Load() != 4096 {
		t.Fatalf("stats = (%d files, %d bytes), want (1, 4096)",
			stats.TotalFiles.Load(), stats.TotalBytes.Load())
	}
}

func TestDetector_TwoUnhashedFiles_EmitHashComp(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	d.Insert(fi("/a", 4096))

	execs := d.Insert(fi("/b", 4096))
	if len(execs) != 1 || execs[0].Type != dupe.HashComp {
		t.Fatalf("second insert = %v, want one HashComp", execTypes(execs))
	}
	if execs[0].Files[0].Path != "/a" || execs[0].Files[1].Path != "/b" {
		t.Fatalf("HashComp files = %q/%q, want /a and /b",
			execs[0].Files[0].Path, execs[0].Files[1].Path)
	}
}

func TestDetector_ThirdUnhashedFile_EmitHashCalc(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	d.Insert(fi("/a", 4096))
	d.Insert(fi("/b", 4096))

	execs := d.Insert(fi("/c", 4096))
	// /a and /b already have a HashComp in flight, so only the new file needs
	// a full hash.
	if len(execs) != 1 || execs[0].Type != dupe.HashCalc {
		t.Fatalf("third insert = %v, want one HashCalc", execTypes(execs))
	}
	if execs[0].Files[0].Path != "/c" {
		t.Fatalf("HashCalc file = %q, want /c", execs[0].Files[0].Path)
	}
}

func TestDetector_KnownShaMatch_EmitDupeElim(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())

	a := fi("/a", 100)
	a.SHA256 = shaOf(1)
	if execs := d.Insert(a); len(execs) != 0 {
		t.Fatalf("first insert returned %v", execTypes(execs))
	}

	b := fi("/b", 100)
	b.SHA256 = shaOf(1)
	execs := d.Insert(b)
	if len(execs) != 1 || execs[0].Type != dupe.DupeElim {
		t.Fatalf("matching insert = %v, want one DupeElim", execTypes(execs))
	}
	if execs[0].Files[0].Path != "/a" || execs[0].Files[1].Path != "/b" {
		t.Fatalf("DupeElim files = %q/%q, want keeper /a and victim /b",
			execs[0].Files[0].Path, execs[0].Files[1].Path)
	}
}

func TestDetector_KnownShaMismatch_NoExec(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())

	a := fi("/a", 100)
	a.SHA256 = shaOf(1)
	d.Insert(a)

	b := fi("/b", 100)
	b.SHA256 = shaOf(2)
	if execs := d.Insert(b); len(execs) != 0 {
		t.Fatalf("mismatching insert = %v, want no executions", execTypes(execs))
	}
}

// TestDetector_ThreeIdenticalFiles_AllReported is the regression test for the
// previously missed duplicates: four identical files must yield exactly three
// eliminations with a single keeper and no victim eliminated twice.
func TestDetector_ThreeIdenticalFiles_AllReported(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	s := shaOf(9)

	d.Insert(fi("/a", 4096))
	if execs := d.Insert(fi("/b", 4096)); len(execs) != 1 || execs[0].Type != dupe.HashComp {
		t.Fatalf("second insert = %v, want HashComp", execTypes(execs))
	}
	if execs := d.Insert(fi("/c", 4096)); len(execs) != 1 || execs[0].Type != dupe.HashCalc {
		t.Fatalf("third insert = %v, want HashCalc", execTypes(execs))
	}

	// The pair comparison completes with both hashes equal.
	a := fi("/a", 4096)
	a.SHA256 = s
	b := fi("/b", 4096)
	b.SHA256 = s
	execs := d.OnCompareDone(key1, a, b)
	if len(execs) != 1 || execs[0].Type != dupe.DupeElim {
		t.Fatalf("OnCompareDone = %v, want one DupeElim", execTypes(execs))
	}
	if execs[0].Files[1].Path != "/b" {
		t.Fatalf("first victim = %q, want /b", execs[0].Files[1].Path)
	}

	// The third file's full hash completes and matches the keeper /a.
	c := fi("/c", 4096)
	c.SHA256 = s
	execs = d.OnHashDone(key1, c)
	if len(execs) != 1 || execs[0].Type != dupe.DupeElim {
		t.Fatalf("OnHashDone(c) = %v, want one DupeElim", execTypes(execs))
	}
	if execs[0].Files[0].Path != "/a" || execs[0].Files[1].Path != "/c" {
		t.Fatalf("second DupeElim = %q/%q, want keeper /a victim /c",
			execs[0].Files[0].Path, execs[0].Files[1].Path)
	}
}

// TestDetector_NoDoubleElimination verifies that an already scheduled victim is
// never handed out a second time.
func TestDetector_NoDoubleElimination(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	s := shaOf(3)

	a := fi("/a", 100)
	a.SHA256 = s
	d.Insert(a)

	b := fi("/b", 100)
	b.SHA256 = s
	d.Insert(b)

	// Re-feeding /b (e.g. via a duplicate completion event) must not emit.
	if execs := d.OnHashDone(key1, b); len(execs) != 0 {
		t.Fatalf("re-feeding scheduled victim = %v, want none", execTypes(execs))
	}
}

func TestDetector_PartialProgressPreserved(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	d.Insert(fi("/a", 4096))
	d.Insert(fi("/b", 4096))

	// The comparison stopped after 512 bytes with no complete hash.
	a := fi("/a", 4096)
	a.HashOffset = 512
	a.HashState = []byte{1, 2, 3}
	b := fi("/b", 4096)
	b.HashOffset = 512

	if execs := d.OnCompareDone(key1, a, b); len(execs) != 0 {
		t.Fatalf("partial compare = %v, want none", execTypes(execs))
	}

	// A third file arrives, triggering full hashing of the bucket. All three
	// files need a HashCalc because none of them has a complete hash, and the
	// partial progress of /a must be carried into its execution.
	execs := d.Insert(fi("/c", 4096))
	if len(execs) != 3 {
		t.Fatalf("third insert = %v, want three HashCalc executions", execTypes(execs))
	}

	for _, ex := range execs {
		if ex.Type != dupe.HashCalc {
			t.Fatalf("execution type = %v, want HashCalc", ex.Type)
		}
		if ex.Files[0].Path == "/a" && ex.Files[0].HashOffset != 512 {
			t.Fatalf("/a HashOffset = %d, want 512", ex.Files[0].HashOffset)
		}
	}
}

func TestDetector_PartialProgressKeepsLargerOffset(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	d.Insert(fi("/a", 4096))
	d.Insert(fi("/b", 4096))

	a := fi("/a", 4096)
	a.HashOffset = 1024
	a.HashState = []byte{9}
	d.OnCompareDone(key1, a, fi("/b", 4096))

	// Feed a smaller offset: the larger one must win.
	small := fi("/a", 4096)
	small.HashOffset = 100
	if execs := d.OnHashDone(key1, small); len(execs) != 0 {
		t.Fatalf("partial hash = %v, want none", execTypes(execs))
	}
}

func TestDetector_CRCCollisionSeparatesBuckets(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())

	a := fi("/a", 4096)
	a.SHA256 = shaOf(1)
	d.Insert(a)

	b := fi("/b", 4096)
	b.SHA256 = shaOf(2)
	d.Insert(b)

	// A new file matching /b must be eliminated against /b, not /a.
	c := fi("/c", 4096)
	c.SHA256 = shaOf(2)
	execs := d.Insert(c)
	if len(execs) != 1 || execs[0].Files[0].Path != "/b" {
		t.Fatalf("collision match = %v, want keeper /b", execTypes(execs))
	}
}

func TestDetector_CoWDetectMode(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats(), dupe.WithCoWDetect())

	a := fi("/a", 100)
	a.SHA256 = shaOf(1)
	d.Insert(a)

	b := fi("/b", 100)
	b.SHA256 = shaOf(1)
	execs := d.Insert(b)
	if len(execs) != 1 || execs[0].Type != dupe.CoWDetect {
		t.Fatalf("CoW match = %v, want CoWDetect", execTypes(execs))
	}

	// Detection never schedules victims, so a third clone still compares.
	c := fi("/c", 100)
	c.SHA256 = shaOf(1)
	execs = d.Insert(c)
	if len(execs) != 1 || execs[0].Type != dupe.CoWDetect || execs[0].Files[0].Path != "/a" {
		t.Fatalf("third CoW match = %v, want CoWDetect against /a", execTypes(execs))
	}
}

func TestDetector_InsertInodeGroups(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())

	linked := func(path string, dev, inode, links uint64) dupe.FileInfo {
		return dupe.FileInfo{Path: path, Size: 10, Dev: dev, Inode: inode, NumLinks: links}
	}

	d.InsertInode(linked("/a", 1, 42, 2))
	d.InsertInode(linked("/b", 1, 42, 2))
	d.InsertInode(linked("/c", 1, 42, 1)) // single link, ignored
	d.InsertInode(linked("/d", 2, 42, 2)) // same inode on another device
	d.InsertInode(linked("/e", 2, 42, 2))

	groups := d.InodeGroups()
	if len(groups) != 2 {
		t.Fatalf("InodeGroups() = %d groups, want 2", len(groups))
	}

	for _, g := range groups {
		if len(g) != 2 {
			t.Fatalf("group size = %d, want 2", len(g))
		}
		if g[0].Dev != g[1].Dev || g[0].Inode != g[1].Inode {
			t.Fatalf("group mixes identities: %+v", g)
		}
		if g[0].NumLinks < 2 {
			t.Fatalf("group contains a single-link file: %+v", g)
		}
	}
}

func TestDetector_Empty(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	if d.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", d.Len())
	}
	if groups := d.InodeGroups(); len(groups) != 0 {
		t.Fatalf("InodeGroups() = %d, want 0", len(groups))
	}
	if d.Stats() == nil {
		t.Fatal("Stats() returned nil")
	}
}

// TestDetector_SamePathInsertedTwice guards against overlapping patterns making
// a file a duplicate of itself (which would eliminate it in dedupe mode).
func TestDetector_SamePathInsertedTwice(t *testing.T) {
	t.Parallel()

	stats := dupe.NewStats()
	d := dupe.NewDetector(stats)
	s := shaOf(11)

	a := fi("/a", 100)
	a.SHA256 = s
	if execs := d.Insert(a); len(execs) != 0 {
		t.Fatalf("first insert = %v, want none", execTypes(execs))
	}

	// The same path again, even with a matching hash, must be ignored.
	if execs := d.Insert(a); len(execs) != 0 {
		t.Fatalf("duplicate insert = %v, want no executions", execTypes(execs))
	}
	if stats.TotalFiles.Load() != 1 {
		t.Fatalf("TotalFiles = %d, want 1", stats.TotalFiles.Load())
	}
}
