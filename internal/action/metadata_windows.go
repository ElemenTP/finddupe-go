//go:build windows

package action

import (
	"os"

	"golang.org/x/sys/windows"

	"finddupe/internal/wininfo"
)

// preserveMetadata gives dst the metadata of src: file attributes, the security
// descriptor (owner, primary group and DACL), and the creation/access/write
// timestamps.
//
// Restoring a security descriptor needs the right to change permissions, so
// that step is best-effort; attributes and timestamps are applied afterwards so
// they cannot be blocked by an inherited access control entry.
func preserveMetadata(dst, src string) error {
	copySecurityInfo(dst, src)
	copyFileTimes(dst, src)

	srcPath, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	dstPath, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}

	attrs, err := windows.GetFileAttributes(srcPath)
	if err != nil {
		return err
	}
	return windows.SetFileAttributes(dstPath, attrs)
}

// copySecurityInfo copies the owner, primary group and DACL of src onto dst.
// Failures are ignored: the caller may not have SeRestorePrivilege, and the
// clone's content and permissions are already correct.
func copySecurityInfo(dst, src string) {
	const info = windows.OWNER_SECURITY_INFORMATION |
		windows.GROUP_SECURITY_INFORMATION |
		windows.DACL_SECURITY_INFORMATION

	sd, err := windows.GetNamedSecurityInfo(src, windows.SE_FILE_OBJECT, info)
	if err != nil {
		return
	}

	owner, _, ownerErr := sd.Owner()
	group, _, groupErr := sd.Group()
	dacl, _, daclErr := sd.DACL()
	if ownerErr != nil || groupErr != nil || daclErr != nil {
		return
	}

	_ = windows.SetNamedSecurityInfo(dst, windows.SE_FILE_OBJECT, info, owner, group, dacl, nil)
}

// copyFileTimes copies the creation, last-access and last-write times of src
// onto dst. Failures are ignored for the same reason as copySecurityInfo. The
// source is opened read-only, so a read-only victim can still be read.
func copyFileTimes(dst, src string) {
	srcFile, err := os.Open(src)
	if err != nil {
		return
	}
	defer srcFile.Close()

	info, infoErr := wininfo.FromHandle(windows.Handle(srcFile.Fd()))
	if infoErr != nil {
		return
	}

	dstFile, err := os.OpenFile(dst, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer dstFile.Close()

	_ = windows.SetFileTime(
		windows.Handle(dstFile.Fd()),
		&info.CreationTime,
		&info.LastAccessTime,
		&info.LastWriteTime,
	)
}
