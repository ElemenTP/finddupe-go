//go:build windows

package volinfo

import (
	"errors"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	clusterMu    sync.Mutex
	clusterCache = map[string]uint64{}

	procGetDiskFreeSpaceW = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW")
)

// ClusterSize returns the cluster size (allocation unit) of the volume that
// contains path, caching results per volume root. Block cloning can only
// duplicate whole clusters, and the extent query needs the size to turn cluster
// numbers into byte offsets.
func ClusterSize(path string) (uint64, error) {
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		abs = path
	}
	root := filepath.VolumeName(abs) + `\`

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
		// LazyProc.Call always returns a non-nil error, so it is the errno that
		// says whether the call failed or merely reported zero.
		if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
			return 0, errno
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
