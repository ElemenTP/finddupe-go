package dupe_test

import (
	"crypto/sha256"
	"testing"

	"finddupe/internal/dupe"
)

func TestDetector_FirstInsert(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())
	fi := dupe.FileInfo{Path: "/a", Size: 100, Signature: 0xABCD}

	groups := d.Insert(fi)
	if groups != nil {
		t.Errorf("expected nil for first insert, got %v", groups)
	}
	if d.Len() != 1 {
		t.Errorf("expected 1 entry, got %d", d.Len())
	}
}

func TestDetector_TwoFiles_EmitsGroup(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())
	fi1 := dupe.FileInfo{Path: "/a", Size: 100, Signature: 0xABCD}
	fi2 := dupe.FileInfo{Path: "/b", Size: 100, Signature: 0xABCD}

	d.Insert(fi1)
	groups := d.Insert(fi2)

	if groups == nil {
		t.Fatal("expected non-nil groups for second file")
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Original.Path != "/a" {
		t.Errorf("expected original /a, got %s", groups[0].Original.Path)
	}
	if groups[0].Key != (dupe.GroupKey{Signature: 0xABCD, Size: 100}) {
		t.Errorf("expected composite key, got %+v", groups[0].Key)
	}
}

func TestDetector_CompositeKey_DifferentSizes(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())

	// Same signature, different sizes → different groups (composite key).
	d.Insert(dupe.FileInfo{Path: "/a", Size: 100, Signature: 0xABCD})
	d.Insert(dupe.FileInfo{Path: "/b", Size: 200, Signature: 0xABCD})

	if d.Len() != 2 {
		t.Errorf("expected 2 separate groups (different sizes), got %d", d.Len())
	}
}

func TestDetector_ThreePlusFiles(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())
	sig := uint64(0xBEEF)
	fi1 := dupe.FileInfo{Path: "/a", Size: 100, Signature: sig}
	fi2 := dupe.FileInfo{Path: "/b", Size: 100, Signature: sig}
	fi3 := dupe.FileInfo{Path: "/c", Size: 100, Signature: sig}

	d.Insert(fi1)
	d.Insert(fi2)

	// Third file: should emit at least 1 group (against zero-SHA representative).
	groups := d.Insert(fi3)
	if len(groups) < 1 {
		t.Fatalf("expected at least 1 group for 3+ files, got %d", len(groups))
	}
	if d.GroupSize(dupe.GroupKey{Signature: sig, Size: 100}) != 3 {
		t.Errorf("expected group size 3, got %d", d.GroupSize(dupe.GroupKey{Signature: sig, Size: 100}))
	}

	// All files should be in zero bucket (no SHA computed yet).
	unhashed := d.UnhashedFiles(dupe.GroupKey{Signature: sig, Size: 100})
	if len(unhashed) != 3 {
		t.Errorf("expected 3 unhashed files, got %d", len(unhashed))
	}
}

func TestDetector_DifferentSignatures(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())

	d.Insert(dupe.FileInfo{Path: "/a", Size: 100, Signature: 1})
	d.Insert(dupe.FileInfo{Path: "/b", Size: 200, Signature: 2})
	d.Insert(dupe.FileInfo{Path: "/c", Size: 300, Signature: 3})

	if d.Len() != 3 {
		t.Errorf("expected 3 entries, got %d", d.Len())
	}
}

func TestDetector_StatsUpdate(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())

	d.Insert(dupe.FileInfo{Path: "/a", Size: 100, Signature: 1})
	d.Insert(dupe.FileInfo{Path: "/b", Size: 200, Signature: 2})

	stats := d.Stats()
	if stats.TotalFiles.Load() != 2 {
		t.Errorf("expected TotalFiles=2, got %d", stats.TotalFiles.Load())
	}
	if stats.TotalBytes.Load() != 300 {
		t.Errorf("expected TotalBytes=300, got %d", stats.TotalBytes.Load())
	}
}

func TestDetector_EmptyDetector(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())

	if d.Len() != 0 {
		t.Errorf("expected 0 entries, got %d", d.Len())
	}

	stats := d.Stats()
	if stats.TotalFiles.Load() != 0 {
		t.Errorf("expected TotalFiles=0, got %d", stats.TotalFiles.Load())
	}
}

