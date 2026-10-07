//go:build !windows

package volinfo

// ClusterSize reports that volume information is unavailable off Windows.
func ClusterSize(_ string) (uint64, error) {
	return 0, ErrUnsupported
}
