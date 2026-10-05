//go:build darwin

// Command darwinfiemap probes whether macOS/APFS can enumerate a file's
// logical-to-physical extent mapping (a Linux-FIEMAP equivalent) and whether
// that mapping can tell APFS clones from independent copies.
//
// It calls fcntl(F_LOG2PHYS_EXT) / fcntl(F_LOG2PHYS) through the libSystem
// wrapper (golang.org/x/sys/unix.FcntlInt, no raw syscall), prints the raw
// per-step results, and compares files block by block.
//
// Usage: darwinfiemap <file> [file...]
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"finddupe/internal/extent"
	"golang.org/x/sys/unix"
)

// log2physSize is sizeof(struct log2phys) from <sys/fcntl.h>:
//
//	#pragma pack(4)
//	struct log2phys {
//	    unsigned int l2p_flags;        /* offset 0  */
//	    off_t        l2p_contigbytes;  /* offset 4  */
//	    off_t        l2p_devoffset;    /* offset 12 */
//	};                                 /* sizeof 20 */
//
// The 4-byte packing is essential: with Go's natural alignment the fields land
// at 8 and 16, so the kernel's output would be read from the wrong bytes.
const log2physSize = 20

// log2phys is a byte-exact mirror of struct log2phys.
type log2phys struct {
	raw [log2physSize]byte
}

func newLog2phys(flags uint32, contig, devOffset int64) log2phys {
	var l log2phys
	binary.LittleEndian.PutUint32(l.raw[0:4], flags)
	binary.LittleEndian.PutUint64(l.raw[4:12], uint64(contig))
	binary.LittleEndian.PutUint64(l.raw[12:20], uint64(devOffset))
	return l
}

func (l *log2phys) flags() uint32    { return binary.LittleEndian.Uint32(l.raw[0:4]) }
func (l *log2phys) contig() int64    { return int64(binary.LittleEndian.Uint64(l.raw[4:12])) }
func (l *log2phys) devOffset() int64 { return int64(binary.LittleEndian.Uint64(l.raw[12:20])) }
func (l *log2phys) hex() string      { return fmt.Sprintf("% x", l.raw[:]) }

// errRange is ERANGE, which F_LOG2PHYS[_EXT] returns past the end of a file.
const errRange = unix.ERANGE

// maxSteps bounds enumeration so a pathological answer cannot loop forever.
const maxSteps = 1 << 20

// variant is one call convention.
type variant struct {
	name  string
	cmd   int
	input func(offset, remaining int64) log2phys
	seek  bool
}

var variants = []variant{
	{
		name:  "A libc F_LOG2PHYS_EXT {devoffset=offset, contig=0} (clone_checker style)",
		cmd:   unix.F_LOG2PHYS_EXT,
		input: func(offset, _ int64) log2phys { return newLog2phys(0, 0, offset) },
	},
	{
		name:  "B libc F_LOG2PHYS_EXT {devoffset=offset, contig=remaining} (documented)",
		cmd:   unix.F_LOG2PHYS_EXT,
		input: func(offset, remaining int64) log2phys { return newLog2phys(0, remaining, offset) },
	},
	{
		name:  "C libc F_LOG2PHYS_EXT {devoffset=0, contig=remaining}",
		cmd:   unix.F_LOG2PHYS_EXT,
		input: func(_, remaining int64) log2phys { return newLog2phys(0, remaining, 0) },
	},
	{
		name:  "D libc F_LOG2PHYS after lseek(offset)",
		cmd:   unix.F_LOG2PHYS,
		input: func(_, _ int64) log2phys { return newLog2phys(0, 0, 0) },
		seek:  true,
	},
}

// step is one query result.
type step struct {
	offset    int64
	ret       int
	errno     syscall.Errno
	flags     uint32
	contig    int64
	devOffset int64
	raw       string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: darwinfiemap <file> [file...]")
		os.Exit(2)
	}

	files := os.Args[1:]
	for _, path := range files {
		inspect(path)
	}

	for i := range files {
		for j := i + 1; j < len(files); j++ {
			compare(files[i], files[j])
		}
	}
}

