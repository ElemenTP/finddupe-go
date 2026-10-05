//go:build !unix && !windows

package action

import "os"

// preserveMetadata restores the metadata this platform exposes: the permission
// bits and the modification time.
func preserveMetadata(dst, src string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if chmodErr := os.Chmod(dst, metadataMode(info.Mode())); chmodErr != nil {
		return chmodErr
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}
