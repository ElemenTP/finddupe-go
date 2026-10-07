//go:build unix

package action

import (
	"runtime"
	"testing"
)

// TestDataLayoutXattr verifies which extended attributes are treated as part of
// the data layout. Getting this wrong is not cosmetic: on macOS a CoW clone of a
// compressed keeper lost com.apple.decmpfs to the victim-metadata sync and became
// a zero-length file.
func TestDataLayoutXattr(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "darwin" {
		for _, name := range []string{"com.apple.decmpfs", "com.apple.ResourceFork"} {
			if !dataLayoutXattr(name) {
				t.Errorf("%s carries the data layout and must not be synced from the victim", name)
			}
		}
	}

	// Attributes that describe the user's view of the file must keep following the
	// victim's metadata.
	for _, name := range []string{"user.finddupe.test", "security.selinux", "system.posix_acl_access"} {
		if dataLayoutXattr(name) {
			t.Errorf("%s is user-visible metadata, not data layout", name)
		}
	}
}
