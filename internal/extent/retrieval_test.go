package extent //nolint:testpackage // exercises the unexported bound

import "testing"

// TestBoundedExtentCount verifies the bound applied to the extent count a driver
// reports: it may size an allocation and drive a buffer walk, so a bogus value
// must not be trusted.
func TestBoundedExtentCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		count  uint32
		bufLen int
		want   uint32
	}{
		{name: "count fits", count: 4, bufLen: 16 + 4*16, want: 4},
		{name: "count exceeds the buffer", count: 1000, bufLen: 16 + 4*16, want: 4},
		{name: "bogus huge count", count: 0xFFFFFFFF, bufLen: 16 + 4*16, want: 4},
		{name: "exactly full", count: 3, bufLen: 16 + 3*16, want: 3},
		{name: "header only", count: 9, bufLen: 16, want: 0},
		{name: "shorter than the header", count: 9, bufLen: 8, want: 0},
		{name: "empty buffer", count: 9, bufLen: 0, want: 0},
		{name: "partial pair", count: 9, bufLen: 16 + 15, want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := boundedExtentCount(tc.count, tc.bufLen); got != tc.want {
				t.Fatalf("boundedExtentCount(%d, %d) = %d, want %d", tc.count, tc.bufLen, got, tc.want)
			}
		})
	}
}
