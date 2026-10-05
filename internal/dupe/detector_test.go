package dupe_test

import (
	"crypto/sha256"
	"sync"
	"testing"

	"finddupe/internal/dupe"
)

// key1 is a reusable composite key for tests.
var key1 = dupe.GroupKey{Signature: 0xABCD, Size: 4096}

// fi builds a FileInfo for the shared test key.
func fi(path string, size int64) dupe.FileInfo {
	return dupe.FileInfo{Path: path, Size: size, Signature: key1.Signature}
}

// fiSha builds a fully hashed FileInfo for the shared test key.
func fiSha(path string, size int64, seed byte) dupe.FileInfo {
	out := fi(path, size)
	out.SHA256 = shaOf(seed)
	out.HashOffset = size
	return out
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

// finalAll drains the detector's end-of-scan work. It must only be used when the
// emitted work needs no simulation (every file has a known SHA-256).
func finalAll(t *testing.T, d *dupe.Detector) []dupe.Execution {
	t.Helper()

	var out []dupe.Execution
	for range 1000 {
		execs, done := d.NextFinal(1024)
		out = append(out, execs...)
		if done {
			return out
		}
	}
	t.Fatal("NextFinal never reported completion")
	return nil
}

// victimsOf returns the victim paths of the DupeElim executions, and the set of
// distinct keepers.
func victimsOf(execs []dupe.Execution) ([]string, map[string]bool) {
	var victims []string
	keepers := make(map[string]bool)
	for _, ex := range execs {
		if ex.Type != dupe.DupeElim {
			continue
		}
		keepers[ex.Files[0].Path] = true
		victims = append(victims, ex.Files[1].Path)
	}
	return victims, keepers
}

func TestDetector_FirstInsert(t *testing.T) {
	t.Parallel()

	stats := dupe.NewStats()
	d := dupe.NewDetector(stats)

	if execs := d.Insert(fi("/a", 4096)); len(execs) != 0 {
		t.Fatalf("first insert returned %v, want no executions", execTypes(execs))
	}
	// The group the insert registered is observed through the stats and the
	// executions it produces; there is no accessor for the detector's own map.
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
	// Only the new file is scheduled: Insert stays O(1) in the group size, and
	// files an early-stopped comparison left behind are completed by NextFinal.
	if len(execs) != 1 || execs[0].Type != dupe.HashCalc {
		t.Fatalf("third insert = %v, want one HashCalc", execTypes(execs))
	}
	if execs[0].Files[0].Path != "/c" {
		t.Fatalf("HashCalc file = %q, want /c", execs[0].Files[0].Path)
	}
}

func TestDetector_KnownShaMatch_DeferredToFinal(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	if execs := d.Insert(fiSha("/a", 100, 1)); len(execs) != 0 {
		t.Fatalf("first insert returned %v", execTypes(execs))
	}

	// Inserting a duplicate does not decide anything: the keeper is chosen once
	// the whole group is known.
	if execs := d.Insert(fiSha("/b", 100, 1)); len(execs) != 0 {
		t.Fatalf("matching insert = %v, want no executions during the scan", execTypes(execs))
	}

	execs := finalAll(t, d)
	if len(execs) != 1 || execs[0].Type != dupe.DupeElim {
		t.Fatalf("final work = %v, want one DupeElim", execTypes(execs))
	}
	if execs[0].Files[0].Path != "/a" || execs[0].Files[1].Path != "/b" {
		t.Fatalf("DupeElim files = %q/%q, want keeper /a and victim /b",
			execs[0].Files[0].Path, execs[0].Files[1].Path)
	}
}

func TestDetector_KnownShaMismatch_NoExec(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	d.Insert(fiSha("/a", 100, 1))
	d.Insert(fiSha("/b", 100, 2))

	if execs := finalAll(t, d); len(execs) != 0 {
		t.Fatalf("mismatching group produced %v, want no executions", execTypes(execs))
	}
}

// TestDetector_ThreeIdenticalFiles_AllReported verifies that identical files
// yield one elimination per extra member, all against the same keeper.
func TestDetector_ThreeIdenticalFiles_AllReported(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	for _, path := range []string{"/a", "/b", "/c"} {
		d.Insert(fiSha(path, 100, 9))
	}

	execs := finalAll(t, d)
	victims, keepers := victimsOf(execs)
	if len(victims) != 2 {
		t.Fatalf("victims = %v, want two eliminations", victims)
	}
	if len(keepers) != 1 {
		t.Fatalf("keepers = %v, want exactly one keeper", keepers)
	}
	if !keepers["/a"] {
		t.Fatalf("keeper = %v, want the smallest path /a", keepers)
	}
	if victims[0] == victims[1] {
		t.Fatalf("victim %q eliminated twice", victims[0])
	}
}

// TestDetector_EarlyStoppedCompareIsCompleted is the regression test for the
// missed duplicates: a comparison that stops early leaves both files without a
// SHA-256, and a third file that finishes hashing in the meantime used to
// condemn them to never being hashed again — so a genuine duplicate between the
// stranded file and the finished one was never reported.
func TestDetector_EarlyStoppedCompareIsCompleted(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())

	a, b := fi("/a", 4096), fi("/b", 4096)
	d.Insert(a)
	d.Insert(b)
	d.Insert(fi("/c", 4096))

	partialA := a
	partialA.HashOffset = 1024
	partialB := b
	partialB.HashOffset = 1024
	d.OnCompareDone(key1, partialA, partialB, false)
	d.OnHashDone(key1, fiSha("/c", 4096, 7), false)

	// The detector must ask for the two stranded files to be hashed; simulate the
	// executor: /a is identical to /c, /b differs from both.
	var hashed []string
	var decided []dupe.Execution
	for range 10 {
		execs, done := d.NextFinal(8)
		for _, ex := range execs {
			if ex.Type != dupe.HashCalc {
				decided = append(decided, ex)
				continue
			}
			hashed = append(hashed, ex.Files[0].Path)
			sha := byte(7)
			if ex.Files[0].Path == "/b" {
				sha = 8
			}
			d.OnHashDone(key1, fiSha(ex.Files[0].Path, 4096, sha), false)
		}
		if done {
			break
		}
	}

	if len(hashed) != 2 {
		t.Fatalf("hashed %v, want the two stranded files /a and /b", hashed)
	}

	victims, keepers := victimsOf(decided)
	if len(victims) != 1 || victims[0] != "/c" {
		t.Fatalf("victims = %v, want the duplicate /c eliminated against /a", victims)
	}
	if !keepers["/a"] {
		t.Fatalf("keeper = %v, want /a", keepers)
	}
}

