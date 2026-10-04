package dupe

import "sync"

// Detector owns all duplicate-detection state and turns file insertions and
// executor completions into the next batch of work to perform. The executor is
// stateless: it only runs the Executions the detector hands out.
//
// Two-level grouping strategy:
//  1. Primary key: (weak CRC signature, file size) — composite key eliminates
//     false collisions from file-size wrapping at 4 GB.
//  2. Secondary key: SHA-256 of full file content — zero means "not yet computed".
//
// Strategy by group size:
//   - 1 file: store without computing SHA-256 (avoids unnecessary I/O).
//   - 2 files: chunked SHA-256 comparison with early-stop; partial hash state
//     saved on mismatch so a later comparison can resume.
//   - 3+ files: mass concurrent SHA-256 computation for all unhashed files,
//     then instant matching against files with a known SHA-256.
//
// Consistency rules:
//   - The first file placed in a SHA-256 bucket is the keeper and is never a
//     victim.
//   - A file is scheduled as a victim at most once; scheduled files are removed
//     from the buckets so they can never be selected as a keeper later.
//   - Reference (--ref) files are still scheduled, but the action layer refuses
//     to eliminate them.
type Detector struct {
	mu sync.Mutex

	// groups maps the composite key to its SHA-256 sub-buckets. The zeroSHA
	// bucket holds files whose full hash is still unknown (possibly partial).
	groups map[GroupKey]map[[32]byte][]FileInfo

	// inodes groups files by physical identity for --listlink.
	inodes map[InodeKey][]FileInfo

	// inflight tracks paths that already have a hash/compare task queued, so
	// the same file is never hashed twice concurrently.
	inflight map[GroupKey]map[string]struct{}

	// scheduled tracks paths that have already been handed to the executor for
	// elimination.
	scheduled map[string]struct{}

	// seenPaths tracks every path already inserted, so overlapping patterns
	// (for example passing the same directory twice) can never make a file a
	// duplicate of itself.
	seenPaths map[string]struct{}

	// coWDetect makes matching identical files emit CoWDetect instead of
	// DupeElim (find --cow).
	coWDetect bool

	stats *Stats
}

// Option customizes a Detector.
type Option func(*Detector)

// WithCoWDetect makes the detector emit CoWDetect executions for matching
// identical files instead of elimination tasks.
func WithCoWDetect() Option {
	return func(d *Detector) { d.coWDetect = true }
}

// zeroSHA is the sentinel key for files whose SHA-256 has not been computed.
var zeroSHA [32]byte

