//go:build !linux && !darwin && !windows

package extent

// Supported reports whether extent querying is implemented on this platform.
func Supported() bool { return false }

// Identity describes what Extent.Physical carries on this platform.
func Identity() string { return "none" }

// query always reports ErrUnsupported on platforms without an implementation.
func query(_ string) ([]Extent, error) {
	return nil, ErrUnsupported
}