// TestDetector_SettledPairNotHashed verifies that the early-stop optimisation
// survives: a two-file comparison that proves the files differ leaves nothing
// for finalization to do.
func TestDetector_SettledPairNotHashed(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	a, b := fi("/a", 4096), fi("/b", 4096)
	d.Insert(a)
	d.Insert(b)

	partialA, partialB := a, b
	partialA.HashOffset, partialB.HashOffset = 1024, 1024
	d.OnCompareDone(key1, partialA, partialB, false)

	execs, done := d.NextFinal(8)
	if len(execs) != 0 || !done {
		t.Fatalf("NextFinal = (%v, %v), want no work and completion", execTypes(execs), done)
	}
}

// TestDetector_FailedCompareIsRetried verifies that a comparison which could not
// run (an I/O error, not a verdict) leads to a full hash attempt instead of being
// silently treated as "different", and that unhashable files stop being retried.
func TestDetector_FailedCompareIsRetried(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	a, b := fi("/a", 4096), fi("/b", 4096)
	d.Insert(a)
	d.Insert(b)

	d.OnCompareDone(key1, a, b, true)

	attempts := 0
	for range 20 {
		execs, done := d.NextFinal(8)
		if len(execs) == 0 {
			if done {
				break
			}
			continue
		}
		for _, ex := range execs {
			if ex.Type != dupe.HashCalc {
				t.Fatalf("unexpected work %v", execTypes(execs))
			}
			attempts++
			// The file cannot be hashed: report it as still incomplete.
			d.OnHashDone(key1, ex.Files[0], true)
		}
	}

	if attempts == 0 {
		t.Fatal("a failed comparison must lead to a hash attempt")
	}
	if attempts > 4 {
		t.Fatalf("unhashable files were retried %d times, want at most 2 per file", attempts)
	}
}

func TestDetector_KeeperPolicyPrefersReference(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	// The reference arrives second, which used to be irrelevant for any file
	// larger than the scan-time checksum window (its hash was zero at insert).
	d.Insert(fiSha("/work/copy", 100, 3))
	ref := fiSha("/ref/orig", 100, 3)
	ref.IsRef = true
	d.Insert(ref)

	execs := finalAll(t, d)
	if len(execs) != 1 {
		t.Fatalf("final work = %v, want one DupeElim", execTypes(execs))
	}
	if got := execs[0].Files[0].Path; got != "/ref/orig" {
		t.Fatalf("keeper = %q, want the reference /ref/orig", got)
	}
	if got := execs[0].Files[1].Path; got != "/work/copy" {
		t.Fatalf("victim = %q, want the non-reference copy", got)
	}
}

