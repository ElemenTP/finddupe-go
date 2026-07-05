//go:build windows

package action

import "os"

// createPlatformHardlink creates a hardlink at linkPath pointing to targetPath.
// On Windows, os.Link wraps CreateHardLinkW.
func createPlatformHardlink(linkPath, targetPath string) error {
	return os.Link(targetPath, linkPath)
}
