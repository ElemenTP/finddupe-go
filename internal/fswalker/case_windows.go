//go:build windows

package fswalker

import "strings"

// foldCase is true where the filesystem is case-insensitive by default, so a
// pattern must match a name that differs only in case: on Windows "*.TXT" is
// expected to find "photo.txt". macOS volumes can be either, so they are matched
// the way the shell does (case-sensitively) rather than guessed at.
const foldCase = true

// foldForMatch lowercases both sides of a component comparison where the platform
// folds case.
func foldForMatch(pattern, name string) (string, string) {
	return strings.ToLower(pattern), strings.ToLower(name)
}
