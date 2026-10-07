package extent_test

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"finddupe/internal/extent"
	"finddupe/internal/fsprobe"
)

// TestSharedWithGroupOverlap covers the physical-range arithmetic of the group
// helper: how much of the first member the second one covers. The fixtures use
// realistic non-zero physical offsets: a zero physical identity means "unknown"
// (see physicalRange) and is skipped rather than treated as device offset 0.
func TestSharedWithGroupOverlap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b []extent.Extent
		want int64
	}{
		{
			name: "partial overlap",
			a:    []extent.Extent{{Physical: 1000, Length: 100}, {Physical: 2000, Length: 100}},
			b:    []extent.Extent{{Physical: 1050, Length: 100}},
			want: 50,
		},
		{
			name: "disjoint",
			a:    []extent.Extent{{Physical: 1000, Length: 100}},
			b:    []extent.Extent{{Physical: 9000, Length: 100}},
			want: 0,
		},
		{
			name: "identical",
			a:    []extent.Extent{{Physical: 1000, Length: 100}, {Physical: 2000, Length: 100}},
			b:    []extent.Extent{{Physical: 1000, Length: 100}, {Physical: 2000, Length: 100}},
			want: 200,
		},
		{
			name: "empty",
			a:    nil,
			b:    []extent.Extent{{Physical: 1000, Length: 100}},
			want: 0,
		},
		{
			name: "multiple overlaps",
			a:    []extent.Extent{{Physical: 1000, Length: 100}, {Physical: 1300, Length: 100}},
			b:    []extent.Extent{{Physical: 1050, Length: 200}, {Physical: 1350, Length: 10}},
			want: 60,
		},
		{
			name: "encoded extents without a physical start are ignored",
			a:    []extent.Extent{{Logical: 0, Length: 100, Encoded: true}},
			b:    []extent.Extent{{Logical: 0, Length: 100, Encoded: true}},
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extent.SharedWithGroup([][]extent.Extent{tc.a, tc.b})
			if len(got) != 2 || got[0] != tc.want {
				t.Fatalf("SharedWithGroup = %v, want first member %d", got, tc.want)
			}
		})
	}
}

// randomData returns incompressible bytes so filesystem compression cannot
// distort the physical extent layout.
func randomData(t *testing.T, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return data
}

// queryOrSkip queries extents, skipping the test when the filesystem cannot
// report them.
func queryOrSkip(t *testing.T, path string) []extent.Extent {
	t.Helper()
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat %s: %v", path, statErr)
	}
	extents, err := extent.Query(path, info.Size())
	if err != nil {
		if errors.Is(err, extent.ErrUnsupported) {
			t.Skipf("extent query unsupported: %v", err)
		}
		t.Fatalf("query %s: %v", path, err)
	}
	return extents
}

// extentProbe reports whether extent queries work on dir's filesystem by querying a
// real file: the answer is what the tests below depend on, not the filesystem type.
func extentProbe(t *testing.T, dir string, data []byte) bool {
	t.Helper()

	probe := filepath.Join(dir, "extent-probe.bin")
	if err := os.WriteFile(probe, data, 0o644); err != nil {
		return false
	}
	defer os.Remove(probe)

	_, err := extent.Query(probe, int64(len(data)))
	return err == nil
}

// extentCapableDir returns a temporary directory whose filesystem supports extent
// queries, skipping the test when none is available (the default temp dir is often
// tmpfs, which cannot answer them).
func extentCapableDir(t *testing.T, probeData []byte) string {
	t.Helper()

	return fsprobe.CapableDir(t, "extent queries", func(dir string) bool {
		return extentProbe(t, dir, probeData)
	})
}

