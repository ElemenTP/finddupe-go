package dupe

import (
	"bytes"
	"slices"
	"sort"
	"sync"
)

// Detector owns all duplicate-detection state and turns file insertions and
// executor completions into the next batch of work to perform. The executor is
// stateless: it only runs the Executions the detector hands out.
//
// Two-level grouping strategy:
//  1. Primary key: (weak CRC signature, file size) — composite key eliminates
//     false collisions from file-size wrapping at 4 GB.
//  2. Secondary key: SHA-256 of full file content — zero means "not yet computed".
//
// Hashing is streaming, elimination is not: Insert emits only the hashing work a
// new file needs, and NextFinal decides who is kept once the whole scan is known.
// That split exists because the keeper can only be chosen once every member of a
// content group has been seen — the file whose hash happens to finish first is an
// accident of scheduling, and for files larger than the scan-time checksum window
// it is unrelated to the order the files were given in.
//
// Strategy by group size:
//   - 1 file: store without computing SHA-256 (avoids unnecessary I/O).
//   - 2 files: chunked SHA-256 comparison with early-stop; when it proves the two
//     files differ, neither is hashed further.
//   - 3+ files: each new file gets its own SHA-256 computation; anything an
//     early-stopped comparison left unfinished is completed by NextFinal.
type Detector struct {
	mu sync.Mutex

	// groups maps the composite key to its state.
	groups map[GroupKey]*keyState

	// inodes groups files by physical identity for --listlink.
	inodes map[InodeKey][]FileInfo

	// seenPaths tracks every path already inserted, so overlapping patterns
	// (for example passing the same directory twice) can never make a file a
	// duplicate of itself.
	seenPaths map[string]struct{}

	// coWDetect makes matching identical files emit CoWDetect instead of
	// DupeElim (find --cow).
	coWDetect bool

	// policy orders the members of a content group; the first one is kept.
	policy KeeperPolicy

	// compressionProbe reports whether a file is stored compressed. When set, a
	// compressed member is preferred as the keeper (a CoW clone inherits the
	// source's layout, so the group stays compressed). It is the only I/O the
	// detector performs, and only for the members of a content group; nil
	// disables the preference.
	compressionProbe func(FileInfo) bool

	// chooser, when set, picks the keeper of a content group instead of the
	// policy — the interactive mode. See [KeeperChooser].
	chooser KeeperChooser

	// finalKeys is the materialized, ordered list of groups NextFinal walks;
	// finalIdx is the group it is working on.
	finalKeys []GroupKey
	finalIdx  int

	// failed counts how often finalization scheduled a hash that then could not
	// be completed, so a file that can never be hashed does not stall the run.
	failed map[string]int

	// finalPending marks the files whose hash was scheduled by NextFinal, so only
	// those attempts count against the retry budget.
	finalPending map[string]struct{}

	stats *Stats
}

// keyState is the state of one (weak signature, size) group.
type keyState struct {
	// buckets maps a complete SHA-256 to the files that have it. The map is only
	// allocated once the group holds a second file: most groups hold exactly one
	// (no duplicate at all), and an inner map per unique file dominated the
	// detector's memory use.
	buckets map[[32]byte][]FileInfo

	// pending holds the files whose full SHA-256 is not known yet, keyed by
	// path so a completion updates its entry in O(1) and so the group can be
	// iterated without scanning every bucket.
	pending map[string]FileInfo

	// first is the single file of a group whose maps are not allocated yet.
	first FileInfo

	// count is the number of files in the group.
	count int

	// settled marks a two-file group whose direct comparison proved that the two
	// files differ, so neither has to be hashed.
	settled bool

	// plan is the remaining end-of-scan work of the group: one DupeElim per
	// victim, or one CoWDetect per identical-content bucket. It is built once the
	// group's content is complete and drained in bounded batches; next is the
	// first entry that has not been sent yet.
	plan []Execution
	next int
}

// KeeperPolicy orders the members of one identical-content group. The first
// member of that order is kept and the others become victims.
type KeeperPolicy interface {
	// Less reports whether a is a better keeper than b. It must be a strict weak
	// ordering over the members of a group.
	Less(a, b FileInfo) bool
}

// KeeperChooser picks the keeper of one content group, replacing the policy for
// that group. It is how an interactive mode asks the user: the detector has just
// finished hashing, every member of the group is known, and nothing is in flight,
// so the choice can be made with full information and applied to the whole group.
type KeeperChooser interface {
	// Choose returns the member of members to keep. ok=false leaves the group
	// alone. The returned FileInfo must be one of the members (matched by path);
	// anything else skips the group rather than acting on an unknown file.
	//
	// Choose is called from NextFinal with the detector's lock held and must not
	// call back into the Detector.
	Choose(members []FileInfo) (keeper FileInfo, ok bool)
}

