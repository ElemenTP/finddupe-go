//go:build windows

package volinfo

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	clusterMu    sync.Mutex
	clusterCache = map[string]uint64{}

	procGetDiskFreeSpaceW  = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW")
	procGetVolumePathNameW = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetVolumePathNameW")
)

// ClusterSize returns the cluster size (allocation unit) of the volume that
// contains path, caching results per volume mount point. Block cloning can only
// duplicate whole clusters, and the extent query needs the size to turn cluster
// numbers into byte offsets.
func ClusterSize(path string) (uint64, error) {
	root, rootErr := volumeRoot(path)
	if rootErr != nil {
		return 0, rootErr
	}

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

// volumeRoot returns the mount point the path belongs to. filepath.VolumeName
// would answer with the drive letter, which is the wrong volume for a volume
// mounted at a folder (C:\mnt\vol): the cluster size and the extent offsets
// would then be taken from the wrong filesystem and the clone would be
// misaligned or rejected.
func volumeRoot(path string) (string, error) {
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		abs = path
	}

	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return "", err
	}

	buf := make([]uint16, windows.MAX_PATH+1)
	n, _, callErr := procGetVolumePathNameW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if n == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
			return "", errno
		}
		return "", errors.New("GetVolumePathNameW failed")
	}

	root := windows.UTF16ToString(buf[:n])
	if root == "" {
		return "", errors.New("unknown volume mount point")
	}
	// Mount points are cached in a canonical form so two spellings of the same
	// volume share one entry.
	if !strings.HasSuffix(root, `\`) {
		root += `\`
	}
	return root, nil
}
