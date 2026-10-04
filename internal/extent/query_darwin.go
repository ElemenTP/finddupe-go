//go:build darwin

package extent

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Supported reports whether extent querying is implemented on this platform.
func Supported() bool { return true }

// fLog2PhysExt is the F_LOG2PHYS_EXT fcntl command (Darwin, fcntl.h).
// It is undocumented but maps a logical offset to a physical device offset and
// is what APFS uses to expose extent sharing.
const fLog2PhysExt = 65

// log2phys mirrors struct log2phys (sys/fcntl.h): the caller sets DevOffset to
// the logical offset and ContigBytes to the remaining length; on return
// DevOffset holds the physical offset and ContigBytes the contiguous length.
type log2phys struct {
	Flags       uint32
	_           uint32
	ContigBytes int64
	DevOffset   int64
}

// Query returns the physical extents of path using fcntl(F_LOG2PHYS_EXT).
// On filesystems that do not expose extents (or sealed volumes) it returns
// ErrUnsupported.
func Query(path string) ([]Extent, error) {
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

	var out []Extent
	var off int64

	for off < size {
		l2p := log2phys{DevOffset: off, ContigBytes: size - off}

		_, _, errno := unix.Syscall(
			unix.SYS_FCNTL,
			f.Fd(),
			fLog2PhysExt,
			uintptr(unsafe.Pointer(&l2p)),
		)
		if errno != 0 {
			if errno == unix.EINVAL || errno == unix.ENOTTY ||
				errno == unix.ENOTSUP || errno == unix.EOPNOTSUPP {
				return nil, ErrUnsupported
			}
			return nil, errno
		}

		if l2p.DevOffset < 0 || l2p.ContigBytes <= 0 {
			break
		}

		out = append(out, Extent{
			Physical: uint64(l2p.DevOffset),
			Length:   uint64(l2p.ContigBytes),
		})
		off += l2p.ContigBytes
	}

	return out, nil
}
