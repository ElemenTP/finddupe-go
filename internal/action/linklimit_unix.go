//go:build unix

package action

// hardlinkLimitReached reports whether path has reached the filesystem's
// hardlink limit.
//
// Unix filesystems enforce their own (much higher or unbounded) limit — ext4
// allows 65000 links, XFS and btrfs effectively do not cap it — so no count is
// guessed here. A link that still fails is no longer destructive: it is created
// under a temporary name and only renamed over the victim once it exists.
func hardlinkLimitReached(_ string) bool {
	return false
}
