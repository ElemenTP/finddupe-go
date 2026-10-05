//go:build darwin

package extent

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Supported reports whether extent querying is implemented on this platform.
func Supported() bool { return true }

// Identity describes what Extent.Physical carries on this platform.
func Identity() string {
	return "physical offset (F_LOG2PHYS_EXT), APFS clone id for compressed files"
}

// errCloneIDFallback marks files whose extents the kernel refuses to report
// (decmpfs-compressed files return ENOTSUP), for which the APFS clone ID is
// used instead.
var errCloneIDFallback = errors.New("F_LOG2PHYS_EXT not supported for this file")

// struct log2phys from <sys/fcntl.h> is declared under #pragma pack(4):
//
//	#pragma pack(4)
//	struct log2phys {
//	    unsigned int l2p_flags;        /* offset 0  */
//	    off_t        l2p_contigbytes;  /* offset 4  */
//	    off_t        l2p_devoffset;    /* offset 12 */
//	};                                 /* sizeof 20 */
//
// The 4-byte packing must be reproduced explicitly: a Go struct with natural
// alignment would place the fields at 8 and 16 and read the kernel's output
// from the wrong bytes.
const (
	l2pSize      = 20
	l2pContigOff = 4
	l2pDevOff    = 12
)

// getattrlist(2) constants, verified against sys/attr.h.
const (
	attrBitMapCount      = 5
	attrCmnReturnedAttrs = 0x80000000
	attrCmnextCloneID    = 0x00000100

	fsoptNoFollow        = 0x00000001
	fsoptPackInvalAttrs  = 0x00000008
	fsoptAttrCmnExtended = 0x00000020
)

// query returns the physical extents of path.
//
// The primary source is fcntl(F_LOG2PHYS_EXT), called through the libSystem
// wrapper (unix.FcntlInt, no raw syscall), which maps a logical offset to a
// device byte offset plus the length of the contiguous run. That gives the same
// per-extent physical identity as Linux FIEMAP, so clone detection and partial
// sharing work exactly as on the other platforms.
//
// APFS returns ENOTSUP for decmpfs-compressed files (the whole file lives in a
// compressed container), so those fall back to the APFS clone ID from
// getattrlist(2), which is family-level: 100% or 0% shared, never partial.
func query(path string) ([]Extent, error) {
	extents, err := extentsFcntl(path)
	if err == nil {
		return extents, nil
	}
	if !errors.Is(err, errCloneIDFallback) {
		return nil, err
	}
	return cloneIDExtents(path)
}

// extentsFcntl enumerates extents with fcntl(F_LOG2PHYS_EXT), skipping whole
// contiguous runs. errCloneIDFallback is returned when the filesystem does not
// implement the query for this file.
func extentsFcntl(path string) ([]Extent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size <= 0 {
		return nil, nil
	}

	blksize := int64(4096)
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Blksize > 0 {
		blksize = int64(st.Blksize)
	}

	var extents []Extent
	offset := int64(0)

	// The request/response record lives on the heap on purpose: fcntl takes its
	// argument as a plain int, so the address handed to the kernel has no
	// pointer semantics and a stack-allocated record could be moved by stack
	// growth between the conversion and the call. Heap objects are not moved by
	// the garbage collector, and KeepAlive keeps this one reachable across the
	// call. One record is reused for every step of the walk.
	rec := new([l2pSize]byte)

	for offset < size {
		binary.LittleEndian.PutUint64(rec[l2pContigOff:l2pDevOff], uint64(size-offset))
		binary.LittleEndian.PutUint64(rec[l2pDevOff:l2pSize], uint64(offset))

		_, err := unix.FcntlInt(f.Fd(), unix.F_LOG2PHYS_EXT, int(uintptr(unsafe.Pointer(&rec[0]))))
		runtime.KeepAlive(rec)

		if err != nil {
			var errno syscall.Errno
			if errors.As(err, &errno) {
				switch errno {
				case unix.ENOTSUP, unix.EOPNOTSUPP, unix.EINVAL, unix.ENOTTY, unix.ENOSYS:
					return nil, errCloneIDFallback
				case unix.ERANGE:
					// Past the end of the mapped range.
					return extents, nil
				}
			}
			return nil, err
		}

		contig := int64(binary.LittleEndian.Uint64(rec[l2pContigOff:l2pDevOff]))
		devOffset := int64(binary.LittleEndian.Uint64(rec[l2pDevOff:l2pSize]))

		// A non-positive device offset is a hole or an unmapped range; the
		// length still advances the walk.
		if devOffset > 0 && contig > 0 {
			extents = append(extents, Extent{
				Logical:  uint64(offset),
				Physical: uint64(devOffset),
				Length:   uint64(contig),
			})
		}

		if contig <= 0 {
			contig = blksize
		}
		offset += contig
	}

	return extents, nil
}

