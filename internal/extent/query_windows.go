//go:build windows

package extent

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Supported reports whether extent querying is implemented on this platform.
func Supported() bool { return true }

const (
	retrievalPointersHeaderSize = 16
	retrievalPointerPairSize    = 16
)

// startingVcnInput mirrors STARTING_VCN_INPUT_BUFFER.
type startingVcnInput struct {
	StartingVcn int64
}

var (
	clusterMu    sync.Mutex
	clusterCache = map[string]uint64{}

	procGetDiskFreeSpaceW = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW")
)

// Query returns the physical extents of path using FSCTL_GET_RETRIEVAL_POINTERS,
// which reports the VCN→LCN mapping on both NTFS and ReFS. ReFS block clones
// make two files reference the same LCNs, so comparing the mappings reveals
// extent sharing.
func Query(path string) ([]Extent, error) {
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		abs = path
	}
	root := filepath.VolumeName(abs) + `\`

	cluster, err := clusterSize(root)
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

		extentCount := *(*uint32)(unsafe.Pointer(&buf[0]))
		prevVcn := *(*int64)(unsafe.Pointer(&buf[8]))

		out := make([]Extent, 0, extentCount)
		for i := uint32(0); i < extentCount; i++ {
			base := retrievalPointersHeaderSize + int(i)*retrievalPointerPairSize
			if base+retrievalPointerPairSize > len(buf) {
				break
			}

			nextVcn := *(*int64)(unsafe.Pointer(&buf[base]))
			lcn := *(*int64)(unsafe.Pointer(&buf[base+8]))
			lengthClusters := nextVcn - prevVcn

			if lcn >= 0 && lengthClusters > 0 {
				out = append(out, Extent{
					Physical: uint64(lcn) * cluster,
					Length:   uint64(lengthClusters) * cluster,
				})
			}
			prevVcn = nextVcn
		}
		return out, nil
	}
}

// clusterSize returns the volume cluster size for the given volume root,
// caching results per root.
func clusterSize(root string) (uint64, error) {
	clusterMu.Lock()
	cached, ok := clusterCache[root]
	clusterMu.Unlock()
	if ok {
		return cached, nil
	}

	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, err
	}

	var sectorsPerCluster, bytesPerSector, freeClusters, totalClusters uint32
	r1, _, callErr := procGetDiskFreeSpaceW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&sectorsPerCluster)),
		uintptr(unsafe.Pointer(&bytesPerSector)),
		uintptr(unsafe.Pointer(&freeClusters)),
		uintptr(unsafe.Pointer(&totalClusters)),
	)
	if r1 == 0 {
		if callErr != nil {
			return 0, callErr
		}
		return 0, errors.New("GetDiskFreeSpaceW failed")
	}

	size := uint64(sectorsPerCluster) * uint64(bytesPerSector)
	if size == 0 {
		return 0, errors.New("unknown cluster size")
	}

	clusterMu.Lock()
	clusterCache[root] = size
	clusterMu.Unlock()
	return size, nil
}

// isUnsupportedWindowsErr reports whether the ioctl failed because the
// filesystem does not support retrieval pointers.
func isUnsupportedWindowsErr(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.ERROR_INVALID_PARAMETER)
}