func TestDetector_KeeperPolicyPrefersMoreHardlinks(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	single := fiSha("/a", 100, 3)
	single.NumLinks = 1
	multi := fiSha("/b", 100, 3)
	multi.NumLinks = 4
	d.Insert(single)
	d.Insert(multi)

	execs := finalAll(t, d)
	if len(execs) != 1 {
		t.Fatalf("final work = %v, want one DupeElim", execTypes(execs))
	}
	// Keeping the file with more links means the victim is the one whose
	// removal actually frees storage.
	if got := execs[0].Files[0].Path; got != "/b" {
		t.Fatalf("keeper = %q, want /b (4 hardlinks)", got)
	}
}

func TestDetector_KeeperPolicyIsStableRegardlessOfInsertOrder(t *testing.T) {
	t.Parallel()

	for _, order := range [][]string{{"/b", "/a"}, {"/a", "/b"}} {
		d := dupe.NewDetector(dupe.NewStats())
		for _, path := range order {
			d.Insert(fiSha(path, 100, 5))
		}

		execs := finalAll(t, d)
		if len(execs) != 1 {
			t.Fatalf("order %v: final work = %v, want one DupeElim", order, execTypes(execs))
		}
		if got := execs[0].Files[0].Path; got != "/a" {
			t.Fatalf("order %v: keeper = %q, want /a", order, got)
		}
	}
}

// customPolicy keeps the lexicographically largest path.
type customPolicy struct{}

func (customPolicy) Less(a, b dupe.FileInfo) bool { return a.Path > b.Path }

func TestDetector_CustomKeeperPolicy(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats(), dupe.WithKeeperPolicy(customPolicy{}))
	d.Insert(fiSha("/a", 100, 5))
	d.Insert(fiSha("/b", 100, 5))

	execs := finalAll(t, d)
	if len(execs) != 1 {
		t.Fatalf("final work = %v, want one DupeElim", execTypes(execs))
	}
	if got := execs[0].Files[0].Path; got != "/b" {
		t.Fatalf("keeper = %q, want /b with the custom policy", got)
	}
}

// TestDetector_CompressionPreference verifies that a compressed member is kept
// when the preference is enabled, ahead of the path order that would otherwise
// win, and that the probe is consulted once per member.
func TestDetector_CompressionPreference(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats(), dupe.WithCompressionPreference(func(fi dupe.FileInfo) bool {
		return fi.Path == "/z-compressed"
	}))
	d.Insert(fiSha("/a", 100, 5))
	d.Insert(fiSha("/z-compressed", 100, 5))

	execs := finalAll(t, d)
	if len(execs) != 1 {
		t.Fatalf("final work = %v, want one DupeElim", execTypes(execs))
	}
	if got := execs[0].Files[0].Path; got != "/z-compressed" {
		t.Fatalf("keeper = %q, want the compressed member", got)
	}
	if got := execs[0].Files[1].Path; got != "/a" {
		t.Fatalf("victim = %q, want /a", got)
	}
}

// TestDetector_CompressionPreferenceIsNotProbedPerComparison verifies that the
// probe runs once per member, not once per comparison: a sort that probed on
// every comparison would open and query the same files O(n log n) times.
func TestDetector_CompressionPreferenceIsNotProbedPerComparison(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	calls := make(map[string]int)
	d := dupe.NewDetector(dupe.NewStats(), dupe.WithCompressionPreference(func(fi dupe.FileInfo) bool {
		mu.Lock()
		defer mu.Unlock()
		calls[fi.Path]++
		return false
	}))

	paths := []string{"/a", "/b", "/c", "/d", "/e", "/f", "/g", "/h"}
	for _, path := range paths {
		d.Insert(fiSha(path, 100, 5))
	}
	finalAll(t, d)

	mu.Lock()
	defer mu.Unlock()
	for _, path := range paths {
		if calls[path] != 1 {
			t.Errorf("probe called %d times for %s, want 1", calls[path], path)
		}
	}
}

// fixedChooser keeps a named path when it is a member of the group.
type fixedChooser struct {
	keep string
}

func (c fixedChooser) Choose(members []dupe.FileInfo) (dupe.FileInfo, bool) {
	for _, member := range members {
		if member.Path == c.keep {
			return member, true
		}
	}
	return dupe.FileInfo{}, false
}

