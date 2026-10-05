package action

import "os"

// metadataMode is the part of a file mode that chmod can restore: the
// permission bits plus setuid/setgid/sticky. Everything else (the type bits) is
// a property of the file, not of its metadata.
func metadataMode(mode os.FileMode) os.FileMode {
	return mode.Perm() | (mode & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky))
}
