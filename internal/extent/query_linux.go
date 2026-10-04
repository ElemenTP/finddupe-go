//go:build linux

package extent

import (
	"os"
	"syscall"
	"unsafe"
)

// Supported reports whether extent querying is implemented on this platform.
func Supported() bool { return true }

// Identity describes what Extent.Physical carries on this platform.
func Identity() string { return "physical offset (FIEMAP)" }

const (
	// fsIOCFiemap is FS_IOC_FIEMAP (linux/fs.h).
	fsIOCFiemap = 0xC020660B

	fiemapExtentLast    = 0x00000001
	fiemapExtentUnknown = 0x00000002
	fiemapExtentEncoded = 0x00000008
	fiemapExtentShared  = 0x00002000

	// fiemapFlagSync forces the file's dirty data to be written back before the
	// mapping is produced; without it freshly written files report zero-length
	// DELALLOC extents.
	fiemapFlagSync = 0x00000001

	fiemapHeaderSize   = 32
	fiemapExtentSize   = 56
	fiemapBatchExtents = 128
)

// fiemapHeader mirrors struct fiemap (linux/fiemap.h). Note that fm_flags and
// fm_reserved are 32-bit, making the header 32 bytes and matching the size
// encoded in FS_IOC_FIEMAP.
type fiemapHeader struct {
	Start         uint64
	Length        uint64
	Flags         uint32
	MappedExtents uint32
	ExtentCount   uint32
	Reserved      uint32
}

// fiemapExtent mirrors struct fiemap_extent (linux/fiemap.h).
type fiemapExtent struct {
	Logical    uint64
	Physical   uint64
	Length     uint64
	Reserved64 [2]uint64
	Flags      uint32
	Reserved   [3]uint32
}

// query returns the physical extents of path using the FIEMAP ioctl.
func query(path string) ([]Extent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Extent
	start := uint64(0)

	for {
		buf := make([]byte, fiemapHeaderSize+fiemapBatchExtents*fiemapExtentSize)
		hdr := (*fiemapHeader)(unsafe.Pointer(&buf[0]))
		hdr.Start = start
		hdr.Length = ^uint64(0) // to end of file
		hdr.Flags = fiemapFlagSync
		hdr.ExtentCount = fiemapBatchExtents

		_, _, errno := syscall.Syscall(
			syscall.SYS_IOCTL,
			f.Fd(),
			fsIOCFiemap,
			uintptr(unsafe.Pointer(&buf[0])),
		)
		if errno != 0 {
			if isUnsupportedErrno(errno) {
				return nil, ErrUnsupported
			}
			return nil, errno
		}

		n := int(hdr.MappedExtents)
		if n <= 0 {
			break
		}

		extents := unsafe.Slice((*fiemapExtent)(unsafe.Pointer(&buf[fiemapHeaderSize])), n)
		last, next, progressed := appendBatch(&out, extents)
		if !progressed {
			break
		}
		if last {
			break
		}
		start = next
	}

	return out, nil
}

// appendBatch appends the parsed extents of one FIEMAP response and reports
// whether the last extent was seen, the offset to continue from, and whether
// progress was made (guards against a non-advancing kernel response).
func appendBatch(out *[]Extent, extents []fiemapExtent) (bool, uint64, bool) {
	var (
		last       bool
		nextStart  uint64
		progressed bool
	)

	for i := range extents {
		e := &extents[i]
		if e.Length == 0 {
			continue
		}
		*out = append(*out, Extent{
			Logical:  e.Logical,
			Physical: e.Physical,
			Length:   e.Length,
			Shared:   e.Flags&fiemapExtentShared != 0,
			Encoded:  e.Flags&(fiemapExtentEncoded|fiemapExtentUnknown) != 0,
		})
		if e.Flags&fiemapExtentLast != 0 {
			last = true
		}
		if end := e.Logical + e.Length; end > nextStart {
			nextStart = end
			progressed = true
		}
	}
	return last, nextStart, progressed
}

// isUnsupportedErrno reports whether the ioctl failed because the filesystem
// cannot report extents.
func isUnsupportedErrno(errno syscall.Errno) bool {
	return errno == syscall.ENOTTY ||
		errno == syscall.EINVAL ||
		errno == syscall.EOPNOTSUPP
}