// DefaultKeeperPolicy is the built-in keeper order:
//  1. reference files (--ref) first, so the original a user pointed at is kept;
//  2. then the file with the most hardlinks, because deleting or replacing a file
//     that still has other links frees no storage;
//  3. then the smallest path, which makes the choice stable across runs, machines
//     and filesystems (the walk order is not).
type DefaultKeeperPolicy struct{}

// Less implements KeeperPolicy.
func (DefaultKeeperPolicy) Less(a, b FileInfo) bool {
	if a.IsRef != b.IsRef {
		return a.IsRef
	}
	if a.NumLinks != b.NumLinks {
		return a.NumLinks > b.NumLinks
	}
	return a.Path < b.Path
}

// Option customizes a Detector.
type Option func(*Detector)

// WithCoWDetect makes the detector emit CoWDetect executions for matching
// identical files instead of elimination tasks.
func WithCoWDetect() Option {
	return func(d *Detector) { d.coWDetect = true }
}

// WithKeeperPolicy replaces the keeper order used to pick the file that is kept.
func WithKeeperPolicy(policy KeeperPolicy) Option {
	return func(d *Detector) { d.policy = policy }
}

// WithCompressionPreference prefers a member whose content is stored compressed
// as the keeper, ahead of the configured policy. probe is consulted once per
// member of a content group.
func WithCompressionPreference(probe func(FileInfo) bool) Option {
	return func(d *Detector) { d.compressionProbe = probe }
}

// WithKeeperChooser replaces the keeper policy: the chooser decides the keeper of
// every content group it is asked about, and may decline a group.
func WithKeeperChooser(chooser KeeperChooser) Option {
	return func(d *Detector) { d.chooser = chooser }
}

// zeroSHA is the sentinel key for files whose SHA-256 has not been computed.
var zeroSHA [32]byte

// FilesPerExecution is the number of files one comparison or elimination execution
// carries: a duplicate is decided between two files.
//
// The detector's group minimum is the same pair seen from the group's side, and the
// pipeline's threshold checks the same arity, so all three derive from this one
// value instead of three separate 2s.
const FilesPerExecution = 2

// MinGroupSize is the smallest number of files that can be duplicates: one file
// cannot have a duplicate.
const MinGroupSize = FilesPerExecution

// maxFailedHashRetries is how often finalization re-schedules a file whose hash
// did not complete. After that the file is left alone: a file whose content
// could not be verified must never be eliminated.
const maxFailedHashRetries = 2

