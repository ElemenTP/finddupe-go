package action

// linkReplace atomically replaces dst with a hard link to src.
//
// The link is created at a free temporary name in dst's directory and renamed
// over dst, which is atomic within one filesystem. If anything fails — different
// devices (EXDEV), a filesystem without hard links, the link limit, a missing
// keeper — dst is left exactly as it was. Removing dst first and linking
// afterwards would lose the file in all of those cases.
func linkReplace(src, dst string) error {
	return withTemporaryName(dst, func(tmpPath string) error {
		return createPlatformHardlink(tmpPath, src)
	})
}