// TestDetector_KeeperChooser verifies that the chooser decides the keeper instead
// of the policy, that declining a group leaves it alone, and that an answer which
// is not one of the members is refused.
func TestDetector_KeeperChooser(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats(), dupe.WithKeeperChooser(fixedChooser{keep: "/c"}))
	for _, path := range []string{"/a", "/b", "/c"} {
		d.Insert(fiSha(path, 100, 5))
	}

	chosen := finalAll(t, d)
	victims, keepers := victimsOf(chosen)
	if len(victims) != 2 || !keepers["/c"] {
		t.Fatalf("victims=%v keepers=%v, want /c kept", victims, keepers)
	}
	if victims[0] == "/c" || victims[1] == "/c" {
		t.Fatalf("the chosen keeper was eliminated: %v", victims)
	}

	// Declining leaves the group untouched.
	declined := dupe.NewDetector(dupe.NewStats(), dupe.WithKeeperChooser(fixedChooser{keep: "/not-there"}))
	declined.Insert(fiSha("/a", 100, 5))
	declined.Insert(fiSha("/b", 100, 5))
	if declinedExecs := finalAll(t, declined); len(declinedExecs) != 0 {
		t.Fatalf("a declined group produced %v, want nothing", execTypes(declinedExecs))
	}
}

// declineChooser refuses every group.
type declineChooser struct{}

func (declineChooser) Choose([]dupe.FileInfo) (dupe.FileInfo, bool) {
	return dupe.FileInfo{}, false
}

// TestDetector_KeeperChooserDeclines verifies the explicit "leave it alone" answer.
func TestDetector_KeeperChooserDeclines(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats(), dupe.WithKeeperChooser(declineChooser{}))
	d.Insert(fiSha("/a", 100, 5))
	d.Insert(fiSha("/b", 100, 5))

	if execs := finalAll(t, d); len(execs) != 0 {
		t.Fatalf("declined group produced %v, want nothing", execTypes(execs))
	}
}

// TestDetector_KeeperChooserCoWDetect verifies that the CoW group is handed over
// with the chosen keeper first, which is the clone source.
func TestDetector_KeeperChooserCoWDetect(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(
		dupe.NewStats(),
		dupe.WithCoWDetect(),
		dupe.WithKeeperChooser(fixedChooser{keep: "/c"}),
	)
	for _, path := range []string{"/a", "/b", "/c"} {
		d.Insert(fiSha(path, 100, 5))
	}

	execs := finalAll(t, d)
	if len(execs) != 1 || execs[0].Type != dupe.CoWDetect {
		t.Fatalf("final work = %v, want one CoWDetect", execTypes(execs))
	}
	if got := execs[0].Files[0].Path; got != "/c" {
		t.Fatalf("clone source = %q, want the chosen /c", got)
	}
	if len(execs[0].Files) != 3 {
		t.Fatalf("CoWDetect carries %d members, want 3", len(execs[0].Files))
	}
}

// TestDetector_FinalBatchesAreBounded verifies that a large group is emitted in
// bounded batches instead of materializing every elimination task at once.
func TestDetector_FinalBatchesAreBounded(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	for _, path := range []string{"/a", "/b", "/c", "/d", "/e"} {
		d.Insert(fiSha(path, 100, 5))
	}

	total := 0
	calls := 0
	for range 20 {
		execs, done := d.NextFinal(2)
		calls++
		if len(execs) > 2 {
			t.Fatalf("NextFinal(2) returned %d executions", len(execs))
		}
		total += len(execs)
		if done {
			break
		}
	}
	if total != 4 {
		t.Fatalf("emitted %d eliminations, want 4", total)
	}
	if calls < 3 {
		t.Fatalf("expected the group to be emitted over several calls, got %d", calls)
	}
}

func TestDetector_CoWDetectMode(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats(), dupe.WithCoWDetect())
	for _, path := range []string{"/b", "/a", "/c"} {
		d.Insert(fiSha(path, 100, 5))
	}

	execs := finalAll(t, d)
	if len(execs) != 1 || execs[0].Type != dupe.CoWDetect {
		t.Fatalf("final work = %v, want one CoWDetect", execTypes(execs))
	}
	if len(execs[0].Files) != 3 {
		t.Fatalf("CoWDetect files = %d, want all three members", len(execs[0].Files))
	}
	// The group is handed over in keeper order, so the first entry is the keeper.
	if got := execs[0].Files[0].Path; got != "/a" {
		t.Fatalf("first member = %q, want /a", got)
	}
}

