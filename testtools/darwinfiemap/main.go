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
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"finddupe/internal/extent"
	l2p "finddupe/internal/log2phys"
	"golang.org/x/sys/unix"
)

// log2phys is a byte-exact mirror of struct log2phys; the layout itself lives in
// internal/log2phys so the probe cannot drift away from what the extent query
// reads.
type log2phys struct {
	raw [l2p.Size]byte
}

func newLog2phys(flags uint32, contig, devOffset int64) log2phys {
	var l log2phys
	l.raw = l2p.Encode(contig, devOffset)
	return l
}

func (l *log2phys) flags() uint32    { return l2p.Flags(l.raw[:]) }
func (l *log2phys) contig() int64    { _, contig := l2p.Parse(l.raw[:]); return contig }
func (l *log2phys) devOffset() int64 { devOffset, _ := l2p.Parse(l.raw[:]); return devOffset }
func (l *log2phys) hex() string      { return fmt.Sprintf("% x", l.raw[:]) }

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

	if extents, qErr := extent.Query(path, info.Size()); qErr == nil {
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
			// ERANGE (the documented "past the end of the file" answer) and any
			// other error end the walk here; the step above records the errno.
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

	// clampedOverrun counts the steps the extent query has to clamp: the kernel
	// described a run longer than the bytes that are left, which would step the
	// offset past the end of the file. No real kernel has been seen doing it, so
	// seeing one here is exactly what the clamp is for.
	clampedOverrun := 0
	stepsLeft := size
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
		if s.contig > stepsLeft && stepsLeft >= 0 {
			clampedOverrun++
		}
		if s.contig > 0 {
			stepsLeft -= s.contig
		} else {
			stepsLeft -= 4096
		}
	}
	fmt.Printf("     summary: steps=%d mapped=%d holes=%d distinct-devoffsets=%d contig>0-steps=%d clamped-overruns=%d last-errno=%d size=%d\n",
		len(steps), mapped, holes, len(distinct), contigUsed, clampedOverrun, lastErr, size)
	if clampedOverrun > 0 {
		fmt.Printf("     NOTE: %d step(s) reported a contiguous run longer than the file has left;\n"+
			"           the extent query clamps these, report this log.\n", clampedOverrun)
	}
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
