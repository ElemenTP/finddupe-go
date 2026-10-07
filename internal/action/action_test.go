package action_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding"
	"errors"
	"hash"
	"os"
	"path/filepath"
	"testing"
	"time"

	"finddupe/internal/action"
	"finddupe/internal/config"
	"finddupe/internal/dupe"

	"finddupe/internal/fsprobe"
)

var testKey = dupe.GroupKey{Signature: 7, Size: 0}

// writeFile creates a file with the given content and returns its path.
func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// fileInfo builds a FileInfo for an existing file.
func fileInfo(t *testing.T, path string) dupe.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return dupe.FileInfo{Path: path, Size: info.Size()}
}

// partialHasher returns a marshaled SHA-256 state after hashing prefix bytes.
func partialHasher(t *testing.T, prefix []byte) ([]byte, hash.Hash) {
	t.Helper()
	h := sha256.New()
	h.Write(prefix)
	m, ok := h.(encoding.BinaryMarshaler)
	if !ok {
		t.Fatal("sha256 hasher is not a BinaryMarshaler")
	}
	state, err := m.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal hasher: %v", err)
	}
	return state, h
}

func TestDoExecution_HashCalc_Complete(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := bytes.Repeat([]byte("finddupe"), 40*1024) // 320 KB, multi-chunk
	path := writeFile(t, dir, "a.bin", data)

	exec := action.New(action.Options{Action: config.ActionReport})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.HashCalc,
		Files: []dupe.FileInfo{{Path: path, Size: int64(len(data))}},
	})
	if err != nil {
		t.Fatalf("HashCalc: %v", err)
	}

	if got, want := out.Files[0].SHA256, sha256.Sum256(data); got != want {
		t.Fatalf("SHA256 = %x, want %x", got, want)
	}
	if out.Files[0].HashOffset != int64(len(data)) {
		t.Fatalf("HashOffset = %d, want %d", out.Files[0].HashOffset, len(data))
	}
}

func TestDoExecution_HashCalc_Resumes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := bytes.Repeat([]byte("resume-me"), 50*1024)
	path := writeFile(t, dir, "a.bin", data)

	prefix := data[:100*1024]
	state, _ := partialHasher(t, prefix)

	exec := action.New(action.Options{Action: config.ActionReport})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:  testKey,
		Type: dupe.HashCalc,
		Files: []dupe.FileInfo{{
			Path:       path,
			Size:       int64(len(data)),
			HashState:  state,
			HashOffset: int64(len(prefix)),
		}},
	})
	if err != nil {
		t.Fatalf("HashCalc resume: %v", err)
	}

	if got, want := out.Files[0].SHA256, sha256.Sum256(data); got != want {
		t.Fatalf("resumed SHA256 = %x, want %x", got, want)
	}
}

func TestDoExecution_HashComp_Identical(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := bytes.Repeat([]byte("same"), 10*1024)
	a := writeFile(t, dir, "a.bin", data)
	b := writeFile(t, dir, "b.bin", data)

	exec := action.New(action.Options{Action: config.ActionReport})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.HashComp,
		Files: []dupe.FileInfo{fileInfo(t, a), fileInfo(t, b)},
	})
	if err != nil {
		t.Fatalf("HashComp: %v", err)
	}

	want := sha256.Sum256(data)
	if out.Files[0].SHA256 != want || out.Files[1].SHA256 != want {
		t.Fatalf("both hashes must complete and match: %x / %x",
			out.Files[0].SHA256, out.Files[1].SHA256)
	}
}

func TestDoExecution_HashComp_EarlyStop(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// 200 KB sharing the first 64 KB, then diverging.
	shared := bytes.Repeat([]byte("A"), 64*1024)
	restA := bytes.Repeat([]byte("B"), 200*1024-64*1024)
	restB := bytes.Repeat([]byte("C"), 200*1024-64*1024)
	a := writeFile(t, dir, "a.bin", append(append([]byte{}, shared...), restA...))
	b := writeFile(t, dir, "b.bin", append(append([]byte{}, shared...), restB...))

	exec := action.New(action.Options{Action: config.ActionReport})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.HashComp,
		Files: []dupe.FileInfo{fileInfo(t, a), fileInfo(t, b)},
	})
	if err != nil {
		t.Fatalf("HashComp: %v", err)
	}

	// Early-stop must avoid reading the whole file.
	if out.Files[0].HashOffset >= 200*1024 {
		t.Fatalf("HashOffset = %d, expected an early stop", out.Files[0].HashOffset)
	}
	if out.Files[0].SHA256 != ([32]byte{}) || out.Files[1].SHA256 != ([32]byte{}) {
		t.Fatal("hashes must be incomplete after an early stop")
	}
	if len(out.Files[0].HashState) == 0 {
		t.Fatal("partial hash state must be saved for a later resume")
	}
}

