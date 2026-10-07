//go:build linux

package extent //nolint:testpackage // exercises the unexported FIEMAP batch parser

import "testing"

// TestAppendBatch covers the offset arithmetic that decides where the next
// FIEMAP request starts. A batch that does not move past the requested offset
// must not be requested again, or the query loop never ends.
func TestAppendBatch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		extents  []fiemapExtent
		wantLast bool
		wantNext uint64
	}{
		{
			name:     "single run",
			extents:  []fiemapExtent{{Logical: 0, Physical: 4096, Length: 8192}},
			wantNext: 8192,
		},
		{
			name: "last flag is reported",
			extents: []fiemapExtent{
				{Logical: 0, Physical: 4096, Length: 4096},
				{Logical: 4096, Physical: 8192, Length: 4096, Flags: fiemapExtentLast},
			},
			wantLast: true,
			wantNext: 8192,
		},
		{
			name: "zero-length runs do not advance",
			extents: []fiemapExtent{
				{Logical: 0, Physical: 0, Length: 0},
			},
			wantNext: 0,
		},
		{
			name: "out-of-order runs advance to the furthest end",
			extents: []fiemapExtent{
				{Logical: 8192, Physical: 0, Length: 4096},
				{Logical: 0, Physical: 0, Length: 4096},
			},
			wantNext: 12288,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out []Extent
			last, next := appendBatch(&out, tc.extents)
			if last != tc.wantLast {
				t.Errorf("last = %v, want %v", last, tc.wantLast)
			}
			if next != tc.wantNext {
				t.Errorf("next = %d, want %d", next, tc.wantNext)
			}
		})
	}
}
