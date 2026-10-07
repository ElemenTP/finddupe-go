package action

import (
	"io"
	"os"
)

// copyTailAt copies n bytes of src starting at srcOffset into dst at dstOffset.
//
// Block-clone APIs only duplicate whole clusters, so the final partial cluster
// of a cloned file has to be copied explicitly. Writing it at the wrong offset
// corrupts the clone silently — the caller must seek the destination too, not
// only the source.
func copyTailAt(dst, src *os.File, dstOffset, srcOffset, n int64) error {
	if _, err := src.Seek(srcOffset, io.SeekStart); err != nil {
		return err
	}
	if _, err := dst.Seek(dstOffset, io.SeekStart); err != nil {
		return err
	}
	_, err := io.CopyN(dst, src, n)
	return err
}
