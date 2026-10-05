//go:build !unix && !windows

package action

import (
	"os"

	"finddupe/internal/dupe"
)

// sameIdentity cannot answer on platforms whose os.FileInfo carries no identity
// and whose files cannot be queried for it: the second result is false, so the
// caller falls back to size and modification time.
func sameIdentity(string, dupe.FileInfo, os.FileInfo) (bool, bool) {
	return false, false
}