func TestDetector_SHA256InstantMatch(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())
	key := dupe.GroupKey{Signature: 0xABCD, Size: 100}
	hash1 := sha256.Sum256([]byte("content A"))

	// Simulate: file stored, verified, SHA-256 completed, moved to SHA bucket.
	fi1 := dupe.FileInfo{Path: "/a", Size: 100, Signature: 0xABCD}
	d.Insert(fi1)
	d.UpdateFileState(key, "/a", nil, 100, hash1)

	// Second file arrives with matching SHA-256 → instant confirmed duplicate.
	fi2 := dupe.FileInfo{Path: "/b", Size: 100, Signature: 0xABCD, SHA256: hash1}
	groups := d.Insert(fi2)

	if groups == nil {
		t.Fatal("expected instant match with known SHA-256")
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Original.Path != "/a" {
		t.Errorf("expected original /a, got %s", groups[0].Original.Path)
	}
}

func TestDetector_SHA256CRCCollision(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())
	key := dupe.GroupKey{Signature: 0xABCD, Size: 100}
	hashA := sha256.Sum256([]byte("content A"))
	hashB := sha256.Sum256([]byte("different content B"))

	// File A with known SHA.
	fiA := dupe.FileInfo{Path: "/a", Size: 100, Signature: 0xABCD}
	d.Insert(fiA)
	d.UpdateFileState(key, "/a", nil, 100, hashA)

	// File B: different SHA-256 → CRC collision, stored in new bucket.
	fiB := dupe.FileInfo{Path: "/b", Size: 100, Signature: 0xABCD, SHA256: hashB}
	groups := d.Insert(fiB)
	if groups != nil {
		t.Error("expected nil (CRC collision with known different SHA-256)")
	}

	// File C matches B's SHA-256 → instant match against B.
	fiC := dupe.FileInfo{Path: "/c", Size: 100, Signature: 0xABCD, SHA256: hashB}
	groups = d.Insert(fiC)
	if groups == nil {
		t.Fatal("expected instant match for C against B's SHA-256")
	}
	if groups[0].Original.Path != "/b" {
		t.Errorf("expected original /b, got %s", groups[0].Original.Path)
	}
}

func TestDetector_UpdateFileState_PartialProgress(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())
	key := dupe.GroupKey{Signature: 0xABCD, Size: 100}
	hash1 := sha256.Sum256([]byte("content A"))

	fi := dupe.FileInfo{Path: "/a", Size: 100, Signature: 0xABCD}
	d.Insert(fi)

	// Save partial state (first 50 bytes hashed, no complete SHA).
	d.UpdateFileState(key, "/a", []byte{1, 2, 3}, 50, [32]byte{})

	// File should still be in zero-SHA bucket (incomplete).
	unhashed := d.UnhashedFiles(key)
	if len(unhashed) != 1 {
		t.Fatalf("expected 1 file in zero bucket, got %d", len(unhashed))
	}
	if unhashed[0].HashOffset != 50 {
		t.Errorf("expected HashOffset=50, got %d", unhashed[0].HashOffset)
	}

	// Complete the hash.
	d.UpdateFileState(key, "/a", nil, 100, hash1)

	// File should now be in SHA bucket.
	unhashed = d.UnhashedFiles(key)
	if len(unhashed) != 0 {
		t.Errorf("expected 0 files in zero bucket after complete, got %d", len(unhashed))
	}
}

func TestDetector_UpdateFileState_KeepsLargerOffset(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())
	key := dupe.GroupKey{Signature: 0xABCD, Size: 100}

	fi := dupe.FileInfo{Path: "/a", Size: 100, Signature: 0xABCD}
	d.Insert(fi)

	// Save partial state at offset 30.
	d.UpdateFileState(key, "/a", []byte{1, 2, 3}, 30, [32]byte{})

	// Try to update with smaller offset — should be ignored.
	d.UpdateFileState(key, "/a", []byte{4, 5, 6}, 10, [32]byte{})

	unhashed := d.UnhashedFiles(key)
	if len(unhashed) != 1 {
		t.Fatal("expected file in zero bucket")
	}
	if unhashed[0].HashOffset != 30 {
		t.Errorf("expected HashOffset=30 (larger kept), got %d", unhashed[0].HashOffset)
	}
}
