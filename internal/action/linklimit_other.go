//go:build !unix && !windows

package action

// hardlinkLimitReached reports whether path has reached the filesystem's
// hardlink limit. Platforms without an implementation rely on the filesystem
// itself: linking happens under a temporary name and is only renamed over the
// victim once the link exists, so a refused link cannot damage the file.
func hardlinkLimitReached(_ string) bool {
	return false
}
