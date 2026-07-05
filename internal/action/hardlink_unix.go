//go:build unix

package action

import "os"

// createPlatformHardlink creates a hardlink at linkPath pointing to targetPath.
func createPlatformHardlink(linkPath, targetPath string) error {
	return os.Link(targetPath, linkPath)
}
