package dupe

import "sync"

// Detector finds duplicate files using a two-level grouping strategy:
//  1. Primary key: (weak CRC signature, file size) — composite key eliminates
//     false collisions from file-size wrapping at 4 GB.
//  2. Secondary key: SHA-256 of full file content — zero means "not yet computed".
//
// Strategy by group size:
//   - 1 file: store without computing SHA-256 (avoids unnecessary I/O).
//   - 2 files: chunked SHA-256 comparison with early-stop; partial hash state
//     saved on mismatch.
//   - 3+ files: mass concurrent SHA-256 computation for all unhashed files;
//     instant matching against files with known SHA-256.
type Detector struct {
	mu     sync.Mutex
	groups map[GroupKey]map[[32]byte][]FileInfo
	stats  *Stats
}

// zeroSHA is the sentinel key for files whose SHA-256 has not been computed.
var zeroSHA [32]byte

// NewDetector creates a new Detector with the given stats.
func NewDetector(stats *Stats) *Detector {
	return &Detector{
		groups: make(map[GroupKey]map[[32]byte][]FileInfo),
		stats:  stats,
	}
}

// Insert adds a FileInfo and returns potential duplicate groups.
func (d *Detector) Insert(fi FileInfo) []Execution {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.stats.TotalFiles.Add(1)
	d.stats.TotalBytes.Add(fi.Size)

	key := GroupKey{Signature: fi.Signature, Size: fi.Size}
	shaGroups, exists := d.groups[key]

	if !exists {
		// Strategy 2: first file — no SHA-256 computation.
		d.groups[key] = map[[32]byte][]FileInfo{fi.SHA256: {fi}}
		return nil
	}

	// Instant match: candidate has a known SHA-256 that matches an existing bucket.
	if fi.SHA256 != zeroSHA {
		if files, ok := shaGroups[fi.SHA256]; ok && len(files) > 0 {
			shaGroups[fi.SHA256] = append(files, fi)
			return []Execution{{Key: key, Original: files[0], Candidate: fi}}
		}
		// SHA-256 differs from all known buckets → CRC collision, store separately.
		shaGroups[fi.SHA256] = append(shaGroups[fi.SHA256], fi)
		return nil
	}

	// Candidate has no SHA-256. Count existing files.
	totalExisting := d.countLocked(shaGroups)
	zeroFiles := shaGroups[zeroSHA]

	// Store the new file in the zero-SHA bucket.
	shaGroups[zeroSHA] = append(zeroFiles, fi)

	if totalExisting == 1 {
		// Strategy 3: exactly 2 files → chunked SHA-256 comparison with early-stop.
		return []Execution{{Key: key, Original: zeroFiles[0], Candidate: fi}}
	}

	// Strategy 4: 3+ files.
	// Emit pre-verified matches against known-SHA sub-groups, plus one
	// chunked-comparison group against the first zero-SHA file.
	var groups []Execution
	for sha, files := range shaGroups {
		if sha == zeroSHA || len(files) == 0 {
			continue
		}
		groups = append(groups, Execution{Key: key, Original: files[0], Candidate: fi})
	}
	// Also emit one group for chunked comparison against the first zero-SHA file
	// (the original file that started this group).
	if len(zeroFiles) > 0 {
		groups = append(groups, Execution{Key: key, Original: zeroFiles[0], Candidate: fi})
	}

	return groups
}

// UnhashedFiles returns files in the zero-SHA bucket for the given key.
// The caller should submit these for full SHA-256 computation when the group
// has 3+ files (mass concurrent computation).
func (d *Detector) UnhashedFiles(key GroupKey) []FileInfo {
	d.mu.Lock()
	defer d.mu.Unlock()

	shaGroups := d.groups[key]
	if shaGroups == nil {
		return nil
	}
	files := shaGroups[zeroSHA]
	if len(files) == 0 {
		return nil
	}
	// Return a copy to avoid races with concurrent updates.
	out := make([]FileInfo, len(files))
	copy(out, files)
	return out
}

// GroupSize returns the total number of files for a given key.
func (d *Detector) GroupSize(key GroupKey) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.countLocked(d.groups[key])
}

// UpdateFileState updates a file's hash progress. Keeps the state with the
// largest HashOffset. Moves the file from the zero-SHA bucket to the correct
// SHA bucket when SHA-256 is complete.
func (d *Detector) UpdateFileState(key GroupKey, path string, hashState []byte, hashOffset int64, sha256 [32]byte) {
	d.mu.Lock()
	defer d.mu.Unlock()

	shaGroups := d.groups[key]
	if shaGroups == nil {
		return
	}

	// Find and update the file in the zero-SHA bucket.
	zeroFiles := shaGroups[zeroSHA]
	for i, f := range zeroFiles {
		if f.Path != path {
			continue
		}
		// Keep the more advanced state (larger HashOffset wins).
		if hashOffset > f.HashOffset {
			zeroFiles[i].HashState = hashState
			zeroFiles[i].HashOffset = hashOffset
		}

		// If SHA-256 is now complete, move to the correct bucket.
		if sha256 != zeroSHA {
			f = zeroFiles[i]
			f.SHA256 = sha256
			// Move from zero bucket to SHA bucket.
			shaGroups[sha256] = append(shaGroups[sha256], f)
			shaGroups[zeroSHA] = append(zeroFiles[:i], zeroFiles[i+1:]...)
		}
		return
	}
}

// countLocked returns the total number of files across all SHA buckets.
// Must be called with d.mu held.
func (d *Detector) countLocked(shaGroups map[[32]byte][]FileInfo) int {
	if shaGroups == nil {
		return 0
	}
	n := 0
	for _, files := range shaGroups {
		n += len(files)
	}
	return n
}

// Len returns the number of unique GroupKeys stored.
func (d *Detector) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.groups)
}

// Stats returns the detector's statistics accumulator.
func (d *Detector) Stats() *Stats {
	return d.stats
}
