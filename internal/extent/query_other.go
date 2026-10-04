//go:build !linux && !darwin && !windows

package extent

// Supported reports whether extent querying is implemented on this platform.
func Supported() bool { return false }

// Query always reports ErrUnsupported on platforms without an implementation.
func Query(_ string) ([]Extent, error) {
	return nil, ErrUnsupported
}