func TestDoExecution_DupeElim_Delete(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("duplicate content")
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	exec := action.New(action.Options{Action: config.ActionDelete})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), fileInfo(t, victim)},
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if out.Result != action.ResultDeleted {
		t.Fatalf("Result = %v, want ResultDeleted", out.Result)
	}
	if _, statErr := os.Stat(victim); !os.IsNotExist(statErr) {
		t.Fatalf("victim still exists: %v", statErr)
	}
	if _, statErr := os.Stat(keeper); statErr != nil {
		t.Fatalf("keeper missing: %v", statErr)
	}
}

func TestDoExecution_DupeElim_RefVictimSkipped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("reference content")
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	exec := action.New(action.Options{Action: config.ActionDelete})
	refVictim := fileInfo(t, victim)
	refVictim.IsRef = true

	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), refVictim},
	})
	if err != nil {
		t.Fatalf("Delete ref: %v", err)
	}
	if out.Result != action.ResultSkippedRef {
		t.Fatalf("Result = %v, want ResultSkippedRef", out.Result)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("reference victim must be preserved: %v", statErr)
	}
}

func TestDoExecution_DupeElim_SkipHardlinked(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("hardlinked pair")
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	kp := fileInfo(t, keeper)
	vp := fileInfo(t, victim)
	kp.Dev, kp.Inode, kp.NumLinks = 1, 42, 2
	vp.Dev, vp.Inode, vp.NumLinks = 1, 42, 2

	exec := action.New(action.Options{Action: config.ActionReport, SkipHardlinked: true})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{kp, vp},
	})
	if err != nil {
		t.Fatalf("skip hardlinked: %v", err)
	}
	if out.Result != action.ResultAlreadyHardlinked {
		t.Fatalf("Result = %v, want ResultAlreadyHardlinked", out.Result)
	}
}

func TestDoExecution_DupeElim_Report(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("report me")
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	exec := action.New(action.Options{Action: config.ActionReport})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), fileInfo(t, victim)},
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if out.Result != action.ResultVerifiedDuplicate {
		t.Fatalf("Result = %v, want ResultVerifiedDuplicate", out.Result)
	}
}

func TestDoExecution_DupeElim_Hardlink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("hardlink me")
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	exec := action.New(action.Options{Action: config.ActionHardlink})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), fileInfo(t, victim)},
	})
	if err != nil {
		t.Fatalf("hardlink: %v", err)
	}
	if out.Result != action.ResultHardlinked {
		t.Fatalf("Result = %v, want ResultHardlinked", out.Result)
	}

	ki, err := os.Stat(keeper)
	if err != nil {
		t.Fatalf("stat keeper: %v", err)
	}
	vi, err := os.Stat(victim)
	if err != nil {
		t.Fatalf("stat victim: %v", err)
	}
	if !os.SameFile(ki, vi) {
		t.Fatal("victim is not hardlinked to the keeper")
	}

	// The replacement goes through a temporary name; nothing may be left behind.
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temporary file left behind: %v", names)
	}
}

// TestDoExecution_DupeElim_Hardlink_LinkFailureKeepsVictim is the regression
// test for the data loss that remove-then-link caused: when the link failed
// (different device, filesystem without hard links, missing keeper), the
// victim had already been deleted.
func TestDoExecution_DupeElim_Hardlink_LinkFailureKeepsVictim(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const content = "precious content"
	victim := writeFile(t, dir, "victim.bin", []byte(content))

	// The keeper no longer exists, so os.Link must fail.
	keeper := dupe.FileInfo{Path: filepath.Join(dir, "gone.bin"), Size: int64(len(content))}

	exec := action.New(action.Options{Action: config.ActionHardlink})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{keeper, fileInfo(t, victim)},
	})
	if err == nil {
		t.Fatal("expected the hardlink to fail")
	}
	if out.Result != action.ResultError {
		t.Fatalf("Result = %v, want ResultError", out.Result)
	}

	data, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatalf("victim was destroyed by a failed hardlink: %v", readErr)
	}
	if string(data) != content {
		t.Fatalf("victim content = %q, want %q", data, content)
	}

	// The temporary link name must not be left behind either.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the victim to remain, got %d entries", len(entries))
	}
}

