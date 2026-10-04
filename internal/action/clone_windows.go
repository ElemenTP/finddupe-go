//go:build windows

package action

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// duplicateExtentsData mirrors DUPLICATE_EXTENTS_DATA.
type duplicateExtentsData struct {
	FileHandle       windows.Handle
	SourceFileOffset int64
	TargetFileOffset int64
	ByteCount        int64
}

var (
	windowsClusterMu    sync.Mutex
	windowsClusterCache = map[string]uint64{}

	procGetDiskFreeSpaceW = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW")
)

// clonePlatformFile creates a CoW (block) clone of src at dst. dst must not
// exist. Windows supports this on ReFS volumes (including Dev Drive) via
// FSCTL_DUPLICATE_EXTENTS_TO_FILE; NTFS returns ERROR_INVALID_FUNCTION.
func clonePlatformFile(src, dst string) error {
	cluster, err := windowsClusterSize(dst)
	if err != nil {
		return errors.Join(ErrCoWNotSupported, err)
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}
	size := info.Size()

	dstFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	dstHandle := windows.Handle(dstFile.Fd())

	// Block cloning requires the destination clusters to be allocated.
	if size > 0 {
		alloc := size
		if allocErr := windows.SetFileInformationByHandle(
			dstHandle,
			windows.FileAllocationInfo,
			(*byte)(unsafe.Pointer(&alloc)),
			uint32(unsafe.Sizeof(alloc)),
		); allocErr != nil {
			return errors.Join(ErrCoWNotSupported, allocErr)
		}
	}
	if truncErr := dstFile.Truncate(size); truncErr != nil {
		return truncErr
	}

	// Block cloning only operates on whole clusters. A file smaller than one
	// cluster has no clonable extent, so reporting it as cloned would be a
	// lie; reject it instead of silently writing a full copy.
	aligned := size - size%int64(cluster)
	if aligned == 0 {
		return errors.Join(ErrCoWNotSupported, errors.New("file is smaller than one cluster"))
	}

	data := duplicateExtentsData{
		FileHandle:       windows.Handle(srcFile.Fd()),
		SourceFileOffset: 0,
		TargetFileOffset: 0,
		ByteCount:        aligned,
	}

	var returned uint32
	ioErr := windows.DeviceIoControl(
		dstHandle,
		windows.FSCTL_DUPLICATE_EXTENTS_TO_FILE,
		(*byte)(unsafe.Pointer(&data)),
		uint32(unsafe.Sizeof(data)),
		nil,
		0,
		&returned,
		nil,
	)
	if ioErr != nil {
		if isBlockCloneUnsupported(ioErr) {
			return errors.Join(ErrCoWNotSupported, ioErr)
		}
		return ioErr
	}

	if tail := size - aligned; tail > 0 {
		if _, seekErr := srcFile.Seek(aligned, io.SeekStart); seekErr != nil {
			return seekErr
		}
		if _, copyErr := io.CopyN(dstFile, srcFile, tail); copyErr != nil {
			return copyErr
		}
	}

	return nil
}

// isBlockCloneUnsupported reports whether block cloning failed because the
// volume does not support it.
func isBlockCloneUnsupported(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.ERROR_INVALID_PARAMETER)
}

// windowsClusterSize returns the cluster size of the volume containing dst.
func windowsClusterSize(path string) (uint64, error) {
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		abs = path
	}
	root := filepath.VolumeName(abs) + `\`

	windowsClusterMu.Lock()
	cached, ok := windowsClusterCache[root]
	windowsClusterMu.Unlock()
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

	windowsClusterMu.Lock()
	windowsClusterCache[root] = size
	windowsClusterMu.Unlock()
	return size, nil
}