// cloneIDExtents returns one synthetic extent whose Physical field carries the
// APFS clone ID. Every file of one clone family (an original and the copies made
// by clonefile(2)) reports the same clone ID, while independent files report
// different ones, so the generic Equal/SharedWithOthers comparisons still work.
func cloneIDExtents(path string) ([]Extent, error) {
	clone, err := cloneID(path)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 {
		return nil, nil
	}

	return []Extent{{
		Logical:  0,
		Physical: clone, // APFS clone family identity, not a device offset
		Length:   uint64(info.Size()),
		Opaque:   true,
	}}, nil
}

// cloneID returns the APFS clone ID of path using getattrlist(2).
// ErrUnsupported is returned when the filesystem does not provide one.
func cloneID(path string) (uint64, error) {
	cpath, err := unix.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}

	attrs := unix.Attrlist{
		Bitmapcount: attrBitMapCount,
		Commonattr:  attrCmnReturnedAttrs,
		Forkattr:    attrCmnextCloneID,
	}
	options := uintptr(fsoptNoFollow | fsoptPackInvalAttrs | fsoptAttrCmnExtended)

	// getattrlist is called through the raw trap on purpose: golang.org/x/sys
	// (through v0.48.0) exports no libSystem wrapper for it (it has Setattrlist
	// and the deprecated SYS_* numbers only), and cgo would break the
	// CGO_ENABLED=0 release/cross-builds. Apple's deprecation note is about
	// direct syscalls in general, so this is isolated here and every failure
	// path degrades to ErrUnsupported; an OS that drops the trap reports
	// "cannot tell" rather than a wrong answer. If x/sys ever exports
	// Getattrlist, this is the single call site to switch.
	var out [64]byte
	_, _, errno := syscall.Syscall6(
		unix.SYS_GETATTRLIST,
		uintptr(unsafe.Pointer(cpath)),
		uintptr(unsafe.Pointer(&attrs)),
		uintptr(unsafe.Pointer(&out[0])),
		uintptr(len(out)),
		options,
		0,
	)
	if errno != 0 {
		if errno == unix.EINVAL || errno == unix.ENOTSUP ||
			errno == unix.EOPNOTSUPP || errno == unix.ENOTTY {
			return 0, ErrUnsupported
		}
		return 0, errno
	}

	// Layout with FSOPT_PACK_INVAL_ATTRS:
	//   u32 total length @0
	//   attribute_set_t returned @4 (forkattr @20)
	//   u64 clone id @24
	totalLen := binary.LittleEndian.Uint32(out[0:4])
	if totalLen < 32 {
		return 0, fmt.Errorf("%w: getattrlist returned %d bytes", ErrUnsupported, totalLen)
	}

	forkattr := binary.LittleEndian.Uint32(out[20:24])
	if forkattr&attrCmnextCloneID == 0 {
		return 0, fmt.Errorf("%w: no clone id in returned attributes (%#x)", ErrUnsupported, forkattr)
	}

	id := binary.LittleEndian.Uint64(out[24:32])
	if id == 0 {
		return 0, fmt.Errorf("%w: clone id is zero", ErrUnsupported)
	}
	return id, nil
}
