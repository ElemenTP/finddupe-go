//go:build unix && !darwin

package action

// dataLayoutXattr reports whether an extended attribute carries the file's data
// layout rather than user-visible metadata. Linux and the BSDs keep the data
// layout in the filesystem itself (extents, compression), not in an attribute, so
// nothing is exempt here: ACLs, SELinux labels and user attributes all follow the
// victim's metadata.
func dataLayoutXattr(string) bool {
	return false
}
