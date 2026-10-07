//go:build !linux && !darwin && !windows

package compress

import "finddupe/internal/dupe"

// IsCompressed always reports false: this platform has no probe for transparent
// compression.
func IsCompressed(dupe.FileInfo) bool {
	return false
}
