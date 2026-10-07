//go:build windows

package volinfo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// Cluster size bounds used to validate the answer of the handle-based fallback:
// every filesystem the tools support uses a power of two between these values,
// so a reply that does not fit was read from the wrong structure.
const (
	minClusterSize = 512
	maxClusterSize = 64 * 1024 * 1024
)

// fileFsSizeInformationClass is the FS_INFORMATION_CLASS value for
// FILE_FS_SIZE_INFORMATION. It is *not* a FILE_INFO_BY_HANDLE_CLASS value: 3 is
// FileRenameInfo there, which GetFileInformationByHandleEx rejects (or answers
// with FILE_NAME_INFO), so the volume query must go through
// NtQueryVolumeInformationFile — which is what the class belongs to. The answer
// is validated below, because a wrong information class is exactly the kind of
// mistake that used to slip through here.
const fileFsSizeInformationClass = 3

// ioStatusBlock mirrors IO_STATUS_BLOCK for NtQueryVolumeInformationFile.
type ioStatusBlock struct {
	Status      uintptr
	Information uintptr
}

var procNtQueryVolumeInformationFile = windows.NewLazySystemDLL("ntdll.dll").
	NewProc("NtQueryVolumeInformationFile")

// fileFsSizeInformation mirrors FILE_FS_SIZE_INFORMATION.
type fileFsSizeInformation struct {
	TotalAllocationUnits     int64
	AvailableAllocationUnits int64
	SectorsPerAllocationUnit uint32
	BytesPerSector           uint32
}

// ClusterSize returns the cluster size (allocation unit) of the volume that
// contains path, caching results per volume. Block cloning can only duplicate
// whole clusters, and the extent query needs the size to turn cluster numbers
// into byte offsets.
//
// Three ways are tried, because the first two depend on resolving a volume root
// and a volume may refuse that (a ReFS Dev Drive answered ERROR_FILE_NOT_FOUND
// for both the mount point and the drive letter):
//
//  1. GetDiskFreeSpaceW on the mount point of the path (correct for volumes
//     mounted at a folder, which filepath.VolumeName cannot express);
//  2. GetDiskFreeSpaceW on the drive letter;
//  3. the volume's own answer through a handle on the path itself
//     (FILE_FS_SIZE_INFORMATION), which needs no root at all.
//
// path may be a file or a directory; it must exist for the third attempt.
func ClusterSize(path string) (uint64, error) {
	abs := absolutePath(path)

	if size, ok := cachedClusterSize(abs); ok {
		return size, nil
	}

	roots, mountErr := candidateRoots(abs)
	attempts := make([]error, 0, len(roots)+2)
	if mountErr != nil {
		attempts = append(attempts, fmt.Errorf("volume mount point of %s: %w", abs, mountErr))
	}

	for _, root := range roots {
		size, err := clusterSizeOfRoot(root)
		if err != nil {
			attempts = append(attempts, fmt.Errorf("GetDiskFreeSpaceW(%q): %w", root, err))
			continue
		}
		storeClusterSize(root, size)
		return size, nil
	}

	size, err := clusterSizeFromHandle(abs)
	if err != nil {
		attempts = append(attempts, fmt.Errorf("FILE_FS_SIZE_INFORMATION on %s: %w", abs, err))
		return 0, errors.Join(attempts...)
	}
	storeClusterSize(volumeCacheKey(abs), size)
	return size, nil
}