// TestDoExecution_DupeElim_Hardlink_PreservesKeeperMetadata verifies that
// hardlinking never rewrites the keeper's permissions or timestamps: the
// victim's metadata cannot be preserved on a shared inode, so it is not
// restored at all.
func TestDoExecution_DupeElim_Hardlink_PreservesKeeperMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("hardlink metadata")
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	if err := os.Chmod(keeper, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(victim, 0o600); err != nil {
		t.Fatal(err)
	}
	keeperTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	victimTime := time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC)
	if err := os.Chtimes(keeper, keeperTime, keeperTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(victim, victimTime, victimTime); err != nil {
		t.Fatal(err)
	}

	exec := action.New(action.Options{Action: config.ActionHardlink})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), fileInfo(t, victim)},
	})
	if err != nil {
		t.Fatalf("hardlink: %v", err)
	}
	if out.Result != action.ResultHardlinked {
		t.Fatalf("Result = %v, want ResultHardlinked", out.Result)
	}

	ki, err := os.Stat(keeper)
	if err != nil {
		t.Fatalf("stat keeper: %v", err)
	}
	vi, err := os.Stat(victim)
	if err != nil {
		t.Fatalf("stat victim: %v", err)
	}
	if !os.SameFile(ki, vi) {
		t.Fatal("victim is not hardlinked to the keeper")
	}
	if got := ki.Mode().Perm(); got != 0o640 {
		t.Errorf("keeper mode = %o, want 640 (the keeper must not be rewritten)", got)
	}
	if !ki.ModTime().Equal(keeperTime) {
		t.Errorf("keeper mtime = %v, want %v", ki.ModTime(), keeperTime)
	}
}

// TestDoExecution_DupeElim_CrossDeviceSkipsHardlink verifies that a pair on two
// different devices is skipped before anything is created or removed.
func TestDoExecution_DupeElim_CrossDeviceSkipsHardlink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const content = "cross device"
	keeper := writeFile(t, dir, "a.bin", []byte(content))
	victim := writeFile(t, dir, "b.bin", []byte(content))

	kp := fileInfo(t, keeper)
	vp := fileInfo(t, victim)
	kp.Dev, vp.Dev = 1, 2

	exec := action.New(action.Options{Action: config.ActionHardlink})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{kp, vp},
	})
	if err != nil {
		t.Fatalf("cross-device hardlink: %v", err)
	}
	if out.Result != action.ResultSkippedCrossDevice {
		t.Fatalf("Result = %v, want ResultSkippedCrossDevice", out.Result)
	}

	for _, path := range []string{keeper, victim} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("%s must be preserved: %v", path, statErr)
		}
	}
}

// TestDoExecution_DupeElim_ChangedFileSkipped verifies that a file which
// changed after its hash was computed is never eliminated.
func TestDoExecution_DupeElim_ChangedFileSkipped(t *testing.T) {
	t.Parallel()

	actions := []struct {
		name string
		kind config.Action
	}{
		{name: "delete", kind: config.ActionDelete},
		{name: "hardlink", kind: config.ActionHardlink},
		{name: "cow", kind: config.ActionCoWClone},
	}

	for _, tc := range actions {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kind := tc.kind
			dir := t.TempDir()
			data := []byte("changed after hashing")
			keeper := writeFile(t, dir, "a.bin", data)
			victim := writeFile(t, dir, "b.bin", data)

			kp := fileInfo(t, keeper)
			vp := fileInfo(t, victim)
			kp.ModTime, vp.ModTime = statModTime(t, keeper), statModTime(t, victim)

			// The victim is rewritten after being hashed.
			changedTime := vp.ModTime.Add(time.Hour)
			if err := os.Chtimes(victim, changedTime, changedTime); err != nil {
				t.Fatal(err)
			}

			exec := action.New(action.Options{Action: kind})
			out, err := exec.DoExecution(context.Background(), dupe.Execution{
				Key:   testKey,
				Type:  dupe.DupeElim,
				Files: []dupe.FileInfo{kp, vp},
			})
			if err != nil {
				t.Fatalf("action %v: %v", kind, err)
			}
			if out.Result != action.ResultSkippedChanged {
				t.Fatalf("action %v: Result = %v, want ResultSkippedChanged", kind, out.Result)
			}

			got, readErr := os.ReadFile(victim)
			if readErr != nil {
				t.Fatalf("victim must be preserved: %v", readErr)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("victim content changed")
			}
		})
	}
}

