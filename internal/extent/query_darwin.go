//go:build darwin

package extent

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Supported reports whether extent querying is implemented on this platform.
func Supported() bool { return true }

// Identity describes what Extent.Physical carries on this platform.
func Identity() string { return "APFS clone id" }

// getattrlist(2) constants, verified against sys/attr.h.
const (
	attrBitMapCount      = 5
	attrCmnReturnedAttrs = 0x80000000
	attrCmnextCloneID    = 0x00000100

	fsoptNoFollow        = 0x00000001
	fsoptPackInvalAttrs  = 0x00000008
	fsoptAttrCmnExtended = 0x00000020
)

// query returns one synthetic extent whose Physical field carries the APFS
// clone ID.
//
// Every file of one clone family (an original and the copies made by
// clonefile(2)) reports the same clone ID, while independent files report
// different ones. Using the clone ID as the physical identity lets the generic
// sharing comparisons (Equal, SharedWithOthers) work unchanged.
//
// APFS only exposes family-level sharing this way, not per-extent byte ranges,
// so a file is either fully shared with its family or not at all.
func query(path string) ([]Extent, error) {
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
