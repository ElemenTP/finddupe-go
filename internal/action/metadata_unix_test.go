//go:build unix

package action_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"finddupe/internal/action"
	"finddupe/internal/config"
	"finddupe/internal/dupe"
)

// TestDoExecution_CoWClone_PreservesVictimMetadata verifies that the clone takes
// the victim's metadata, not the keeper's: mode, timestamps and extended
// attributes must survive the replacement.
func TestDoExecution_CoWClone_PreservesVictimMetadata(t *testing.T) {
	t.Parallel()

	dir := cloneCapableDir(t)
	data := randomBytes(t)
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	// Deliberately different metadata on each side of the pair.
	if err := os.Chmod(keeper, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(victim, 0o640); err != nil {
		t.Fatal(err)
	}
	victimTime := time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC)
	if err := os.Chtimes(victim, victimTime, victimTime); err != nil {
		t.Fatal(err)
	}

	// User extended attributes are best-effort: not every filesystem has them.
	const xattrName = "user.finddupe.test"
	hasXattr := unix.Setxattr(victim, xattrName, []byte("victim"), 0) == nil

	exec := action.New(action.Options{Action: config.ActionCoWClone})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
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
	if out.Result != action.ResultCoWCloned {
		t.Fatalf("Result = %v, want ResultCoWCloned", out.Result)
	}

	info, err := os.Stat(victim)
	if err != nil {
		t.Fatalf("stat victim: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("victim mode = %o, want 640 (the victim's, not the keeper's 600)", got)
	}
	if !info.ModTime().Equal(victimTime) {
		t.Errorf("victim mtime = %v, want %v", info.ModTime(), victimTime)
	}

	if hasXattr {
		value := make([]byte, 32)
		n, xattrErr := unix.Getxattr(victim, xattrName, value)
		if xattrErr != nil || string(value[:n]) != "victim" {
			t.Errorf("victim xattr = %q (err %v), want %q", value[:n], xattrErr, "victim")
		}
	}
}

// TestDoExecution_CoWClone_PreservesReadOnlyVictimMetadata is the regression
// test for the ordering bug: extended attributes are copied while the clone is
// still writable, because Linux refuses setxattr(user.*) on a read-only file
// even for its owner. The final mode (read-only, from the victim) is applied
// afterwards, and the victim's timestamps survive too.
func TestDoExecution_CoWClone_PreservesReadOnlyVictimMetadata(t *testing.T) {
	t.Parallel()

	dir := cloneCapableDir(t)
	data := randomBytes(t)
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	const xattrName = "user.finddupe.readonly"
	if err := unix.Setxattr(victim, xattrName, []byte("victim"), 0); err != nil {
		t.Skipf("user xattrs unsupported here: %v", err)
	}
	if err := os.Chmod(victim, 0o444); err != nil {
		t.Fatal(err)
	}
	victimTime := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(victim, victimTime, victimTime); err != nil {
		t.Fatal(err)
	}

	exec := action.New(action.Options{Action: config.ActionCoWClone, IncludeReadonly: true})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
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
	if out.Result != action.ResultCoWCloned {
		t.Fatalf("Result = %v, want ResultCoWCloned", out.Result)
	}

	info, err := os.Stat(victim)
	if err != nil {
		t.Fatalf("stat victim: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o444 {
		t.Errorf("victim mode = %o, want 444", got)
	}
	if !info.ModTime().Equal(victimTime) {
		t.Errorf("victim mtime = %v, want %v", info.ModTime(), victimTime)
	}

	value := make([]byte, 32)
	n, xattrErr := unix.Getxattr(victim, xattrName, value)
	if xattrErr != nil || string(value[:n]) != "victim" {
		t.Errorf("read-only victim lost its xattr: err=%v value=%q", xattrErr, value[:n])
	}
}

// TestDoExecution_DupeElim_SymlinkVictimSkipped verifies that a path which
// became a symbolic link after the content was hashed is never acted on, even
// when the link's target still matches the record: [os.Remove] would delete the
// link and [os.Link] would link the symlink, neither touching the duplicate the
// decision was made about.
func TestDoExecution_DupeElim_SymlinkVictimSkipped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("symlink victim")
	keeper := writeFile(t, dir, "a.bin", data)
	target := writeFile(t, dir, "target.bin", data)

	victim := filepath.Join(dir, "b.bin")
	if err := os.Symlink(target, victim); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	// Record what a following stat sees: the target, which still matches by size
	// and mtime. Only looking at the path itself (Lstat) can reject it.
	ti, err := os.Stat(victim)
	if err != nil {
		t.Fatalf("stat link target: %v", err)
	}
	vp := dupe.FileInfo{Path: victim, Size: ti.Size(), ModTime: ti.ModTime()}

	exec := action.New(action.Options{Action: config.ActionDelete})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), vp},
	})
	if err != nil {
		t.Fatalf("symlink victim: %v", err)
	}
	if out.Result != action.ResultSkippedChanged {
		t.Fatalf("Result = %v, want ResultSkippedChanged", out.Result)
	}

	info, err := os.Lstat(victim)
	if err != nil {
		t.Fatalf("the link must be preserved: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced")
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("the link target must be preserved: %v", statErr)
	}
}
