package progress_test

import (
	"os"
	"path/filepath"
	"testing"

	"finddupe/internal/progress"
)

// TestIsTerminal pins the rule that keeps escape sequences out of redirected
// output: only a character device gets the progress line.
func TestIsTerminal(t *testing.T) {
	t.Parallel()

	file, err := os.Create(filepath.Join(t.TempDir(), "stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	if progress.IsTerminal(file) {
		t.Error("a regular file must not be treated as a terminal")
	}
}