// NewDetector creates a new Detector with the given stats.
func NewDetector(stats *Stats, opts ...Option) *Detector {
	d := &Detector{
		groups:       make(map[GroupKey]*keyState),
		inodes:       make(map[InodeKey][]FileInfo),
		seenPaths:    make(map[string]struct{}),
		failed:       make(map[string]int),
		finalPending: make(map[string]struct{}),
		policy:       DefaultKeeperPolicy{},
		stats:        stats,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Insert adds a FileInfo and returns the hashing work it triggers. A path is
// only ever inserted once: overlapping patterns (or a file reached through
// several links) must never make a file a duplicate of itself.
//
// Only the new file is ever scheduled, which keeps Insert O(1) in the group size;
// work left behind by an early-stopped comparison is picked up by NextFinal.
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
	st := d.groups[key]

	if st == nil {
		// First file with this key — no hashing needed yet.
		d.groups[key] = &keyState{first: fi, count: 1}
		return nil
	}

	st.count++
	if st.buckets == nil {
		// Second file: allocate the content maps and re-home the first one.
		st.buckets = make(map[[32]byte][]FileInfo, 1)
		st.addFileLocked(st.first)
		st.first = FileInfo{}
	}
	st.addFileLocked(fi)

	// Small files arrive with their SHA-256 already computed by the scan: they
	// only have to be placed in their bucket and are never hashed again.
	if fi.SHA256 != zeroSHA {
		return nil
	}

	if st.count == MinGroupSize {
		// Exactly two files, both unhashed: compare them directly with
		// early-stop instead of hashing each one completely.
		return []Execution{{Key: key, Type: HashComp, Files: st.pendingPairLocked()}}
	}

	// 3+ files: hash this one; the others already have their own task or will be
	// completed by NextFinal.
	return []Execution{{Key: key, Type: HashCalc, Files: []FileInfo{fi}}}
}

// OnHashDone feeds a completed HashCalc back into the detector. incomplete
// reports that the digest could not be finished (I/O error, or the file changed
// while it was read), in which case whatever partial state came back is kept.
func (d *Detector) OnHashDone(key GroupKey, fi FileInfo, incomplete bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, scheduled := d.finalPending[fi.Path]; scheduled {
		delete(d.finalPending, fi.Path)
		if incomplete || fi.SHA256 == zeroSHA {
			d.failed[fi.Path]++
		}
	}

	st := d.groups[key]
	if st == nil {
		return
	}
	st.applyHashLocked(fi)
}

// OnCompareDone feeds a completed HashComp back into the detector. incomplete
// reports that the comparison could not reach a verdict (I/O error or a file
// that changed); a comparison that returns two still-unhashed files without an
// error proved that they differ.
func (d *Detector) OnCompareDone(key GroupKey, a, b FileInfo, incomplete bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if incomplete {
		d.failed[a.Path]++
		d.failed[b.Path]++
	}

	st := d.groups[key]
	if st == nil {
		return
	}
	st.applyHashLocked(a)
	st.applyHashLocked(b)

	if !incomplete && st.count == MinGroupSize && len(st.pending) == MinGroupSize && len(st.buckets) == 0 {
		st.settled = true
	}
}

// NextFinal returns the next batch of end-of-scan work, at most limit
// executions, and whether the detector has nothing left to produce. Every group
// with at least two files is first completed (files an early-stopped comparison
// left without a SHA-256 are hashed), then decided: the kept file is chosen by
// the keeper policy and every other member becomes a victim.
//
// The caller must have no execution in flight when it calls NextFinal, and must
// call it again until it reports completion.
func (d *Detector) NextFinal(limit int) ([]Execution, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if limit < 1 {
		limit = 1
	}

	if d.finalKeys == nil {
		d.finalKeys = make([]GroupKey, 0, len(d.groups))
		for key, st := range d.groups {
			if st.count >= MinGroupSize {
				d.finalKeys = append(d.finalKeys, key)
			}
		}
		// Groups are decided in a stable order so a run is reproducible.
		sort.Slice(d.finalKeys, func(i, j int) bool {
			return groupKeyLess(d.finalKeys[i], d.finalKeys[j])
		})
	}

	execs := make([]Execution, 0, limit)
	for d.finalIdx < len(d.finalKeys) {
		key := d.finalKeys[d.finalIdx]
		st := d.groups[key]
		if st == nil {
			d.finalIdx++
			continue
		}

		if work := d.completeHashesLocked(key, st, limit-len(execs)); len(work) > 0 {
			// Whatever this work finishes may reveal more duplicates in the same
			// group, so the group is revisited before it is decided.
			return append(execs, work...), false
		}

		decided, done := d.decideLocked(key, st, limit-len(execs))
		execs = append(execs, decided...)
		if done {
			d.finalIdx++
		}

		if len(execs) >= limit {
			return execs, false
		}
	}

	return execs, d.finalIdx >= len(d.finalKeys)
}

// completeHashesLocked schedules a HashCalc for the files of a group whose
// digest is still unknown and has not already failed too often.
func (d *Detector) completeHashesLocked(key GroupKey, st *keyState, limit int) []Execution {
	if st.settled || len(st.pending) == 0 || limit <= 0 {
		return nil
	}

	var execs []Execution
	for path, fi := range st.pending {
		if len(execs) >= limit {
			break
		}
		if d.failed[path] >= maxFailedHashRetries {
			continue
		}
		d.finalPending[path] = struct{}{}
		execs = append(execs, Execution{Key: key, Type: HashCalc, Files: []FileInfo{fi}})
	}
	return execs
}

// orderMembersLocked sorts a content bucket's members into keeper order,
// optionally preferring compressed content. The probe is consulted once per
// member: sorting must not turn it into an I/O storm.
func (d *Detector) orderMembersLocked(members []FileInfo) {
	if d.compressionProbe == nil {
		sort.Slice(members, func(i, j int) bool { return d.policy.Less(members[i], members[j]) })
		return
	}

	compressed := make(map[string]bool, len(members))
	for _, fi := range members {
		compressed[fi.Path] = d.compressionProbe(fi)
	}
	sort.Slice(members, func(i, j int) bool {
		if compressed[members[i].Path] != compressed[members[j].Path] {
			return compressed[members[i].Path]
		}
		return d.policy.Less(members[i], members[j])
	})
}

// chooseKeeperLocked returns the member to keep: the chooser's answer when one is
// configured, the policy's first member otherwise. An answer that is not one of
// the members skips the group — acting on an unknown file would eliminate the
// wrong duplicates.
func (d *Detector) chooseKeeperLocked(members []FileInfo) (FileInfo, bool) {
	if d.chooser == nil {
		return members[0], true
	}

	keeper, ok := d.chooser.Choose(members)
	if !ok {
		return FileInfo{}, false
	}
	for _, member := range members {
		if member.Path == keeper.Path {
			return member, true
		}
	}
	return FileInfo{}, false
}

// keeperFirst returns the members with keeper moved to the front, keeping the
// relative order of the rest.
func keeperFirst(members []FileInfo, keeper FileInfo) []FileInfo {
	out := make([]FileInfo, 0, len(members))
	out = append(out, keeper)
	for _, member := range members {
		if member.Path != keeper.Path {
			out = append(out, member)
		}
	}
	return out
}

// decideLocked emits the end-of-scan work of one group in bounded batches. done
// reports that every task of the group has been emitted.
//
// Only files inside the same content bucket are duplicates: one group can hold
// several unrelated contents that happen to share a weak signature and a size.
// In CoW-detect mode each such bucket becomes one CoWDetect, which the action
// layer needs whole to compute the sharing ratios.
func (d *Detector) decideLocked(key GroupKey, st *keyState, limit int) ([]Execution, bool) {
	if st.plan == nil {
		st.plan = d.buildPlanLocked(st)
		if len(st.plan) == 0 {
			return nil, true
		}
	}

	var execs []Execution
	for st.next < len(st.plan) && len(execs) < limit {
		next := st.plan[st.next]
		next.Key = key
		execs = append(execs, next)
		st.next++
	}

	if st.next >= len(st.plan) {
		st.plan, st.next = nil, 0
		return execs, true
	}
	return execs, false
}

// buildPlanLocked decides a group whose content is complete: for every content
// bucket, the keeper policy picks the file to keep and every other member of that
// bucket becomes a victim.
func (d *Detector) buildPlanLocked(st *keyState) []Execution {
	shas := make([][32]byte, 0, len(st.buckets))
	for sha := range st.buckets {
		shas = append(shas, sha)
	}
	// Content buckets are decided in a stable order so the plan (and the order
	// the eliminations are dispatched in) is reproducible.
	slices.SortFunc(shas, func(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) })

	var plan []Execution
	for _, sha := range shas {
		files := st.buckets[sha]
		if len(files) < MinGroupSize {
			continue
		}

		// Order first, then collapse hardlinked aliases, so the surviving path of
		// an inode is the one the policy prefers.
		members := append([]FileInfo(nil), files...)
		d.orderMembersLocked(members)
		members = dedupeByInode(members)
		if len(members) < MinGroupSize {
			continue
		}

		keeper, keep := d.chooseKeeperLocked(members)
		if !keep {
			continue
		}

		if d.coWDetect {
			// The action layer needs the whole group; the keeper leads it so the
			// report and the clone source agree on which member is the original.
			plan = append(plan, Execution{Type: CoWDetect, Files: keeperFirst(members, keeper)})
			continue
		}

		for _, victim := range members {
			if victim.Path == keeper.Path {
				continue
			}
			plan = append(plan, Execution{Type: DupeElim, Files: []FileInfo{keeper, victim}})
		}
	}
	return plan
}

