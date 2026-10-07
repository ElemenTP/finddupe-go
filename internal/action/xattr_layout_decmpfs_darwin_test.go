//go:build darwin

package action

import (
	"encoding/binary"
	"testing"
)

// TestDecmpfsNeedsResourceFork pins the header classification the data-layout
// pass depends on. An inline payload (types 1, 3, 11) has no resource fork by
// design and must not fail the action for the lack of one; a resource-fork type
// must have it, and an unreadable or unknown header is treated as requiring it,
// so a payload that went missing fails the action instead of reaching a clone.
func TestDecmpfsNeedsResourceFork(t *testing.T) {
	t.Parallel()

	header := func(compressionType uint32) []byte {
		out := make([]byte, decmpfsHeaderSize)
		copy(out, decmpfsMagic)
		binary.LittleEndian.PutUint32(out[4:8], compressionType)
		return out
	}

	cases := []struct {
		name     string
		typeID   uint32
		wantFork bool
	}{
		{"uncompressed in attribute", decmpfsTypeUncompressed, false},
		{"zlib inline", decmpfsTypeZlibInline, false},
		{"lzfse inline", decmpfsTypeLZFSEInline, false},
		{"zlib resource fork", decmpfsTypeZlibFork, true},
		{"lzvn resource fork", decmpfsTypeLZVNFork, true},
		{"lzvn resource fork (alt)", decmpfsTypeLZVNForkAlt, true},
		{"lzfse resource fork", decmpfsTypeLZFSEFork, true},
		{"unknown type", 99, true},
	}
	for _, tc := range cases {
		if got := decmpfsNeedsResourceFork(header(tc.typeID)); got != tc.wantFork {
			t.Errorf("%s: decmpfsNeedsResourceFork = %v, want %v", tc.name, got, tc.wantFork)
		}
	}

	if !decmpfsNeedsResourceFork(nil) {
		t.Error("a missing header must require the resource fork")
	}
	if !decmpfsNeedsResourceFork([]byte("cmp")) {
		t.Error("a truncated header must require the resource fork")
	}
	if !decmpfsNeedsResourceFork([]byte("nope\x03\x00\x00\x00")) {
		t.Error("a header without the cmpf magic must require the resource fork")
	}
}
