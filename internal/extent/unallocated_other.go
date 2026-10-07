//go:build !unix

package extent

// unallocatedFile always reports false: this platform has no cheap "is anything
// allocated" test, and answering true would turn a query the volume refused into
// a claim that the file shares nothing. The error must stay an error.
func unallocatedFile(string) bool {
	return false
}
