//go:build darwin

package action

// Data-layout extended attributes on macOS: a decmpfs-compressed file keeps its
// header in com.apple.decmpfs and its payload in com.apple.ResourceFork. They
// describe where the bytes are, not how the user sees the file, so a CoW clone
// must keep the ones it inherited from the cloned data.
const (
	xattrDecmpfs      = "com.apple.decmpfs"
	xattrResourceFork = "com.apple.ResourceFork"
)

// dataLayoutXattr reports whether an extended attribute carries the file's data
// layout rather than user-visible metadata.
func dataLayoutXattr(name string) bool {
	return name == xattrDecmpfs || name == xattrResourceFork
}
