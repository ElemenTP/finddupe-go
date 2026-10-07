//go:build unix

package action

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// staleInfo is a FileInfo that deliberately disagrees with the file on disk.
type staleInfo struct {
	mode    fs.FileMode
	modTime time.Time
}

func (s staleInfo) Name() string       { return "stale" }
func (s staleInfo) Size() int64        { return 0 }
func (s staleInfo) Mode() fs.FileMode  { return s.mode }
func (s staleInfo) ModTime() time.Time { return s.modTime }
func (s staleInfo) IsDir() bool        { return false }
func (s staleInfo) Sys() any           { return nil }

// TestPreserveMetadata_UsesTheCallersStat pins the contract that lets an
// elimination reuse the stat its freshness check already made: the clone's metadata
// must come from the value the caller passes, not from a second read of the source
// (which cost two extra syscalls per clone). Passing a value that disagrees with the
// file on disk is the only way to observe which one was used, and it also documents
// that the caller owns the freshness of that stat.
func TestPreserveMetadata_UsesTheCallersStat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "clone.bin")
	if err := os.WriteFile(dst, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	const wantMode fs.FileMode = 0o604
	wantTime := time.Unix(1700000000, 0)
	if err := preserveMetadata(dst, src, staleInfo{mode: wantMode, modTime: wantTime}); err != nil {
		t.Fatalf("preserveMetadata: %v", err)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != wantMode.Perm() {
		t.Errorf("mode = %v, want %v (the caller's stat, not the file on disk)", got, wantMode.Perm())
	}
	if !info.ModTime().Equal(wantTime) {
		t.Errorf("mtime = %v, want %v (the caller's stat, not the file on disk)", info.ModTime(), wantTime)
	}
}
