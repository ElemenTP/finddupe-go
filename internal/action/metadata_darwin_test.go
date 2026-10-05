//go:build darwin

package action_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"finddupe/internal/action"
	"finddupe/internal/config"
	"finddupe/internal/dupe"
)

// ufCompressedFlag is UF_COMPRESSED from sys/stat.h: the file is stored
// compressed. It is a storage flag, so it belongs to the cloned data.
const ufCompressedFlag = 0x00000020

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

	// The compression state is a storage attribute: the clone holds the keeper's
	// bytes, so both the flag and the reported size must come from the keeper. A
	// clone that reported zero bytes was exactly what the probe caught, because
	// the victim's (unset) UF_COMPRESSED was applied to it.
	info, statErr := os.Stat(victim)
	if statErr != nil {
		t.Fatalf("stat the clone: %v", statErr)
	}
	if info.Size() != int64(len(data)) {
		t.Fatalf("the clone reports %d bytes, want %d", info.Size(), len(data))
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Flags&ufCompressedFlag == 0 {
		t.Fatalf("the clone lost UF_COMPRESSED (flags %#x): it is a compressed payload marked uncompressed", stat.Flags)
	}
}

// TestDoExecution_CoWClone_DoesNotInheritVictimCompression is the other half of
// the rule: the layout follows the keeper, so cloning an uncompressed keeper over
// a compressed victim must not attach the victim's compressed container to a file
// that has no such payload — --prefer-compressed is what keeps a group compressed.
func TestDoExecution_CoWClone_DoesNotInheritVictimCompression(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("ditto"); err != nil {
		t.Skipf("ditto is needed to create a decmpfs-compressed file: %v", err)
	}

	dir := cloneCapableDir(t)
	data := randomBytes(t)
	keeper := writeFile(t, dir, "keeper.bin", data)

	victim := filepath.Join(dir, "victim.bin")
	out, err := exec.Command("ditto", "--hfsCompression", keeper, victim).CombinedOutput()
	if err != nil {
		t.Skipf("ditto --hfsCompression failed (%v): %s", err, out)
	}
	if !hasDecmpfs(t, victim) {
		t.Skip("the filesystem did not store the copy compressed")
	}

	exec := action.New(action.Options{Action: config.ActionCoWClone, IncludeReadonly: true})
	result, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), fileInfo(t, victim)},
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
		t.Fatalf("the clone holds %d bytes, want the original %d", len(got), len(data))
	}
	if hasDecmpfs(t, victim) {
		t.Fatal("the clone claims to be compressed although its data came from an uncompressed keeper")
	}
}

// hasDecmpfs reports whether path carries the decmpfs compression header.
func hasDecmpfs(t *testing.T, path string) bool {
	t.Helper()
	size, err := unix.Getxattr(path, "com.apple.decmpfs", nil)
	return err == nil && size > 0
}
