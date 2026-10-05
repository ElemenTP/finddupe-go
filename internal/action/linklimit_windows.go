//go:build windows

package action

import (
	"os"
	"syscall"
)

// maxHardlinks is the hardlink limit per file on NTFS. It is checked because the
// count read during the scan is stale: every link created during the run has
// raised the real one.
const maxHardlinks = 1023

// hardlinkLimitReached reports whether path already carries NTFS's maximum
// number of hard links. The count is re-read from the file because the value in
// the scan listing is stale: every link created during this run has raised the
// real one, so the listing would let the run walk past the limit and fail on a
// file that still has to be replaced.
func hardlinkLimitReached(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info); err != nil {
		return false
	}
	return uint64(info.NumberOfLinks) >= maxHardlinks
}
