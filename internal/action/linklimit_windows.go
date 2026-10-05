//go:build windows

package action

import (
	"os"

	"golang.org/x/sys/windows"

	"finddupe/internal/wininfo"
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

	info, infoErr := wininfo.FromHandle(windows.Handle(f.Fd()))
	if infoErr != nil {
		return false
	}
	return info.NumLinks >= maxHardlinks
}
