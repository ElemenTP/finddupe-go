//go:build !unix && !windows

package action

import "os"

// preserveMetadata restores the metadata this platform exposes: the permission
// bits and the modification time. srcInfo is the stat the caller already made of
// src, so the source is not read again.
func preserveMetadata(dst string, _ os.FileInfo, srcInfo os.FileInfo) error {
	if chmodErr := os.Chmod(dst, metadataMode(srcInfo.Mode())); chmodErr != nil {
		return chmodErr
	}
	return os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime())
}