// inspect prints metadata and every variant's mapping for one file.
func inspect(path string) {
	fmt.Printf("\n=== %s ===\n", path)

	info, err := os.Stat(path)
	if err != nil {
		fmt.Printf("  stat: %v\n", err)
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		fmt.Printf("  stat: unexpected Sys() type\n")
		return
	}
	fmt.Printf("  size=%d blocks=%d blksize=%d dev=%d ino=%d\n",
		info.Size(), st.Blocks, st.Blksize, st.Dev, st.Ino)

	if extents, qErr := extent.Query(path); qErr == nil {
		fmt.Printf("  clone-id reference: identity=%s extents=%d", extent.Identity(), len(extents))
		for _, e := range extents {
			fmt.Printf(" physical=%d length=%d", e.Physical, e.Length)
		}
		fmt.Println()
	} else {
		fmt.Printf("  clone-id reference: %v\n", qErr)
	}

	f, err := os.Open(path)
	if err != nil {
		fmt.Printf("  open: %v\n", err)
		return
	}
	defer f.Close()

	size := info.Size()
	if size <= 0 {
		fmt.Printf("  (empty file, nothing to map)\n")
		return
	}

	for _, v := range variants {
		steps := enumerate(f, size, int64(st.Blksize), v, true)
		printSteps(v.name, steps, size)
	}
}

// enumerate runs one variant over the whole file. When byContig is true the
// loop skips whole contiguous runs, otherwise it walks every blksize block (used
// for comparisons so both files report the same logical offsets).
func enumerate(f *os.File, size, blksize int64, v variant, byContig bool) []step {
	steps := make([]step, 0, 64)
	offset := int64(0)

	// The record is heap-allocated once and reused: fcntl takes its argument as
	// a plain int, so a stack-allocated record could be moved by stack growth
	// between the conversion and the call.
	rec := new(log2phys)

	for offset < size && len(steps) < maxSteps {
		if v.seek {
			if _, err := f.Seek(offset, 0); err != nil {
				break
			}
		}

		*rec = v.input(offset, size-offset)
		ret, err := unix.FcntlInt(f.Fd(), v.cmd, int(uintptr(unsafe.Pointer(&rec.raw[0]))))
		runtime.KeepAlive(rec)

		var errno syscall.Errno
		if err != nil {
			if e, ok := err.(syscall.Errno); ok {
				errno = e
			}
		}

		steps = append(steps, step{
			offset:    offset,
			ret:       ret,
			errno:     errno,
			flags:     rec.flags(),
			contig:    rec.contig(),
			devOffset: rec.devOffset(),
			raw:       rec.hex(),
		})

		if err != nil {
			break
		}

		advance := blksize
		if byContig && rec.contig() > 0 {
			advance = rec.contig()
		}
		if advance <= 0 {
			advance = 4096
		}
		offset += advance
	}

	return steps
}

// printSteps prints the first few and last few results plus a summary.
func printSteps(name string, steps []step, size int64) {
	fmt.Printf("  -- %s --\n", name)
	if len(steps) == 0 {
		fmt.Printf("     no results\n")
		return
	}

	show := func(s step) {
		fmt.Printf("     off=%-10d ret=%-3d errno=%-3d flags=%-4d contig=%-10d devoffset=%-14d raw=[%s]\n",
			s.offset, s.ret, s.errno, s.flags, s.contig, s.devOffset, s.raw)
	}

	head := steps
	if len(head) > 4 {
		head = head[:4]
	}
	for _, s := range head {
		show(s)
	}
	if len(steps) > 6 {
		fmt.Printf("     ... (%d steps omitted) ...\n", len(steps)-6)
		for _, s := range steps[len(steps)-2:] {
			show(s)
		}
	}

	distinct := map[int64]struct{}{}
	holes, mapped, contigUsed, lastErr := 0, 0, 0, syscall.Errno(0)
	for _, s := range steps {
		distinct[s.devOffset] = struct{}{}
		if s.devOffset < 0 {
			holes++
		} else {
			mapped++
		}
		if s.contig > 0 {
			contigUsed++
		}
		if s.errno != 0 {
			lastErr = s.errno
		}
	}
	fmt.Printf("     summary: steps=%d mapped=%d holes=%d distinct-devoffsets=%d contig>0-steps=%d last-errno=%d size=%d\n",
		len(steps), mapped, holes, len(distinct), contigUsed, lastErr, size)
}

