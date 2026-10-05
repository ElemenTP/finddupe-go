package extent

// FSCTL_GET_RETRIEVAL_POINTERS returns a fixed layout: a 16-byte header
// (ExtentCount and the starting VCN) followed by 16-byte (next VCN, LCN) pairs,
// with the LCN 8-byte aligned. The constants live here rather than next to the
// Windows query so the bound below can be unit-tested on every platform.
const (
	retrievalPointersHeaderSize = 16
	retrievalPointerPairSize    = 16
)

// boundedExtentCount bounds a driver-supplied extent count by the number of
// pairs a buffer of bufLen bytes can hold. The count is used to size an
// allocation and to walk the buffer, so a bogus value from a driver must not be
// trusted: 2^32 pairs would reserve tens of gigabytes before the first read.
func boundedExtentCount(count uint32, bufLen int) uint32 {
	room := bufLen - retrievalPointersHeaderSize
	if room < retrievalPointerPairSize {
		return 0
	}
	return min(count, uint32(room/retrievalPointerPairSize)) //nolint:gosec // bufLen is a buffer size
}