// TestDoExecution_DupeElim_UnchangedFileIsActedOn verifies the happy path: a
// recorded modification time that still matches does not block the action.
func TestDoExecution_DupeElim_UnchangedFileIsActedOn(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("unchanged")
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	kp := fileInfo(t, keeper)
	vp := fileInfo(t, victim)
	kp.ModTime, vp.ModTime = statModTime(t, keeper), statModTime(t, victim)

	exec := action.New(action.Options{Action: config.ActionDelete})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{kp, vp},
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if out.Result != action.ResultDeleted {
		t.Fatalf("Result = %v, want ResultDeleted", out.Result)
	}
	if _, statErr := os.Stat(victim); !os.IsNotExist(statErr) {
		t.Fatalf("victim should have been deleted: %v", statErr)
	}
}

// statModTime returns the modification time of path.
func statModTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

func TestDoExecution_DupeElim_ReadOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("readonly victim")
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	if err := os.Chmod(victim, 0o444); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	exec := action.New(action.Options{Action: config.ActionDelete})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), fileInfo(t, victim)},
	})
	if err != nil {
		t.Fatalf("readonly delete: %v", err)
	}
	if out.Result != action.ResultSkippedRO {
		t.Fatalf("Result = %v, want ResultSkippedRO", out.Result)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("read-only victim must be preserved: %v", statErr)
	}

	// With IncludeReadonly the delete must succeed.
	exec = action.New(action.Options{Action: config.ActionDelete, IncludeReadonly: true})
	out, err = exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), fileInfo(t, victim)},
	})
	if err != nil {
		t.Fatalf("forced readonly delete: %v", err)
	}
	if out.Result != action.ResultDeleted {
		t.Fatalf("Result = %v, want ResultDeleted", out.Result)
	}
}

// randomBytes returns incompressible data so filesystem compression cannot
// distort the physical extent layout.
func randomBytes(t *testing.T) []byte {
	t.Helper()
	buf := make([]byte, 128*1024)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return buf
}

// cowProbe reports whether a CoW clone succeeds inside dir. It exercises the action
// layer itself, so the capability it reports is the one the tests below rely on.
func cowProbe(t *testing.T, dir string) bool {
	t.Helper()

	//nolint:usetesting // the probe must live on the filesystem being tested
	probe, mkErr := os.MkdirTemp(dir, "cowprobe-")
	if mkErr != nil {
		return false
	}
	defer os.RemoveAll(probe)

	data := randomBytes(t)
	a := filepath.Join(probe, "probe-a.bin")
	b := filepath.Join(probe, "probe-b.bin")
	if err := os.WriteFile(a, data, 0o644); err != nil {
		return false
	}
	if err := os.WriteFile(b, data, 0o644); err != nil {
		return false
	}

	exec := action.New(action.Options{Action: config.ActionCoWClone})
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, a), fileInfo(t, b)},
	})
	return err == nil && out.Result == action.ResultCoWCloned
}

// cloneCapableDir returns a temp directory where CoW cloning works, skipping the test
// when neither the default temp dir (often tmpfs) nor the repository filesystem
// supports it.
func cloneCapableDir(t *testing.T) string {
	t.Helper()

	return fsprobe.CapableDir(t, "CoW cloning", func(dir string) bool { return cowProbe(t, dir) })
}

func TestDoExecution_CoWClone(t *testing.T) {
	t.Parallel()

	dir := cloneCapableDir(t)
	data := randomBytes(t)
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

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

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("cloned victim content differs")
	}

	ki, _ := os.Stat(keeper)
	vi, _ := os.Stat(victim)
	if os.SameFile(ki, vi) {
		t.Fatal("CoW clone must be a distinct inode, not a hardlink")
	}
}

