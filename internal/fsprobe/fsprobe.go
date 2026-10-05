// Package fsprobe provides the filesystem-capability helpers the test suites share.
//
// The action, extent and system tests all need a directory whose filesystem supports
// the feature they exercise (CoW cloning, extent queries). Each had its own copy of
// "try the default temp dir, then the working directory, and skip the test when
// neither works", and copies of that logic drift: a probe that stops matching the
// feature turns the test into a silent skip, which is exactly the failure mode the
// CoW tests must not have.
//
// It is not a _test package because Go does not share test helpers between packages.
package fsprobe

import (
	"os"
	"testing"
)

// RepoDir returns a temporary directory inside the process working directory, which
// is often on the developer's real btrfs/XFS/APFS volume when the default temp dir is
// tmpfs. It returns "" when one cannot be created.
func RepoDir(t *testing.T) string {
	t.Helper()

	//nolint:usetesting // the default temp dir may be on a filesystem without the feature
	dir, err := os.MkdirTemp(".", "fsprobe-")
	if err != nil {
		return ""
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// CapableDir returns a temporary directory whose filesystem passes probe, skipping
// the test when neither the default temp dir nor the working directory does. what
// names the feature in the skip message.
//
// probe may create files in dir and is expected to clean up after itself; a probe
// that reports true for a directory whose filesystem does not really support the
// feature would make the caller test the wrong thing, so it should answer by
// exercising the feature, not by inspecting the filesystem type.
func CapableDir(t *testing.T, what string, probe func(dir string) bool) string {
	t.Helper()

	candidates := []string{t.TempDir()}
	if dir := RepoDir(t); dir != "" {
		candidates = append(candidates, dir)
	}

	for _, dir := range candidates {
		if probe(dir) {
			return dir
		}
	}

	t.Skipf("%s is not supported by the default temp dir or the repository filesystem", what)
	return ""
}
