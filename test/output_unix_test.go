//go:build unix

package test_test

import (
	"strings"
	"testing"
)

// TestOutput_EscapesControlCharactersInPaths verifies that a crafted file name
// cannot forge extra result lines: the report must stay one line per action, or
// a reader (or a script) would attribute the forged line to another file.
func TestOutput_EscapesControlCharactersInPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	makeFile(t, dir, "evil\nDuplicate: HACKED.txt", "content")
	makeFile(t, dir, "b.txt", "content")

	stdout, stderr, code := run(t, "find", dir, "--no-progress")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, stderr)
	}

	var resultLines int
	for line := range strings.SplitSeq(stdout, "\n") {
		if strings.HasPrefix(line, "Duplicate:") {
			resultLines++
		}
	}
	if resultLines != 1 {
		t.Errorf("expected exactly one result line, got %d:\n%s", resultLines, stdout)
	}
	if strings.Contains(stdout, "\nDuplicate: HACKED") {
		t.Errorf("a crafted file name forged a result line:\n%s", stdout)
	}
	if !strings.Contains(stdout, `evil\nDuplicate`) {
		t.Errorf("expected the escaped file name in the report, got:\n%s", stdout)
	}
}