// compare walks two files block by block with variant A (falling back to B)
// and reports whether their physical mappings match.
func compare(pathA, pathB string) {
	fmt.Printf("\n=== compare %s vs %s ===\n", pathA, pathB)

	fa, errA := os.Open(pathA)
	fb, errB := os.Open(pathB)
	if errA != nil || errB != nil {
		fmt.Printf("  open failed: %v / %v\n", errA, errB)
		return
	}
	defer fa.Close()
	defer fb.Close()

	ia, sa := statOf(pathA)
	ib, sb := statOf(pathB)
	if ia == nil || ib == nil {
		fmt.Printf("  stat failed\n")
		return
	}
	if ia.Size() != ib.Size() || sa.Dev != sb.Dev {
		fmt.Printf("  sizes/devices differ (size %d vs %d, dev %d vs %d) - not clones\n",
			ia.Size(), ib.Size(), sa.Dev, sb.Dev)
		return
	}

	blksize := int64(sa.Blksize)
	if blksize <= 0 {
		blksize = 4096
	}

	v := variants[0]
	stepsA := enumerate(fa, ia.Size(), blksize, v, false)
	if len(stepsA) == 0 {
		// Variant A returned nothing; try the documented convention.
		v = variants[1]
		stepsA = enumerate(fa, ia.Size(), blksize, v, false)
	}
	stepsB := enumerate(fb, ib.Size(), blksize, v, false)

	if len(stepsA) == 0 || len(stepsB) == 0 {
		fmt.Printf("  no mapping returned (A=%d steps, B=%d steps)\n", len(stepsA), len(stepsB))
		return
	}

	n := min(len(stepsA), len(stepsB))
	equal, comparable, holesA, holesB := 0, 0, 0, 0
	firstDiff := int64(-1)

	for i := range n {
		a, b := stepsA[i].devOffset, stepsB[i].devOffset
		if a <= 0 {
			holesA++
		}
		if b <= 0 {
			holesB++
		}
		// Only positive device offsets are a usable mapping; 0/-1 means the
		// kernel did not report a location for that logical block.
		if a <= 0 || b <= 0 {
			continue
		}
		comparable++
		if a == b {
			equal++
			continue
		}
		if firstDiff < 0 {
			firstDiff = stepsA[i].offset
		}
	}

	fmt.Printf("  steps: A=%d B=%d; comparable=%d identical=%d holesA=%d holesB=%d\n",
		len(stepsA), len(stepsB), comparable, equal, holesA, holesB)

	switch {
	case comparable == 0:
		fmt.Printf("  => no usable device offsets returned; cannot tell\n")
	case firstDiff >= 0:
		fmt.Printf("  => NOT fully shared: first difference at logical offset %d\n", firstDiff)
	case len(stepsA) == len(stepsB):
		fmt.Printf("  => every comparable logical block maps to the same device offset (clone-like)\n")
	default:
		fmt.Printf("  => identical so far but step counts differ; inspect raw output above\n")
	}
}

// statOf returns the os.FileInfo and syscall.Stat_t for a path.
func statOf(path string) (os.FileInfo, *syscall.Stat_t) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, nil
	}
	return info, st
}