// TestDoExecution_SamePhysicalFile_NoAction verifies that no action ever
// touches a path that is the same physical file as the keeper (a hardlink).
func TestDoExecution_SamePhysicalFile_NoAction(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("hardlinked content")
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	kp := fileInfo(t, keeper)
	vp := fileInfo(t, victim)
	kp.Dev, kp.Inode = 1, 7
	vp.Dev, vp.Inode = 1, 7

	for _, actionKind := range []config.Action{
		config.ActionDelete, config.ActionHardlink, config.ActionCoWClone,
	} {
		exec := action.New(action.Options{Action: actionKind})
		out, err := exec.DoExecution(context.Background(), dupe.Execution{
			Key:   testKey,
			Type:  dupe.DupeElim,
			Files: []dupe.FileInfo{kp, vp},
		})
		if err != nil {
			t.Fatalf("action %v: %v", actionKind, err)
		}
		if out.Result != action.ResultAlreadyHardlinked {
			t.Fatalf("action %v: Result = %v, want ResultAlreadyHardlinked", actionKind, out.Result)
		}
	}

	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim must be preserved: %v", err)
	}
}

// TestDoExecution_SamePhysicalFile_UnknownIdentity verifies the identity
// fallback: when the scan could not report a file index, two names for one
// physical file must still be recognized instead of being eliminated against
// each other.
func TestDoExecution_SamePhysicalFile_UnknownIdentity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte("hardlinked without an index")
	keeper := writeFile(t, dir, "a.bin", data)
	victim := filepath.Join(dir, "b.bin")
	if err := os.Link(keeper, victim); err != nil {
		t.Fatalf("link: %v", err)
	}

	// fileInfo leaves Dev/Inode at zero, as a filesystem without identity would.
	kp := fileInfo(t, keeper)
	vp := fileInfo(t, victim)
	if kp.Inode != 0 || vp.Inode != 0 {
		t.Fatal("test setup expects an unknown identity")
	}

	for _, actionKind := range []config.Action{
		config.ActionDelete, config.ActionHardlink, config.ActionCoWClone,
	} {
		exec := action.New(action.Options{Action: actionKind})
		out, err := exec.DoExecution(context.Background(), dupe.Execution{
			Key:   testKey,
			Type:  dupe.DupeElim,
			Files: []dupe.FileInfo{kp, vp},
		})
		if err != nil {
			t.Fatalf("action %v: %v", actionKind, err)
		}
		if out.Result != action.ResultAlreadyHardlinked {
			t.Fatalf("action %v: Result = %v, want ResultAlreadyHardlinked", actionKind, out.Result)
		}
	}

	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim must be preserved: %v", err)
	}
}

// TestDoExecution_CoWClone_SkipsAlreadyShared verifies the skip fast path: a
// second clone of an already-shared pair is a no-op.
func TestDoExecution_CoWClone_SkipsAlreadyShared(t *testing.T) {
	t.Parallel()

	dir := cloneCapableDir(t)
	data := randomBytes(t)
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	exec := action.New(action.Options{Action: config.ActionCoWClone})

	// First pass creates the clone.
	if out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), fileInfo(t, victim)},
	}); err != nil || out.Result != action.ResultCoWCloned {
		t.Fatalf("first clone: result=%v err=%v", out.Result, err)
	}

	// Second pass must notice they already share storage. The device is known
	// (the scanner always records it), which is what makes physical offsets
	// comparable at all.
	kp, vp := fileInfo(t, keeper), fileInfo(t, victim)
	kp.Dev, vp.Dev = 1, 1
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{kp, vp},
	})
	if err != nil {
		t.Fatalf("second clone: %v", err)
	}
	if out.Result != action.ResultAlreadyShared {
		t.Fatalf("second clone: Result = %v, want ResultAlreadyShared", out.Result)
	}
}

// TestDoExecution_CoWClone_CrossDeviceNotShared verifies that files whose
// extent layouts match are never assumed to share storage when they live on
// different devices: physical offsets are only comparable within one volume.
func TestDoExecution_CoWClone_CrossDeviceNotShared(t *testing.T) {
	t.Parallel()

	dir := cloneCapableDir(t)
	data := randomBytes(t)
	keeper := writeFile(t, dir, "a.bin", data)
	victim := writeFile(t, dir, "b.bin", data)

	exec := action.New(action.Options{Action: config.ActionCoWClone})

	// First pass creates a real clone, so the two layouts are identical.
	if out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, keeper), fileInfo(t, victim)},
	}); err != nil || out.Result != action.ResultCoWCloned {
		t.Fatalf("clone setup: result=%v err=%v", out.Result, err)
	}

	// Same device: the identical layouts are recognized as already shared.
	kp := fileInfo(t, keeper)
	vp := fileInfo(t, victim)
	kp.Dev, vp.Dev = 1, 1
	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{kp, vp},
	})
	if err != nil {
		t.Fatalf("same-device clone: %v", err)
	}
	if out.Result != action.ResultAlreadyShared {
		t.Fatalf("same device: Result = %v, want ResultAlreadyShared", out.Result)
	}

	// Different devices: the layout must not be trusted and no clone can be
	// attempted at all, because storage blocks cannot be shared across volumes.
	// The pair is skipped — the same outcome a hardlink would get — instead of
	// being reported as "this filesystem does not support CoW".
	kp.Dev, vp.Dev = 1, 2
	out, err = exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{kp, vp},
	})
	if err != nil {
		t.Fatalf("cross-device clone: %v", err)
	}
	if out.Result != action.ResultSkippedCrossDevice {
		t.Fatalf("different devices: Result = %v, want ResultSkippedCrossDevice", out.Result)
	}
}

