//go:build darwin

package log2phys

import "encoding/binary"

// Layout of struct log2phys from <sys/fcntl.h>:
//
//	unsigned int l2p_flags;        /* offset 0  */
//	off_t        l2p_contigbytes;  /* offset 4  */
//	off_t        l2p_devoffset;    /* offset 12 */
//
// The call passes the structure itself (flags included) as the fcntl argument, so
// the buffer is Size bytes even though only the fields after FlagsOffset are
// written.
const (
	// Size is the number of bytes the kernel writes into the buffer.
	Size = 20

	// FlagsOffset is the offset of f_flags.
	FlagsOffset = 0

	// ContigOffset is the offset of f_contigbytes.
	ContigOffset = 4

	// DevOffsetOffset is the offset of f_devoffset.
	DevOffsetOffset = 12
)

// Encode lays out the request for one range: contigBytes is the number of bytes to
// ask about and devOffset is the logical offset to map. The kernel replaces both
// with its answer.
func Encode(contigBytes, devOffset int64) [Size]byte {
	var rec [Size]byte
	binary.LittleEndian.PutUint64(rec[ContigOffset:DevOffsetOffset], uint64(contigBytes))
	binary.LittleEndian.PutUint64(rec[DevOffsetOffset:Size], uint64(devOffset))
	return rec
}

// Parse returns the device offset and the contiguous run length the kernel wrote
// into record.
func Parse(record []byte) (devOffset, contig int64) {
	contig = int64(binary.LittleEndian.Uint64(record[ContigOffset:DevOffsetOffset]))
	devOffset = int64(binary.LittleEndian.Uint64(record[DevOffsetOffset:Size]))
	return devOffset, contig
}

// Flags returns the f_flags field of record.
func Flags(record []byte) uint32 {
	return binary.LittleEndian.Uint32(record[FlagsOffset:ContigOffset])
}
