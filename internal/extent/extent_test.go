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

func TestQuery_HardlinksShareExtents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := randomData(t, 64*1024)

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

	dir := t.TempDir()
	data := randomData(t, 64*1024)

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
