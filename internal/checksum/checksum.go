// Package checksum computes 64-bit file signatures by reading the first 32KB of a file.
// The algorithm matches the original C finddupe for cross-version signature compatibility.
package checksum

import (
	"crypto/sha256"
	"io"
	"os"
)

// BytesToChecksum is the number of bytes to read from the beginning of a file.
// This matches the C version's BYTES_DO_CHECKSUM_OF constant.
const BytesToChecksum = 32768

// Compute opens the file at path and returns its 64-bit composite checksum.
// The signature is (crc << 32) | sum, where crc and sum are computed from
// the first BytesToChecksum bytes, and fileSize is added to sum.
func Compute(path string, size int64) (uint64, error) {
	sig, _, _, _, err := ComputeFileInfo(path, size)
	return sig, err
}

// ComputeFileInfo opens the file once and returns the checksum signature,
// filesystem inode, hardlink count, and SHA-256 hash. On Windows, this uses
// GetFileInformationByHandle on the already-open handle — avoiding a
// second CreateFile call in the single-threaded walker.
//
// When size <= BytesToChecksum (32KB), the entire file is read for CRC so
// SHA-256 is also computed at zero additional cost. For larger files,
// SHA-256 is returned as zero (not yet computed).
func ComputeFileInfo(path string, size int64) (sig uint64, inode uint64, numLinks uint64, sha256sum [32]byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, [32]byte{}, err
	}
	defer f.Close()

	// Get inode from the open file handle (platform-specific).
	inode, numLinks = fileInode(f)

	if size <= BytesToChecksum {
		// File fits entirely in the CRC buffer — compute SHA-256 alongside CRC
		// at zero additional I/O cost.
		sig, sha256sum, err = computeBoth(f, size)
	} else {
		sig, err = ComputeFromReader(f, size)
	}
	return sig, inode, numLinks, sha256sum, err
}

// computeBoth reads the entire file (up to BytesToChecksum) and computes
// both the weak CRC signature and the SHA-256 hash. The file must be
// <= BytesToChecksum bytes.
func computeBoth(r io.Reader, size int64) (uint64, [32]byte, error) {
	bytesToRead := min(size, BytesToChecksum)
	buf := make([]byte, bytesToRead)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return 0, [32]byte{}, err
	}
	buf = buf[:n]

	// Weak CRC (matching C CalcCrc algorithm).
	var crc uint32
	var sum uint32
	for _, b := range buf {
		crc ^= uint32(b)
		sum += uint32(b)
		crc = (crc >> 8) ^ ((crc & 0xff) << 24) ^ ((crc & 0xff) << 9)
		sum = (sum << 1) + (sum >> 31)
	}
	sum += uint32(size)
	sig := (uint64(crc) << 32) | uint64(sum)

	// SHA-256 of the complete file content.
	sha256sum := sha256.Sum256(buf)

	return sig, sha256sum, nil
}

// ComputeFromReader reads up to BytesToChecksum bytes from r and returns the composite checksum.
// The size parameter is the total file size, which is folded into the sum component.
func ComputeFromReader(r io.Reader, size int64) (uint64, error) {
	// Determine how many bytes to read.
	bytesToRead := min(size, BytesToChecksum)

	// Read the data.
	buf := make([]byte, bytesToRead)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return 0, err
	}
	buf = buf[:n]

	// Compute CRC and sum, matching the C CalcCrc algorithm exactly.
	var crc uint32
	var sum uint32

	for _, b := range buf {
		crc ^= uint32(b)
		sum += uint32(b)

		// These operations must use uint32 to match C's unsigned int overflow behavior.
		crc = (crc >> 8) ^ ((crc & 0xff) << 24) ^ ((crc & 0xff) << 9)
		sum = (sum << 1) + (sum >> 31)
	}

	// Add file size to sum, matching C: CheckSum.Sum += (unsigned int)FileSize.
	sum += uint32(size)

	// Pack into 64-bit result, matching C's Checksum_t memory layout.
	return (uint64(crc) << 32) | uint64(sum), nil
}
