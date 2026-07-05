package dupe

// Detector finds duplicate files by grouping them by checksum (normal mode)
// or filesystem object identifier (hardlink search mode).
type Detector struct {
	// groups maps a 64-bit signature to files with that signature.
	// The slice holds files in insertion order and serves as a collision chain.
	groups map[uint64][]FileInfo

	// inodeGroups maps inode to files (hardlink search mode only).
	inodeGroups map[uint64][]FileInfo

	// stats is the shared statistics accumulator.
	stats *Stats
}

// NewDetector creates a new Detector with the given stats.
func NewDetector(stats *Stats) *Detector {
	return &Detector{
		groups:      make(map[uint64][]FileInfo),
		inodeGroups: make(map[uint64][]FileInfo),
		stats:       stats,
	}
}

// Insert adds a FileInfo to the detector and returns potential duplicate groups.
// In normal mode (checksum-based), returns one DupeGroup for each existing file
// with the same signature. The caller must verify these with a full byte comparison.
// In hardlink search mode, files with NumLinks <= 1 are silently skipped.
func (d *Detector) Insert(fi FileInfo) []DupeGroup {
	d.stats.TotalFiles.Add(1)
	d.stats.TotalBytes.Add(fi.Size)

	key := fi.Signature
	existing, exists := d.groups[key]

	if !exists {
		// First file with this signature — store and return nil.
		d.groups[key] = []FileInfo{fi}
		return nil
	}

	// Signature collision — create ONE DupeGroup against the first file in the group.
	// This matches the C version behavior: only verify against the first stored file.
	// If verification fails (CRC collision), the file is added to the collision chain
	// and compared against the next peer on the next match.
	group := DupeGroup{
		Signature: key,
		Original:  existing[0],
		Candidate: fi,
	}

	// Always append to the group (collision chain).
	d.groups[key] = append(existing, fi)

	return []DupeGroup{group}
}

// InsertHardlink adds a FileInfo for hardlink search mode.
// Files with NumLinks <= 1 are skipped (they cannot be part of a hardlink group).
// Returns true if the file was stored for hardlink detection.
func (d *Detector) InsertHardlink(fi FileInfo) bool {
	if fi.NumLinks <= 1 {
		return false
	}

	d.stats.TotalFiles.Add(1)
	d.stats.TotalBytes.Add(fi.Size)

	d.inodeGroups[fi.Inode] = append(d.inodeGroups[fi.Inode], fi)
	return true
}

// HardlinkGroups returns all inode groups with more than one file.
// Each group represents a set of hardlinked files.
func (d *Detector) HardlinkGroups() [][]FileInfo {
	var result [][]FileInfo
	for _, files := range d.inodeGroups {
		if len(files) > 1 {
			// Copy the slice to prevent external modification.
			group := make([]FileInfo, len(files))
			copy(group, files)
			result = append(result, group)
		}
	}
	return result
}

// Len returns the number of unique signatures stored.
func (d *Detector) Len() int {
	return len(d.groups)
}

// Stats returns the detector's statistics accumulator.
func (d *Detector) Stats() *Stats {
	return d.stats
}