// TestDetector_HardlinkedAliasesCollapse verifies that two paths to the same
// inode are never eliminated against each other, and that the surviving path is
// the one the keeper policy prefers.
func TestDetector_HardlinkedAliasesCollapse(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())

	aliasA := fiSha("/data/a", 100, 5)
	aliasA.Dev, aliasA.Inode = 1, 42
	aliasB := fiSha("/data/z-alias", 100, 5)
	aliasB.Dev, aliasB.Inode = 1, 42
	refAlias := fiSha("/ref/a", 100, 5)
	refAlias.Dev, refAlias.Inode = 1, 42
	refAlias.IsRef = true
	other := fiSha("/data/other", 100, 5)
	other.Dev, other.Inode = 1, 43

	d.Insert(aliasA)
	d.Insert(aliasB)
	d.Insert(refAlias)
	d.Insert(other)

	execs := finalAll(t, d)
	if len(execs) != 1 {
		t.Fatalf("final work = %v, want exactly one DupeElim", execTypes(execs))
	}
	// The reference alias survives the collapse and is the keeper.
	if got := execs[0].Files[0].Path; got != "/ref/a" {
		t.Fatalf("keeper = %q, want the collapsed reference /ref/a", got)
	}
}

func TestDetector_CRCCollisionSeparatesBuckets(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	d.Insert(fiSha("/small", 100, 5))

	// Same signature but a different size: a distinct group.
	big := fiSha("/big", 200, 5)
	d.Insert(big)

	if execs := finalAll(t, d); len(execs) != 0 {
		t.Fatalf("different sizes produced %v, want no executions", execTypes(execs))
	}
}

func TestDetector_PartialProgressKeepsLargerOffset(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	a, b := fi("/a", 4096), fi("/b", 4096)
	d.Insert(a)
	d.Insert(b)

	// Two interrupted attempts for the same file: the more advanced resume state
	// must win, and an interrupted streaming attempt does not consume the
	// finalization retry budget.
	first := a
	first.HashOffset = 512
	d.OnHashDone(key1, first, true)
	second := a
	second.HashOffset = 1024
	d.OnHashDone(key1, second, true)
	stale := a
	stale.HashOffset = 256
	d.OnHashDone(key1, stale, true)

	execs, _ := d.NextFinal(4)
	if len(execs) == 0 {
		t.Fatal("expected the unhashed files to be scheduled")
	}
	seen := false
	for _, ex := range execs {
		if ex.Files[0].Path == "/a" {
			seen = true
			if ex.Files[0].HashOffset != 1024 {
				t.Fatalf("/a resumes at %d, want 1024", ex.Files[0].HashOffset)
			}
		}
	}
	if !seen {
		t.Fatal("/a was not scheduled for hashing")
	}
}

func TestDetector_InsertInodeGroups(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())

	a := fi("/a", 100)
	a.Dev, a.Inode, a.NumLinks = 1, 10, 2
	b := fi("/b", 100)
	b.Dev, b.Inode, b.NumLinks = 1, 10, 2
	c := fi("/c", 100)
	c.Dev, c.Inode, c.NumLinks = 1, 11, 1

	d.InsertInode(a)
	d.InsertInode(b)
	d.InsertInode(c)

	groups := d.InodeGroups()
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("InodeGroups() = %v, want one group of two", groups)
	}
}

func TestDetector_Empty(t *testing.T) {
	t.Parallel()

	d := dupe.NewDetector(dupe.NewStats())
	if execs, done := d.NextFinal(8); len(execs) != 0 || !done {
		t.Fatalf("NextFinal = (%v, %v), want nothing and completion", execTypes(execs), done)
	}
	if groups := d.InodeGroups(); len(groups) != 0 {
		t.Fatalf("InodeGroups() = %v, want none", groups)
	}
}

func TestDetector_SamePathInsertedTwice(t *testing.T) {
	t.Parallel()

	stats := dupe.NewStats()
	d := dupe.NewDetector(stats)
	d.Insert(fiSha("/a", 100, 5))
	d.Insert(fiSha("/a", 100, 5))

	if execs := finalAll(t, d); len(execs) != 0 {
		t.Fatalf("a path inserted twice produced %v, want no executions", execTypes(execs))
	}
	// The second insert must be rejected before it is counted, otherwise the same
	// file would be part of two decisions.
	if stats.TotalFiles.Load() != 1 {
		t.Fatalf("TotalFiles = %d after inserting one path twice, want 1", stats.TotalFiles.Load())
	}
}
