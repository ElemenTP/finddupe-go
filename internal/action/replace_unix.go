//go:build unix

package action

import "os"

// replaceFile atomically renames tmp over dst (atomic within a filesystem).
func replaceFile(tmp, dst string) error {
	return os.Rename(tmp, dst)
}
