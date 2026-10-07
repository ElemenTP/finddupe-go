//go:build !darwin

package action

import "os"

// preservePlatformFlags is a no-op on platforms without BSD file flags.
func preservePlatformFlags(_ string, _ os.FileInfo) error {
	return nil
}
