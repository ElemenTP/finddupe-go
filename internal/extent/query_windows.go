//go:build windows

package extent

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"finddupe/internal/volinfo"
)

// Supported reports whether extent querying is implemented on this platform.
func Supported() bool { return true }

// Identity describes what Extent.Physical carries on this platform.
func Identity() string { return "physical LCN (FSCTL_GET_RETRIEVAL_POINTERS)" }

// startingVcnInput mirrors STARTING_VCN_INPUT_BUFFER.
type startingVcnInput struct {
	StartingVcn int64
}

// query returns the physical extents of path using FSCTL_GET_RETRIEVAL_POINTERS,
// which reports the VCN→LCN mapping on both NTFS and ReFS. ReFS block clones
// make two files reference the same LCNs, so comparing the mappings reveals
// extent sharing.
func query(path string) ([]Extent, error) {
	cluster, err := volinfo.ClusterSize(path)
	if err != nil {
		return nil, ErrUnsupported
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	handle := windows.Handle(f.Fd())
	input := startingVcnInput{StartingVcn: 0}
	bufSize := uint32(64 * 1024)

	for {
		buf := make([]byte, bufSize)
		var returned uint32

		ioErr := windows.DeviceIoControl(
			handle,
			windows.FSCTL_GET_RETRIEVAL_POINTERS,
			(*byte)(unsafe.Pointer(&input)),
			uint32(unsafe.Sizeof(input)),
			&buf[0],
			bufSize,
			&returned,
			nil,
		)
		if ioErr != nil {
			if errors.Is(ioErr, windows.ERROR_MORE_DATA) {
				if bufSize >= 32*1024*1024 {
					return nil, ioErr
				}
				bufSize *= 2
				continue
			}
			if isUnsupportedWindowsErr(ioErr) {
				return nil, ErrUnsupported
			}
			return nil, ioErr
		}

		// A driver that reports success without writing anything has not answered
		// the query (observed on a ReFS Dev Drive): an empty mapping would be read
		// as "this file shares nothing", which is a different statement.
		if returned == 0 {
			return nil, fmt.Errorf("%w: FSCTL_GET_RETRIEVAL_POINTERS returned no data", ErrUnsupported)
		}

		// The count comes from the driver: bound it by what the buffer can hold
		// before it sizes an allocation.
		extentCount := boundedExtentCount(*(*uint32)(unsafe.Pointer(&buf[0])), len(buf))
		prevVcn := *(*int64)(unsafe.Pointer(&buf[8]))

		out := make([]Extent, 0, extentCount)
		for i := range extentCount {
			base := retrievalPointersHeaderSize + int(i)*retrievalPointerPairSize
			if base+retrievalPointerPairSize > len(buf) {
				break
			}

			nextVcn := *(*int64)(unsafe.Pointer(&buf[base]))
			lcn := *(*int64)(unsafe.Pointer(&buf[base+8]))
			lengthClusters := nextVcn - prevVcn

			if lcn >= 0 && lengthClusters > 0 {
				out = append(out, Extent{
					Logical:  uint64(prevVcn) * cluster,
					Physical: uint64(lcn) * cluster,
					Length:   uint64(lengthClusters) * cluster,
				})
			}
			prevVcn = nextVcn
		}
		return out, nil
	}
}

// isUnsupportedWindowsErr reports whether the ioctl failed because the
// filesystem does not support retrieval pointers. ERROR_INVALID_PARAMETER is
// deliberately not in this list: it is also the answer to a malformed request,
// and folding it in would hide a wrong buffer size or structure behind "this
// filesystem does not support extents". Callers treat any query error as
// "unavailable", so a rejected request still degrades gracefully.
func isUnsupportedWindowsErr(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED)
}
