package action

import (
	"fmt"
	"os"
	"path/filepath"
)

// linkReplace atomically replaces dst with a hard link to src.
//
// The link is created at a temporary name in dst's directory and renamed over
// dst, which is atomic within one filesystem. If anything fails — different
// devices (EXDEV), a filesystem without hard links, the link limit, a missing
// keeper — dst is left exactly as it was. Removing dst first and linking
// afterwards would lose the file in all of those cases.
func linkReplace(src, dst string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".finddupe-link-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}

	// link(2) requires the new name not to exist.
	if removeErr := os.Remove(tmpPath); removeErr != nil {
		return removeErr
	}

	if linkErr := createPlatformHardlink(tmpPath, src); linkErr != nil {
		_ = os.Remove(tmpPath)
		return linkErr
	}

	if replaceErr := replaceFile(tmpPath, dst); replaceErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace %s: %w", dst, replaceErr)
	}
	return nil
}
