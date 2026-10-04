package extent_test

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"finddupe/internal/extent"
)

func TestSharedBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b []extent.Extent
		want int64
	}{
		{
			name: "partial overlap",
			a:    []extent.Extent{{Physical: 0, Length: 100}, {Physical: 200, Length: 100}},
			b:    []extent.Extent{{Physical: 50, Length: 100}},
			want: 50,
		},
		{
			name: "disjoint",
			a:    []extent.Extent{{Physical: 0, Length: 100}},
			b:    []extent.Extent{{Physical: 1000, Length: 100}},
			want: 0,
		},
		{
			name: "identical",
			a:    []extent.Extent{{Physical: 0, Length: 100}, {Physical: 200, Length: 100}},
			b:    []extent.Extent{{Physical: 0, Length: 100}, {Physical: 200, Length: 100}},
			want: 200,
		},
		{
			name: "empty",
			a:    nil,
			b:    []extent.Extent{{Physical: 0, Length: 100}},
			want: 0,
		},
		{
			name: "multiple overlaps",
			a:    []extent.Extent{{Physical: 0, Length: 100}, {Physical: 300, Length: 100}},
			b:    []extent.Extent{{Physical: 50, Length: 200}, {Physical: 350, Length: 10}},
			want: 60,
		},
		{
			name: "encoded independent extents are ignored",
			a:    []extent.Extent{{Logical: 0, Length: 100, Encoded: true}},
			b:    []extent.Extent{{Logical: 0, Length: 100, Encoded: true}},
			want: 0,
		},
		{
			name: "shared logical fallback for encoded extents",
			a:    []extent.Extent{{Logical: 0, Length: 100, Encoded: true, Shared: true}},
			b:    []extent.Extent{{Logical: 0, Length: 100, Encoded: true, Shared: true}},
			want: 100,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := extent.SharedBytes(tc.a, tc.b); got != tc.want {
				t.Fatalf("SharedBytes = %d, want %d", got, tc.want)
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
	extents, err := extent.Query(path)
	if err != nil {
		if errors.Is(err, extent.ErrUnsupported) {
			t.Skipf("extent query unsupported: %v", err)
		}
		t.Fatalf("query %s: %v", path, err)
	}
	return extents
}

// repoTestDir creates a temp dir in the package working directory, which is
// often on the developer's real btrfs/XFS/APFS volume when /tmp is tmpfs.
func repoTestDir(t *testing.T) string {
	t.Helper()
	//nolint:usetesting // the default temp dir may be on a filesystem without CoW
	dir, err := os.MkdirTemp(".", "extent-fs-test-")
	if err != nil {
		return ""
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// extentCapableDir returns a temporary directory whose filesystem supports
// extent queries, skipping the test when none is available. It first tries the
// default temp dir (t.TempDir(), i.e. $TMPDIR, often tmpfs) and then a directory
// inside the package working directory, which is often on the developer's real
// btrfs/XFS/APFS volume.
func extentCapableDir(t *testing.T, probeData []byte) string {
	t.Helper()

	candidates := []string{t.TempDir()}
	if dir := repoTestDir(t); dir != "" {
		candidates = append(candidates, dir)
	}

	for _, dir := range candidates {
		probe := filepath.Join(dir, "probe.bin")
		if err := os.WriteFile(probe, probeData, 0o644); err != nil {
			continue
		}
		_, err := extent.Query(probe)
		_ = os.Remove(probe)
		if err == nil {
			return dir
		}
	}

	t.Skip("extent queries unsupported by the default temp dir and the repository filesystem")
	return ""
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

	if got := extent.SharedBytes(a, b); got != int64(len(data)) {
		t.Fatalf("hardlinked files share %d bytes, want %d", got, len(data))
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

	if got := extent.SharedBytes(ea, eb); got != 0 {
		t.Fatalf("independent copies share %d bytes, want 0", got)
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

func TestSharedWithOthers(t *testing.T) {
	t.Parallel()

	own := []extent.Extent{
		{Logical: 0, Physical: 4096, Length: 100},
		{Logical: 100, Physical: 8192, Length: 100},
	}
	other := []extent.Extent{
		{Logical: 0, Physical: 4096, Length: 100},    // shared
		{Logical: 100, Physical: 16384, Length: 100}, // not shared
	}

	if got := extent.SharedWithOthers(own, [][]extent.Extent{other}); got != 100 {
		t.Fatalf("SharedWithOthers = %d, want 100", got)
	}
	if got := extent.SharedWithOthers(own, nil); got != 0 {
		t.Fatalf("SharedWithOthers(nil) = %d, want 0", got)
	}

	// A shorter other extent caps the counted bytes.
	short := []extent.Extent{{Logical: 0, Physical: 4096, Length: 40}}
	if got := extent.SharedWithOthers(own, [][]extent.Extent{short}); got != 40 {
		t.Fatalf("SharedWithOthers(short) = %d, want 40", got)
	}

	// A shared run that starts mid-way through own's run is still counted: this
	// is the APFS partial-clone shape, where the untouched tail becomes its own
	// extent starting inside the original's run.
	big := []extent.Extent{{Logical: 0, Physical: 1000, Length: 1044480}}
	tail := []extent.Extent{{Logical: 262144, Physical: 1000 + 258048, Length: 786432}}
	if got := extent.SharedWithOthers(big, [][]extent.Extent{tail}); got != 786432 {
		t.Fatalf("SharedWithOthers(mid-run tail) = %d, want 786432", got)
	}

	// A byte shared with several others is counted once.
	if got := extent.SharedWithOthers(own, [][]extent.Extent{other, other}); got != 100 {
		t.Fatalf("SharedWithOthers(duplicated others) = %d, want 100", got)
	}

	// Encoded (compressed) extents have no comparable physical range, so they
	// fall back to an exact physical start match.
	encoded := []extent.Extent{{Logical: 0, Physical: 4096, Length: 100, Encoded: true}}
	if got := extent.SharedWithOthers(encoded, [][]extent.Extent{encoded}); got != 100 {
		t.Fatalf("SharedWithOthers(encoded) = %d, want 100", got)
	}

	// Opaque keys (the APFS clone ID used for compressed files) are not device
	// offsets: two independent families with adjacent IDs must not be treated as
	// overlapping ranges, while an identical key still means shared.
	opaqueA := []extent.Extent{{Logical: 0, Physical: 52139610, Length: 2097152, Opaque: true}}
	opaqueB := []extent.Extent{{Logical: 0, Physical: 52139611, Length: 2097152, Opaque: true}}
	if got := extent.SharedWithOthers(opaqueA, [][]extent.Extent{opaqueB}); got != 0 {
		t.Fatalf("SharedWithOthers(adjacent opaque keys) = %d, want 0", got)
	}
	if got := extent.SharedWithOthers(opaqueA, [][]extent.Extent{opaqueA}); got != 2097152 {
		t.Fatalf("SharedWithOthers(same opaque key) = %d, want 2097152", got)
	}
}
