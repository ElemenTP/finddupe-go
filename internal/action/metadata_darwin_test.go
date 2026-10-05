//go:build darwin

package action_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"finddupe/internal/action"
	"finddupe/internal/config"
	"finddupe/internal/dupe"
)

// TestDoExecution_CoWClone_KeepsCompressedLayout is the regression test for a
// macOS data-loss bug: cloning a decmpfs-compressed keeper over an uncompressed
// victim synced the victim's (empty) attribute set onto the clone, which removed
// com.apple.decmpfs and left the file with an empty data fork — a zero-length
// duplicate that had replaced a 2 MiB file. The compressed payload lives in
// com.apple.ResourceFork, so the attributes belong to the cloned data and must
// follow the keeper, not the victim's metadata.
func TestDoExecution_CoWClone_KeepsCompressedLayout(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("ditto"); err != nil {
		t.Skipf("ditto is needed to create a decmpfs-compressed file: %v", err)
	}

	dir := cloneCapableDir(t)
	data := randomBytes(t)
	keeper := writeFile(t, dir, "keeper.bin", data)

	compressed := filepath.Join(dir, "compressed.bin")
	out, err := exec.Command("ditto", "--hfsCompression", keeper, compressed).CombinedOutput()
	if err != nil {
		t.Skipf("ditto --hfsCompression failed (%v): %s", err, out)
	}
	if !hasDecmpfs(t, compressed) {
		t.Skip("the filesystem did not store the copy compressed")
	}

	victim := writeFile(t, dir, "victim.bin", data)

	exec := action.New(action.Options{Action: config.ActionCoWClone, IncludeReadonly: true})
	result, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, compressed), fileInfo(t, victim)},
	})
	if err != nil {
		if errors.Is(err, action.ErrCoWNotSupported) {
			t.Skipf("CoW not supported on this filesystem: %v", err)
		}
		t.Fatalf("CoW clone: %v", err)
	}
	if result.Result != action.ResultCoWCloned {
		t.Fatalf("Result = %v, want ResultCoWCloned", result.Result)
	}

	got, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatalf("read the clone: %v", readErr)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("the clone holds %d bytes, want the original %d (content lost)", len(got), len(data))
	}
	if !hasDecmpfs(t, victim) {
		t.Fatal("the clone lost com.apple.decmpfs; its data fork is empty")
	}
}

// hasDecmpfs reports whether path carries the decmpfs compression header.
func hasDecmpfs(t *testing.T, path string) bool {
	t.Helper()
	size, err := unix.Getxattr(path, "com.apple.decmpfs", nil)
	return err == nil && size > 0
}