// addFileLocked places a file in its SHA-256 bucket, or in the pending set when
// its digest is still unknown. The maps must exist.
func (st *keyState) addFileLocked(fi FileInfo) {
	if fi.SHA256 == zeroSHA {
		if st.pending == nil {
			st.pending = make(map[string]FileInfo, 1)
		}
		st.pending[fi.Path] = fi
		return
	}
	st.buckets[fi.SHA256] = append(st.buckets[fi.SHA256], fi)
}

// applyHashLocked records the result of a hash attempt: a completed digest moves
// the file from the pending set into its content bucket, an unfinished one keeps
// the most advanced partial state for a later resume.
func (st *keyState) applyHashLocked(fi FileInfo) {
	prev, ok := st.pending[fi.Path]
	if !ok {
		return // already complete (or never pending)
	}

	if fi.SHA256 == zeroSHA {
		if fi.HashOffset > prev.HashOffset {
			st.pending[fi.Path] = fi
		}
		return
	}

	delete(st.pending, fi.Path)
	st.buckets[fi.SHA256] = append(st.buckets[fi.SHA256], fi)
}

// pendingPairLocked returns the two unhashed files of a two-file group in a
// deterministic order.
func (st *keyState) pendingPairLocked() []FileInfo {
	pair := make([]FileInfo, 0, MinGroupSize)
	for _, fi := range st.pending {
		pair = append(pair, fi)
	}
	if len(pair) == MinGroupSize && pair[0].Path > pair[1].Path {
		pair[0], pair[1] = pair[1], pair[0]
	}
	return pair
}

// groupKeyLess orders groups by their weak signature and size.
func groupKeyLess(a, b GroupKey) bool {
	if a.Signature != b.Signature {
		return a.Signature < b.Signature
	}
	return a.Size < b.Size
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
