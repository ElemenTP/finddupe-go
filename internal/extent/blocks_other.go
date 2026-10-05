//go:build !unix

package extent

// hasAllocatedBlocks reports whether the file has any storage allocated to it.
// The non-Unix implementations answer the query for a file without allocated
// extents directly (Windows reports resident data as an empty mapping), so the
// distinction is never needed and a false result simply keeps the original
// "unsupported" error.
func hasAllocatedBlocks(string) bool {
	return false
}
