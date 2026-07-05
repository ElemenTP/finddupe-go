package dupe_test

import (
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

func TestDetector_DuplicateSignature(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())
	fi1 := dupe.FileInfo{Path: "/a", Size: 100, Signature: 0xABCD}
	fi2 := dupe.FileInfo{Path: "/b", Size: 100, Signature: 0xABCD}

	d.Insert(fi1)
	groups := d.Insert(fi2)

	if groups == nil {
		t.Fatal("expected non-nil groups for duplicate signature")
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Original.Path != "/a" {
		t.Errorf("expected original /a, got %s", groups[0].Original.Path)
	}
	if groups[0].Candidate.Path != "/b" {
		t.Errorf("expected candidate /b, got %s", groups[0].Candidate.Path)
	}
}

func TestDetector_MultipleCollisions(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())
	sig := uint64(0xBEEF)
	fi1 := dupe.FileInfo{Path: "/a", Size: 100, Signature: sig}
	fi2 := dupe.FileInfo{Path: "/b", Size: 100, Signature: sig}
	fi3 := dupe.FileInfo{Path: "/c", Size: 100, Signature: sig}

	d.Insert(fi1)
	d.Insert(fi2)

	// Third file should create ONE group (against the first file in the chain).
	groups := d.Insert(fi3)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group for third collision, got %d", len(groups))
	}
	if groups[0].Original.Path != "/a" {
		t.Errorf("expected original /a, got %s", groups[0].Original.Path)
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

func TestDetector_HardlinkMode(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())

	// File with same inode.
	ok1 := d.InsertHardlink(dupe.FileInfo{Path: "/a", Size: 100, Inode: 42, NumLinks: 3})
	ok2 := d.InsertHardlink(dupe.FileInfo{Path: "/b", Size: 100, Inode: 42, NumLinks: 3})

	if !ok1 {
		t.Error("expected first hardlink insert to succeed")
	}
	if !ok2 {
		t.Error("expected second hardlink insert to succeed")
	}

	groups := d.HardlinkGroups()
	if len(groups) != 1 {
		t.Fatalf("expected 1 hardlink group, got %d", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Fatalf("expected 2 files in group, got %d", len(groups[0]))
	}
}

func TestDetector_HardlinkMode_SingleLink(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())

	// File with NumLinks == 1 should be skipped.
	ok := d.InsertHardlink(dupe.FileInfo{Path: "/a", Size: 100, Inode: 42, NumLinks: 1})
	if ok {
		t.Error("expected single-link file to be skipped")
	}

	groups := d.HardlinkGroups()
	if len(groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(groups))
	}
}

func TestDetector_EmptyDetector(t *testing.T) {
	t.Parallel()
	d := dupe.NewDetector(dupe.NewStats())

	if d.Len() != 0 {
		t.Errorf("expected 0 entries, got %d", d.Len())
	}

	groups := d.HardlinkGroups()
	if len(groups) != 0 {
		t.Errorf("expected 0 hardlink groups, got %d", len(groups))
	}

	stats := d.Stats()
	if stats.TotalFiles.Load() != 0 {
		t.Errorf("expected TotalFiles=0, got %d", stats.TotalFiles.Load())
	}
}
