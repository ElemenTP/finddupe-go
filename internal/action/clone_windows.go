//go:build windows

package action

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// dst is the temporary name the clone is about to create, so it does not
	// exist yet: the volume (and therefore the cluster size) is resolved from the
	// directory it will live in.
	cluster, err := volinfo.ClusterSize(filepath.Dir(dst))
	if err != nil {
		return errors.Join(ErrCoWNotSupported, fmt.Errorf("cluster size of %s: %w", filepath.Dir(dst), err))
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %s: %w", src, err)
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source %s: %w", src, err)
	}
	size := info.Size()

	dstFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create destination %s: %w", dst, err)
	}
	defer dstFile.Close()

	dstHandle := windows.Handle(dstFile.Fd())

	// The destination must cover the range that is about to be cloned.
	if truncErr := dstFile.Truncate(size); truncErr != nil {
		return fmt.Errorf("size destination %s to %d bytes: %w", dst, size, truncErr)
	}

	// Reserving the destination clusters keeps the clone from being fragmented,
	// but it is an optimization: a volume that rejects the call (a ReFS Dev Drive
	// answered ERROR_FILE_NOT_FOUND) must not stop the clone, so the failure is
	// only reported if the clone itself fails.
	var allocErr error
	if size > 0 {
		alloc := size
		allocErr = windows.SetFileInformationByHandle(
			dstHandle,
			windows.FileAllocationInfo,
			(*byte)(unsafe.Pointer(&alloc)),
			uint32(unsafe.Sizeof(alloc)),
		)
	}

	// Block cloning only operates on whole clusters. A file smaller than one
	// cluster has no clonable extent, so reporting it as cloned would be a
	// lie; reject it instead of silently writing a full copy.
	aligned := size - size%int64(cluster)
	if aligned == 0 {
		return errors.Join(ErrCoWNotSupported,
			fmt.Errorf("file is %d bytes, smaller than the %d-byte cluster", size, cluster))
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
		detail := fmt.Errorf("duplicate %d bytes of %s at offset 0: %w", aligned, src, ioErr)
		if allocErr != nil {
			detail = fmt.Errorf("%w (destination preallocation also failed: %w)", detail, allocErr)
		}
		if isBlockCloneUnsupported(ioErr) {
			return errors.Join(ErrCoWNotSupported, detail)
		}
		return detail
	}

	if tail := size - aligned; tail > 0 {
		// The clone covers whole clusters only; the trailing bytes have to be
		// copied explicitly, at their own offset in the destination.
		if copyErr := copyTailAt(dstFile, srcFile, aligned, aligned, tail); copyErr != nil {
			return fmt.Errorf("copy the %d-byte tail of %s: %w", tail, src, copyErr)
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