// absolutePath makes path absolute, falling back to the input when it cannot.
func absolutePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// candidateRoots returns the volume roots to ask about, mount point first, plus
// the error the mount point lookup hit (it is diagnostic even when the drive
// letter still works).
func candidateRoots(abs string) ([]string, error) {
	var (
		roots []string
		err   error
	)

	if mount, mountErr := volumeMountPoint(abs); mountErr != nil {
		err = mountErr
	} else if mount != "" {
		roots = append(roots, mount)
	}
	if drive := filepath.VolumeName(abs); drive != "" {
		if root := drive + `\`; len(roots) == 0 || roots[0] != root {
			roots = append(roots, root)
		}
	}
	return roots, err
}

// volumeMountPoint returns the mount point the path belongs to. It is the
// correct answer for a volume mounted at a folder, where filepath.VolumeName
// would name the drive that hosts the mount point instead.
func volumeMountPoint(abs string) (string, error) {
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
	if n == 0 || int(n) > len(buf) {
		if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
			return "", fmt.Errorf("GetVolumePathNameW: %w", errno)
		}
		return "", errors.New("GetVolumePathNameW returned no path")
	}

	mount := windows.UTF16ToString(buf[:n])
	if mount == "" {
		return "", errors.New("GetVolumePathNameW returned an empty path")
	}
	return withTrailingSeparator(mount), nil
}

// withTrailingSeparator returns root with exactly one trailing backslash, which
// GetDiskFreeSpaceW requires.
func withTrailingSeparator(root string) string {
	for len(root) > 1 && (root[len(root)-1] == '\\' || root[len(root)-1] == '/') {
		root = root[:len(root)-1]
	}
	return root + `\`
}

// clusterSizeOfRoot asks GetDiskFreeSpaceW about one root path.
func clusterSizeOfRoot(root string) (uint64, error) {
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
		return 0, errors.New("GetDiskFreeSpaceW reported a zero cluster size")
	}
	return size, nil
}

// clusterSizeFromHandle asks the volume through a handle on the path itself:
// FILE_FS_SIZE_INFORMATION reports the sectors per allocation unit and the bytes
// per sector of the volume that hosts the handle, with no volume root involved.
func clusterSizeFromHandle(abs string) (uint64, error) {
	file, err := os.Open(abs)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var (
		info   fileFsSizeInformation
		status ioStatusBlock
	)
	ntStatus, _, _ := procNtQueryVolumeInformationFile.Call(
		file.Fd(),
		uintptr(unsafe.Pointer(&status)),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		fileFsSizeInformationClass,
	)
	// The call reports through both its NTSTATUS return value and the
	// IO_STATUS_BLOCK; either one being non-zero means the buffer was not filled.
	if windows.NTStatus(ntStatus) != windows.STATUS_SUCCESS {
		return 0, windows.NTStatus(ntStatus)
	}
	if windows.NTStatus(status.Status) != windows.STATUS_SUCCESS {
		return 0, windows.NTStatus(status.Status)
	}

	size := uint64(info.SectorsPerAllocationUnit) * uint64(info.BytesPerSector)
	if size < minClusterSize || size > maxClusterSize || size&(size-1) != 0 {
		return 0, fmt.Errorf("implausible cluster size %d (%d sectors of %d bytes)",
			size, info.SectorsPerAllocationUnit, info.BytesPerSector)
	}
	return size, nil
}

// volumeCacheKey is the stable key a volume's answer is cached under.
func volumeCacheKey(abs string) string {
	if drive := filepath.VolumeName(abs); drive != "" {
		return drive + `\`
	}
	return filepath.Dir(abs)
}

// cachedClusterSize returns a cached answer for the path's volume, if any.
func cachedClusterSize(abs string) (uint64, bool) {
	clusterMu.Lock()
	defer clusterMu.Unlock()

	if size, ok := clusterCache[volumeCacheKey(abs)]; ok {
		return size, true
	}
	roots, _ := candidateRoots(abs)
	for _, root := range roots {
		if size, ok := clusterCache[root]; ok {
			return size, true
		}
	}
	return 0, false
}

// storeClusterSize caches an answer under the root that produced it and under
// its canonical spelling, so later lookups hit it immediately.
func storeClusterSize(root string, size uint64) {
	clusterMu.Lock()
	defer clusterMu.Unlock()

	clusterCache[root] = size
	clusterCache[withTrailingSeparator(root)] = size
}
