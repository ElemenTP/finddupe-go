//go:build windows

package action

import (
	"encoding/binary"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"finddupe/internal/volinfo"
)

// duplicateExtentsDataSize is sizeof(DUPLICATE_EXTENTS_DATA): a HANDLE followed
// by three 8-byte-aligned LARGE_INTEGERs, i.e. 4 bytes of padding after the
// handle on 32-bit Windows.
const duplicateExtentsDataSize = 32

// buildDuplicateExtentsData lays out the DUPLICATE_EXTENTS_DATA input by hand.
// A Go struct mirroring the C type would be 28 bytes with different field
// offsets on windows/386 (Go aligns int64 to 4 there, MSVC to 8), and the ioctl
// would be rejected — or, worse, read ByteCount out of bounds.
func buildDuplicateExtentsData(handle windows.Handle, sourceOffset, targetOffset, byteCount int64) [duplicateExtentsDataSize]byte {
	var data [duplicateExtentsDataSize]byte
	binary.LittleEndian.PutUint64(data[0:8], uint64(handle)) // HANDLE + padding
	binary.LittleEndian.PutUint64(data[8:16], uint64(sourceOffset))
	binary.LittleEndian.PutUint64(data[16:24], uint64(targetOffset))
	binary.LittleEndian.PutUint64(data[24:32], uint64(byteCount))
	return data
}

// clonePlatformFile creates a CoW (block) clone of src at dst. dst must not
// exist. Windows supports this on ReFS volumes (including Dev Drive) via
// FSCTL_DUPLICATE_EXTENTS_TO_FILE; NTFS returns ERROR_INVALID_FUNCTION.
func clonePlatformFile(src, dst string) error {
	cluster, err := volinfo.ClusterSize(dst)
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

	data := buildDuplicateExtentsData(windows.Handle(srcFile.Fd()), 0, 0, aligned)

	var returned uint32
	ioErr := windows.DeviceIoControl(
		dstHandle,
		windows.FSCTL_DUPLICATE_EXTENTS_TO_FILE,
		&data[0],
		duplicateExtentsDataSize,
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
		// The clone covers whole clusters only; the trailing bytes have to be
		// copied explicitly, at their own offset in the destination.
		if copyErr := copyTailAt(dstFile, srcFile, aligned, aligned, tail); copyErr != nil {
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
