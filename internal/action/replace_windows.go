//go:build windows

package action

import "golang.org/x/sys/windows"

// replaceFile replaces dst with tmp using MoveFileEx with REPLACE_EXISTING.
func replaceFile(tmp, dst string) error {
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING)
}