// NewDetector creates a new Detector with the given stats.
func NewDetector(stats *Stats, opts ...Option) *Detector {
	d := &Detector{
		groups:    make(map[GroupKey]map[[32]byte][]FileInfo),
		inodes:    make(map[InodeKey][]FileInfo),
		inflight:  make(map[GroupKey]map[string]struct{}),
		scheduled: make(map[string]struct{}),
		seenPaths: make(map[string]struct{}),
		stats:     stats,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Insert adds a FileInfo and returns the executions it triggers.
// A path is only ever inserted once: overlapping patterns (or a file reached
// through several links) must never make a file a duplicate of itself.
func (d *Detector) Insert(fi FileInfo) []Execution {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.seenPaths[fi.Path]; ok {
		return nil
	}
	d.seenPaths[fi.Path] = struct{}{}

	d.stats.TotalFiles.Add(1)
	d.stats.TotalBytes.Add(fi.Size)

	key := GroupKey{Signature: fi.Signature, Size: fi.Size}
	shaGroups, exists := d.groups[key]

	if !exists {
		// First file with this key — no hashing needed yet.
		d.groups[key] = map[[32]byte][]FileInfo{fi.SHA256: {fi}}
		return nil
	}

	// Instant match: the candidate already has a complete SHA-256 (small files).
	if fi.SHA256 != zeroSHA {
		return d.placeShaLocked(key, shaGroups, fi)
	}

	totalExisting := d.countLocked(shaGroups)
	zeroFiles := shaGroups[zeroSHA]

	// Store the new file in the zero-SHA bucket.
	shaGroups[zeroSHA] = append(zeroFiles, fi)

	if totalExisting == 1 && len(zeroFiles) == 1 {
		// Exactly two files, both unhashed: compare them directly with
		// early-stop instead of hashing each one completely.
		d.markInflightLocked(key, zeroFiles[0].Path, fi.Path)
		return []Execution{{Key: key, Type: HashComp, Files: []FileInfo{zeroFiles[0], fi}}}
	}

	// 3+ files: hash every unhashed file that has no task in flight.
	return d.emitHashCalcLocked(key, shaGroups)
}

// OnHashDone feeds a completed HashCalc back into the detector.
// The execution returns the file with either a complete SHA-256 or, if the read
// was interrupted, an updated resume state.
func (d *Detector) OnHashDone(key GroupKey, fi FileInfo) []Execution {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.clearInflightLocked(key, fi.Path)

	shaGroups := d.groups[key]
	if shaGroups == nil {
		return nil
	}
	return d.applyHashLocked(key, shaGroups, fi)
}

// OnCompareDone feeds a completed HashComp back into the detector. When both
// files are fully hashed and equal it emits DupeElim; when the comparison
// stopped early it persists the partial progress for a later resume.
func (d *Detector) OnCompareDone(key GroupKey, a, b FileInfo) []Execution {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.clearInflightLocked(key, a.Path, b.Path)

	shaGroups := d.groups[key]
	if shaGroups == nil {
		return nil
	}

	execs := d.applyHashLocked(key, shaGroups, a)
	return append(execs, d.applyHashLocked(key, shaGroups, b)...)
}

// InsertInode records a file in the hardlink-group index (used by --listlink).
// Files with fewer than two links cannot form a group and are ignored.
func (d *Detector) InsertInode(fi FileInfo) {
	if fi.Inode == 0 || fi.NumLinks < 2 {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.seenPaths[fi.Path]; ok {
		return
	}
	d.seenPaths[fi.Path] = struct{}{}

	k := InodeKey{Dev: fi.Dev, Inode: fi.Inode}
	d.inodes[k] = append(d.inodes[k], fi)
}

// InodeGroups returns every hardlink group (two or more files sharing a
// physical inode).
func (d *Detector) InodeGroups() [][]FileInfo {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([][]FileInfo, 0, len(d.inodes))
	for _, files := range d.inodes {
		if len(files) > 1 {
			out = append(out, append([]FileInfo(nil), files...))
		}
	}
	return out
}

// placeShaLocked records a file with a complete SHA-256 and emits DupeElim when
// it collides with the keeper of that SHA-256 bucket.
func (d *Detector) placeShaLocked(key GroupKey, shaGroups map[[32]byte][]FileInfo, fi FileInfo) []Execution {
	bucket := shaGroups[fi.SHA256]
	if len(bucket) == 0 {
		shaGroups[fi.SHA256] = []FileInfo{fi}
		return nil
	}

	if _, done := d.scheduled[fi.Path]; done {
		return nil
	}

	keeper := bucket[0]

	// CoW-detect mode never eliminates and emits no per-pair work: every file
	// is kept in the bucket so the final CoWGroups() pass can report each
	// identical-content group with per-file sharing ratios.
	if d.coWDetect {
		shaGroups[fi.SHA256] = append(bucket, fi)
		return nil
	}

	d.scheduled[fi.Path] = struct{}{}
	return []Execution{{Key: key, Type: DupeElim, Files: []FileInfo{keeper, fi}}}
}

// CoWGroups returns the identical-content groups that should share storage:
// for every SHA-256 bucket with at least two distinct physical files, one
// representative per (Dev, Inode). Hardlinked aliases collapse to a single
// entry, so listing hardlink groups is left to --listlink.
func (d *Detector) CoWGroups() [][]FileInfo {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out [][]FileInfo
	for _, shaGroups := range d.groups {
		for sha, files := range shaGroups {
			if sha == zeroSHA || len(files) < minGroupSize {
				continue
			}
			group := dedupeByInode(files)
			if len(group) >= minGroupSize {
				out = append(out, group)
			}
		}
	}
	return out
}

// minGroupSize is the smallest number of files that can share storage.
const minGroupSize = 2

// dedupeByInode keeps the first path of each physical file.
func dedupeByInode(files []FileInfo) []FileInfo {
	seen := make(map[InodeKey]struct{}, len(files))
	out := make([]FileInfo, 0, len(files))

	for _, fi := range files {
		if fi.Inode != 0 {
			k := InodeKey{Dev: fi.Dev, Inode: fi.Inode}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
		}
		out = append(out, fi)
	}
	return out
}

// applyHashLocked moves a file between the zero-SHA bucket and a concrete
// SHA-256 bucket, keeping the most advanced resume state.
func (d *Detector) applyHashLocked(key GroupKey, shaGroups map[[32]byte][]FileInfo, fi FileInfo) []Execution {
	zeroFiles := shaGroups[zeroSHA]

	idx := -1
	for i, f := range zeroFiles {
		if f.Path == fi.Path {
			idx = i
			break
		}
	}

	if fi.SHA256 == zeroSHA {
		// Still incomplete: keep the most advanced partial progress.
		if idx >= 0 {
			if fi.HashOffset > zeroFiles[idx].HashOffset {
				zeroFiles[idx].HashState = fi.HashState
				zeroFiles[idx].HashOffset = fi.HashOffset
			}
		} else {
			shaGroups[zeroSHA] = append(zeroFiles, fi)
		}
		return nil
	}

	// Complete: detach from the zero bucket and place in the SHA bucket.
	if idx >= 0 {
		shaGroups[zeroSHA] = append(zeroFiles[:idx], zeroFiles[idx+1:]...)
	}
	return d.placeShaLocked(key, shaGroups, fi)
}

// emitHashCalcLocked queues a HashCalc for every unhashed file in the key that
// does not already have a task in flight.
func (d *Detector) emitHashCalcLocked(key GroupKey, shaGroups map[[32]byte][]FileInfo) []Execution {
	zeroFiles := shaGroups[zeroSHA]

	var execs []Execution
	for _, f := range zeroFiles {
		if d.isInflightLocked(key, f.Path) {
			continue
		}
		d.setInflightLocked(key, f.Path)
		execs = append(execs, Execution{Key: key, Type: HashCalc, Files: []FileInfo{f}})
	}
	return execs
}

// markInflightLocked flags the given paths as having a task in flight.
func (d *Detector) markInflightLocked(key GroupKey, paths ...string) {
	for _, p := range paths {
		d.setInflightLocked(key, p)
	}
}

// setInflightLocked flags a single path as having a task in flight.
func (d *Detector) setInflightLocked(key GroupKey, path string) {
	m := d.inflight[key]
	if m == nil {
		m = make(map[string]struct{})
		d.inflight[key] = m
	}
	m[path] = struct{}{}
}

// clearInflightLocked removes the in-flight flags for the given paths.
func (d *Detector) clearInflightLocked(key GroupKey, paths ...string) {
	m := d.inflight[key]
	if m == nil {
		return
	}
	for _, p := range paths {
		delete(m, p)
	}
	if len(m) == 0 {
		delete(d.inflight, key)
	}
}

// isInflightLocked reports whether a path already has a task in flight.
func (d *Detector) isInflightLocked(key GroupKey, path string) bool {
	_, ok := d.inflight[key][path]
	return ok
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
