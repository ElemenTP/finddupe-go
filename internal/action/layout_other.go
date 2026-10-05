//go:build !darwin

package action

// preserveDataLayout is a no-op where the filesystem keeps the data layout in
// itself rather than in attributes: Linux stores compression per extent and
// Windows in file attributes, neither of which a clone inherits through the
// victim's metadata.
func preserveDataLayout(_, _ string) error {
	return nil
}
