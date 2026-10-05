// Package checksum computes 64-bit file signatures by reading the first 32KB of a file.
// The algorithm matches the original C finddupe for cross-version signature compatibility.
package checksum

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"finddupe/internal/dupe"
)

// BytesToChecksum is the number of bytes to read from the beginning of a file.
// This matches the C version's BYTES_DO_CHECKSUM_OF constant.
const BytesToChecksum = 32768

// fileStat holds the file facts the scanner needs. It is filled by one system
// call per file (see statFile).
type fileStat struct {
	Size     int64
	ModTime  time.Time
	Dev      uint64
	Inode    uint64
	NumLinks uint64
}

// Bit widths and shifts of the composite checksum algorithm. They are part of
// the on-disk-compatible signature format and must not be changed.
const (
	crcShiftA     = 8
	crcShiftB     = 24
	crcShiftC     = 9
	byteMask      = 0xff
	sumShift      = 1
	sumRotate     = 31
	checksumWidth = 32
)

// Info holds the results of a single file open: the weak signature, the
// physical file identity, and (for files <= BytesToChecksum) the full SHA-256.
type Info struct {
	// Signature is the 64-bit composite checksum of the first 32KB.
	Signature uint64

	// Dev is the filesystem/volume identifier (st_dev / volume serial).
	Dev uint64

	// Inode is the filesystem object identifier (inode / NTFS file index).
	Inode uint64

	// NumLinks is the number of hardlinks to the file (0 if unavailable).
	NumLinks uint64

	// SHA256 is the full-content hash when it was computed at no extra cost
	// (files <= BytesToChecksum); the zero value means "not yet computed".
	SHA256 [32]byte

	// ModTime is the file's modification time as observed while the checksum
	// was computed. It is the reference the executor re-checks before acting
	// on the file.
	ModTime time.Time
}

// Compute opens the file at path and returns its 64-bit composite checksum.
// The signature is (crc << 32) | sum, where crc and sum are computed from
// the first BytesToChecksum bytes, and fileSize is added to sum.
func Compute(path string, size int64) (uint64, error) {
	info, err := ComputeFileInfo(path, size)
	return info.Signature, err
}

// ComputeFileInfo opens the file once and returns the checksum signature,
// filesystem identity, hardlink count, and SHA-256 hash. On Windows, this uses
// GetFileInformationByHandle on the already-open handle — avoiding a
// second CreateFile call in the single-threaded walker.
//
// The file's current size must still equal size (the size observed by the
// walker): reading a different number of bytes would produce a signature for
// content other than the file that was grouped, which could later make an
// elimination act on a file that is no longer a duplicate.
//
// When size <= BytesToChecksum (32KB), the entire file is read for CRC so
// SHA-256 is also computed at zero additional cost. For larger files,
// SHA-256 is returned as zero (not yet computed).
func ComputeFileInfo(path string, size int64) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()

	stat, err := statFile(f)
	if err != nil {
		return Info{}, err
	}
	if stat.Size != size {
		return Info{}, fmt.Errorf("%w: size is %d, scanned %d", dupe.ErrFileChanged, stat.Size, size)
	}

	out := Info{
		ModTime:  stat.ModTime,
		Dev:      stat.Dev,
		Inode:    stat.Inode,
		NumLinks: stat.NumLinks,
	}

	if size <= BytesToChecksum {
		// File fits entirely in the CRC buffer — compute SHA-256 alongside CRC
		// at zero additional I/O cost.
		out.Signature, out.SHA256, err = computeBoth(f, size)
	} else {
		out.Signature, err = ComputeFromReader(f, size)
	}
	return out, err
}

// signatureBufferPool recycles the read buffer of the weak-checksum path: one
// 32 KiB buffer per active worker instead of one per scanned file, which on a
// scan of millions of files is the difference between a steady state and a
// stream of large short-lived allocations.
var signatureBufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, BytesToChecksum)
		return &buf
	},
}

// readSignatureBuffer reads up to size bytes from r into a pooled buffer and
// returns the bytes read together with the buffer that must be released.
func readSignatureBuffer(r io.Reader, size int64) ([]byte, *[]byte, error) {
	bufPtr, ok := signatureBufferPool.Get().(*[]byte)
	if !ok || cap(*bufPtr) < BytesToChecksum {
		fresh := make([]byte, BytesToChecksum)
		bufPtr = &fresh
	}

	buf := *bufPtr
	n, err := io.ReadFull(r, buf[:min(size, int64(len(buf)))])
	if err != nil && err != io.ErrUnexpectedEOF {
		signatureBufferPool.Put(bufPtr)
		return nil, nil, err
	}
	return buf[:n], bufPtr, nil
}

// signature computes the composite 64-bit weak checksum of buf, folding the file
// size into the sum component exactly like the C original (CalcCrc).
func signature(buf []byte, size int64) uint64 {
	var crc uint32
	var sum uint32

	for _, b := range buf {
		crc ^= uint32(b)
		sum += uint32(b)

		// These operations must use uint32 to match C's unsigned int overflow behavior.
		crc = (crc >> crcShiftA) ^ ((crc & byteMask) << crcShiftB) ^ ((crc & byteMask) << crcShiftC)
		sum = (sum << sumShift) + (sum >> sumRotate)
	}

	// CheckSum.Sum += (unsigned int)FileSize, as in C.
	sum += uint32(size) //nolint:gosec // size is folded into a 32-bit sum by design

	// Pack into 64-bit result, matching C's Checksum_t memory layout.
	return (uint64(crc) << checksumWidth) | uint64(sum)
}

// computeBoth reads the entire file (up to BytesToChecksum) and computes
// both the weak CRC signature and the SHA-256 hash. The file must be
// <= BytesToChecksum bytes.
func computeBoth(r io.Reader, size int64) (uint64, [32]byte, error) {
	buf, bufPtr, err := readSignatureBuffer(r, size)
	if err != nil {
		return 0, [32]byte{}, err
	}
	defer signatureBufferPool.Put(bufPtr)

	// SHA-256 of the complete file content.
	sha256sum := sha256.Sum256(buf)

	return signature(buf, size), sha256sum, nil
}

// ComputeFromReader reads up to BytesToChecksum bytes from r and returns the composite checksum.
// The size parameter is the total file size, which is folded into the sum component.
func ComputeFromReader(r io.Reader, size int64) (uint64, error) {
	buf, bufPtr, err := readSignatureBuffer(r, size)
	if err != nil {
		return 0, err
	}
	defer signatureBufferPool.Put(bufPtr)

	return signature(buf, size), nil
}
