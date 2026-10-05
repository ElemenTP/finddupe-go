//go:build !windows

package fswalker

// foldCase is false: these platforms match the way their shell does, and a
// case-insensitive volume (APFS can be either) is not detected at run time.
const foldCase = false

// foldForMatch returns the operands unchanged where case is significant.
func foldForMatch(pattern, name string) (string, string) {
	return pattern, name
}