// TestQuery_SparseFileIsNotUnsupported verifies that a file with nothing
// allocated is reported as "nothing is shared" rather than as "this filesystem
// cannot report extents". btrfs answers EOPNOTSUPP for a fully sparse file, which
// used to make find --cow claim the filesystem was unsupported while it was
// querying every other file on the volume just fine.
func TestQuery_SparseFileIsNotUnsupported(t *testing.T) {
	t.Parallel()

	data := randomData(t, 64*1024)
	dir := extentCapableDir(t, data)

	sparse := filepath.Join(dir, "sparse.bin")
	f, err := os.Create(sparse)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if truncErr := f.Truncate(1 << 20); truncErr != nil {
		_ = f.Close()
		t.Skipf("cannot create a sparse file: %v", truncErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	extents, err := extent.Query(sparse, 1<<20)
	if err != nil {
		t.Fatalf("a sparse file on a supported filesystem must not be reported as unsupported: %v", err)
	}
	if len(extents) != 0 {
		t.Fatalf("sparse file reported %d extents, want none", len(extents))
	}
}

func TestQuery_HardlinksShareExtents(t *testing.T) {
	t.Parallel()

	data := randomData(t, 64*1024)
	dir := extentCapableDir(t, data)

	orig := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(orig, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "b.bin")
	if err := os.Link(orig, link); err != nil {
		t.Fatalf("link: %v", err)
	}

	a := queryOrSkip(t, orig)
	b := queryOrSkip(t, link)

	if got := extent.SharedWithGroup([][]extent.Extent{a, b}); got[0] != int64(len(data)) {
		t.Fatalf("hardlinked files share %d bytes, want %d", got[0], len(data))
	}
}

func TestQuery_IndependentCopiesShareNothing(t *testing.T) {
	t.Parallel()

	data := randomData(t, 64*1024)
	dir := extentCapableDir(t, data)

	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	if err := os.WriteFile(a, data, 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(b, data, 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}

	ea := queryOrSkip(t, a)
	eb := queryOrSkip(t, b)

	if got := extent.SharedWithGroup([][]extent.Extent{ea, eb}); got[0] != 0 {
		t.Fatalf("independent copies share %d bytes, want 0", got[0])
	}
}

// TestQuery_LengthsClampedToFileSize is the regression test for a reported
// sharing ratio above 100%: filesystems allocate whole blocks, so the last
// extent of a file whose size is not block-aligned extends past the end of the
// file unless it is clamped.
func TestQuery_LengthsClampedToFileSize(t *testing.T) {
	t.Parallel()

	// Deliberately not a multiple of any common block size.
	data := randomData(t, 100*1024+1234)
	dir := extentCapableDir(t, data)

	path := filepath.Join(dir, "odd.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	extents := queryOrSkip(t, path)

	var total uint64
	for _, e := range extents {
		if end := e.Logical + e.Length; end > uint64(info.Size()) {
			t.Errorf("extent %+v extends past the %d-byte file", e, info.Size())
		}
		total += e.Length
	}
	if total > uint64(info.Size()) {
		t.Errorf("extents cover %d bytes of a %d-byte file", total, info.Size())
	}
}

func TestEqual(t *testing.T) {
	t.Parallel()

	base := []extent.Extent{
		{Logical: 0, Physical: 4096, Length: 4096},
		{Logical: 4096, Physical: 8192, Length: 4096},
	}
	same := []extent.Extent{
		{Logical: 0, Physical: 4096, Length: 4096},
		{Logical: 4096, Physical: 8192, Length: 4096},
	}

	if !extent.Equal(base, same) {
		t.Error("identical layouts must compare equal")
	}
	if extent.Equal(base, same[:1]) {
		t.Error("different extent counts must not compare equal")
	}

	shifted := []extent.Extent{
		{Logical: 0, Physical: 4096, Length: 4096},
		{Logical: 4096, Physical: 12288, Length: 4096},
	}
	if extent.Equal(base, shifted) {
		t.Error("different physical starts must not compare equal")
	}

	if extent.Equal(base, nil) || extent.Equal(nil, nil) {
		t.Error("empty lists must not compare equal")
	}

	// Encoded (compressed) extents still have a usable identity: an exact
	// start+length match is sound; only range arithmetic is not.
	encoded := []extent.Extent{{Logical: 0, Physical: 4096, Length: 4096, Encoded: true}}
	if !extent.Equal(encoded, encoded) {
		t.Error("identical encoded extents must compare equal")
	}
	encodedShifted := []extent.Extent{{Logical: 0, Physical: 8192, Length: 4096, Encoded: true}}
	if extent.Equal(encoded, encodedShifted) {
		t.Error("encoded extents with different physical identities must not compare equal")
	}

	zero := []extent.Extent{{Logical: 0, Physical: 0, Length: 4096}}
	if extent.Equal(zero, zero) {
		t.Error("unknown (zero) physical addresses must not compare equal")
	}
}

func TestSharedFlagBytes(t *testing.T) {
	t.Parallel()

	extents := []extent.Extent{
		{Logical: 0, Physical: 4096, Length: 100, Shared: true},
		{Logical: 100, Physical: 8192, Length: 200, Shared: false},
		{Logical: 300, Physical: 12288, Length: 50, Shared: true},
	}
	if got := extent.SharedFlagBytes(extents); got != 150 {
		t.Fatalf("SharedFlagBytes = %d, want 150", got)
	}
}

// TestSharedWithGroup_EncodedWidestOverlap covers three members meeting at one
// physical start with different lengths: an extent's bytes are shared with the
// *union* of the other members, so the widest overlap decides. Counting the
// shortest other extent reported 40/40/40 instead of 70/40/70.
func TestSharedWithGroup_EncodedWidestOverlap(t *testing.T) {
	t.Parallel()

	group := [][]extent.Extent{
		{{Logical: 0, Physical: 100, Length: 100, Encoded: true}},
		{{Logical: 0, Physical: 100, Length: 40, Encoded: true}},
		{{Logical: 0, Physical: 100, Length: 70, Encoded: true}},
	}

	got := extent.SharedWithGroup(group)
	want := []int64{70, 40, 70}
	if !slices.Equal(got, want) {
		t.Fatalf("SharedWithGroup = %v, want %v", got, want)
	}
}

func TestSharedWithGroup(t *testing.T) {
	t.Parallel()

	own := []extent.Extent{
		{Logical: 0, Physical: 4096, Length: 100},
		{Logical: 100, Physical: 8192, Length: 100},
	}
	other := []extent.Extent{
		{Logical: 0, Physical: 4096, Length: 100},    // shared
		{Logical: 100, Physical: 16384, Length: 100}, // not shared
	}

	if got := extent.SharedWithGroup([][]extent.Extent{own, other}); got[0] != 100 || got[1] != 100 {
		t.Fatalf("SharedWithGroup = %v, want [100 100]", got)
	}
	if got := extent.SharedWithGroup([][]extent.Extent{own}); got[0] != 0 {
		t.Fatalf("SharedWithGroup(single) = %v, want [0]", got)
	}
	if got := extent.SharedWithGroup([][]extent.Extent{own, nil}); got[0] != 0 || got[1] != 0 {
		t.Fatalf("SharedWithGroup(nil member) = %v, want [0 0]", got)
	}

	// A shorter other extent caps the counted bytes.
	short := []extent.Extent{{Logical: 0, Physical: 4096, Length: 40}}
	if got := extent.SharedWithGroup([][]extent.Extent{own, short}); got[0] != 40 {
		t.Fatalf("SharedWithGroup(short) = %v, want 40 for the first member", got)
	}

	// A shared run that starts mid-way through own's run is still counted: this
	// is the APFS partial-clone shape, where the untouched tail becomes its own
	// extent starting inside the original's run.
	big := []extent.Extent{{Logical: 0, Physical: 1000, Length: 1044480}}
	tail := []extent.Extent{{Logical: 262144, Physical: 1000 + 258048, Length: 786432}}
	if got := extent.SharedWithGroup([][]extent.Extent{big, tail}); got[0] != 786432 {
		t.Fatalf("SharedWithGroup(mid-run tail) = %v, want 786432 for the original", got)
	}

	// A byte shared with several others is counted once.
	if got := extent.SharedWithGroup([][]extent.Extent{own, other, other}); got[0] != 100 {
		t.Fatalf("SharedWithGroup(duplicated others) = %v, want 100", got)
	}

	// Only the members that really share are credited: the third copy shares with
	// nobody, so it stays at zero while the first two count each other.
	lonely := []extent.Extent{{Logical: 0, Physical: 999999, Length: 100}}
	if got := extent.SharedWithGroup([][]extent.Extent{own, other, lonely}); got[2] != 0 {
		t.Fatalf("SharedWithGroup(unrelated member) = %v, want 0 for the third", got)
	}

	// Encoded (compressed) extents have no comparable physical range, so they fall
	// back to an exact physical start match.
	encoded := []extent.Extent{{Logical: 0, Physical: 4096, Length: 100, Encoded: true}}
	if got := extent.SharedWithGroup([][]extent.Extent{encoded, encoded}); got[0] != 100 {
		t.Fatalf("SharedWithGroup(encoded) = %v, want 100", got)
	}

	// A file that shares both a plain run and an encoded one is credited for both:
	// the encoded start match used to be dropped as soon as any plain overlap was
	// found.
	mixedOwn := []extent.Extent{
		{Logical: 0, Physical: 5000, Length: 100},
		{Logical: 100, Physical: 4096, Length: 100, Encoded: true},
	}
	mixedOther := []extent.Extent{
		{Logical: 0, Physical: 5000, Length: 100},
		{Logical: 100, Physical: 4096, Length: 100},
	}
	if got := extent.SharedWithGroup([][]extent.Extent{mixedOwn, mixedOther}); got[0] != 200 {
		t.Fatalf("SharedWithGroup(mixed plain+encoded) = %v, want 200", got)
	}

	// Opaque keys (the APFS clone ID used for compressed files) are not device
	// offsets: two independent families with adjacent IDs must not be treated as
	// overlapping ranges, while an identical key still means shared.
	opaqueA := []extent.Extent{{Logical: 0, Physical: 52139610, Length: 2097152, Opaque: true}}
	opaqueB := []extent.Extent{{Logical: 0, Physical: 52139611, Length: 2097152, Opaque: true}}
	if got := extent.SharedWithGroup([][]extent.Extent{opaqueA, opaqueB}); got[0] != 0 {
		t.Fatalf("SharedWithGroup(adjacent opaque keys) = %v, want 0", got)
	}
	if got := extent.SharedWithGroup([][]extent.Extent{opaqueA, opaqueA}); got[0] != 2097152 {
		t.Fatalf("SharedWithGroup(same opaque key) = %v, want 2097152", got)
	}
}

// TestEqual_IdentityKind covers the rule that physical identities of different
// kinds are never the same thing: an opaque APFS clone ID can be numerically equal
// to a device offset, and a compressed extent's start does not describe the same
// bytes as a plain extent at that offset. Treating them as equal would skip a clone
// that is actually needed.
func TestEqual_IdentityKind(t *testing.T) {
	t.Parallel()

	plain := []extent.Extent{{Logical: 0, Physical: 4096, Length: 100}}
	opaque := []extent.Extent{{Logical: 0, Physical: 4096, Length: 100, Opaque: true}}
	encoded := []extent.Extent{{Logical: 0, Physical: 4096, Length: 100, Encoded: true}}

	if !extent.Equal(plain, plain) {
		t.Error("identical plain layouts must compare equal")
	}
	for _, tc := range []struct {
		name string
		a, b []extent.Extent
	}{
		{name: "plain vs opaque", a: plain, b: opaque},
		{name: "plain vs encoded", a: plain, b: encoded},
		{name: "opaque vs encoded", a: opaque, b: encoded},
	} {
		if extent.Equal(tc.a, tc.b) {
			t.Errorf("%s: layouts with different kinds of physical identity compared equal", tc.name)
		}
	}
}

// TestQuery_ExtentsCoverTheFile verifies that a written file's extents describe all
// of its bytes, which every sharing ratio is computed from. The macOS query returned
// only its first run (4 KiB of a 1 MiB file) while the request record was not
// rewritten for each step, and `find --cow` then reported 0.4% where the file was
// fully shared.
func TestQuery_ExtentsCoverTheFile(t *testing.T) {
	t.Parallel()

	probe := randomData(t, 1<<20)
	dir := extentCapableDir(t, probe)

	path := filepath.Join(dir, "covered.bin")
	if err := os.WriteFile(path, probe, 0o644); err != nil {
		t.Fatal(err)
	}

	extents, err := extent.Query(path, int64(len(probe)))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	var total int64
	for _, e := range extents {
		total += int64(e.Length)
	}
	if total != int64(len(probe)) {
		t.Errorf("extents cover %d of %d bytes (%d extents): %+v",
			total, len(probe), len(extents), extents)
	}
}

// TestQuery_SubClusterFileIsNotAnError verifies that a file whose data is stored
// resident — anything smaller than a cluster, which NTFS keeps in the file record —
// is reported as having nothing allocated rather than as an error. The Windows query
// fails with a handle-EOF for such a file, which reached the caller as "extent query
// error" for every tiny duplicate.
func TestQuery_SubClusterFileIsNotAnError(t *testing.T) {
	t.Parallel()

	probe := randomData(t, 1<<20)
	dir := extentCapableDir(t, probe)

	small := []byte("tiny")
	path := filepath.Join(dir, "small.bin")
	if err := os.WriteFile(path, small, 0o644); err != nil {
		t.Fatal(err)
	}

	extents, err := extent.Query(path, int64(len(small)))
	if err != nil {
		t.Fatalf("a sub-cluster file must not be an error: %v", err)
	}

	var total int64
	for _, e := range extents {
		total += int64(e.Length)
	}
	if total > int64(len(small)) {
		t.Errorf("extents cover %d bytes of a %d-byte file", total, len(small))
	}
}