// TestDoExecution_CoWDetect_PartialCloneRatio covers a group whose clone was
// partially rewritten: the ratio answers how much each member shares with the rest
// of its group, so both members report the untouched tail. The kernel's per-extent
// "shared" flag would answer a different question ("shared with someone") and made
// the untouched original look 100% shared on filesystems that set it.
func TestDoExecution_CoWDetect_PartialCloneRatio(t *testing.T) {
	t.Parallel()

	dir := cloneCapableDir(t)
	data := randomBytes(t)
	original := writeFile(t, dir, "a.bin", data)
	clone := writeFile(t, dir, "clone.bin", data)

	exec := action.New(action.Options{Action: config.ActionCoWClone})
	if out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, original), fileInfo(t, clone)},
	}); err != nil || out.Result != action.ResultCoWCloned {
		t.Fatalf("clone setup: result=%v err=%v", out.Result, err)
	}

	// Rewrite the first quarter of the clone with the very same bytes: the content
	// is unchanged, but those blocks can no longer be shared with the original.
	rewritten := len(data) / 4
	file, err := os.OpenFile(clone, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, writeErr := file.WriteAt(data[:rewritten], 0); writeErr != nil {
		_ = file.Close()
		t.Fatalf("rewrite the clone: %v", writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.CoWDetect,
		Files: []dupe.FileInfo{fileInfo(t, original), fileInfo(t, clone)},
	})
	if err != nil {
		t.Fatalf("CoWDetect: %v", err)
	}
	if out.FileShared == nil {
		t.Skip("extent information unavailable")
	}

	want := int64(len(data) - rewritten)
	for i, shared := range out.FileShared {
		if shared != want {
			t.Errorf("member %d shared = %d, want %d (the group shares the untouched tail)", i, shared, want)
		}
	}
}

// TestDoExecution_CoWDetect_GroupRatios checks per-file sharing ratios for a
// group of [original, clone, independent copy].
func TestDoExecution_CoWDetect_GroupRatios(t *testing.T) {
	t.Parallel()

	dir := cloneCapableDir(t)
	data := randomBytes(t)
	size := int64(len(data))

	original := writeFile(t, dir, "a.bin", data)
	clone := writeFile(t, dir, "clone.bin", data)
	copied := writeFile(t, dir, "copy.bin", data)

	exec := action.New(action.Options{Action: config.ActionCoWClone})
	if out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:   testKey,
		Type:  dupe.DupeElim,
		Files: []dupe.FileInfo{fileInfo(t, original), fileInfo(t, clone)},
	}); err != nil || out.Result != action.ResultCoWCloned {
		t.Fatalf("clone setup: result=%v err=%v", out.Result, err)
	}

	out, err := exec.DoExecution(context.Background(), dupe.Execution{
		Key:  testKey,
		Type: dupe.CoWDetect,
		Files: []dupe.FileInfo{
			fileInfo(t, original),
			fileInfo(t, clone),
			fileInfo(t, copied),
		},
	})
	if err != nil {
		t.Fatalf("CoWDetect: %v", err)
	}
	if out.FileShared == nil {
		t.Skip("extent information unavailable")
	}
	if len(out.FileShared) != 3 {
		t.Fatalf("FileShared length = %d, want 3", len(out.FileShared))
	}

	if out.FileShared[0] != size {
		t.Errorf("original shared = %d, want %d", out.FileShared[0], size)
	}
	if out.FileShared[1] != size {
		t.Errorf("clone shared = %d, want %d", out.FileShared[1], size)
	}
	if out.FileShared[2] != 0 {
		t.Errorf("independent copy shared = %d, want 0", out.FileShared[2])
	}
}
