# Algorithms

## 1. Checksum Computation

### Purpose

Compute a fast 64-bit file signature from the first 32KB of a file to quickly identify potential duplicates without reading the entire file.

### Algorithm (Matching the C Original)

```
Given: first N bytes of file (N = min(fileSize, 32768))

Initialize:
  crc = 0   (32-bit CRC-like register)
  sum = 0   (32-bit running sum)
  size = file_size

For each byte b in the first N bytes:
  crc = crc ^ b
  sum = sum + b
  crc = (crc >> 8) ^ ((crc & 0xff) << 24) ^ ((crc & 0xff) << 9)
  sum = (sum << 1) + (sum >> 31)

After loop:
  sum = (sum + size) mod 2^32    // Fold file size into sum

Result:
  signature = (uint64(crc) << 32) | uint64(sum)
```

### Implementation: `ComputeFileInfo`

`checksum.ComputeFileInfo(path, size)` opens the file **once** and returns a `checksum.Info`:

```go
type Info struct {
    Signature uint64   // 64-bit composite checksum of the first 32KB
    Dev       uint64   // Filesystem/volume identifier
    Inode     uint64   // Filesystem object identifier
    NumLinks  uint64   // Hardlink count (0 if unavailable)
    SHA256    [32]byte // Full-content hash when computed at no extra cost
}
```

The physical identity comes from the platform helper `statFile(f)`:
- On **Unix**: `Dev`, `Inode`, and `NumLinks` are read from `f.Stat().Sys().(*syscall.Stat_t)`.
- On **Windows**: they are read from `GetFileInformationByHandle` on the open file handle (volume serial number, `FileIndexHigh<<32 | FileIndexLow`, `NumberOfLinks`).

When `size <= BytesToChecksum (32768)`, the whole file is already read for the CRC, so `computeBoth` also returns its SHA-256 at zero additional I/O cost. For larger files `SHA256` is left as the zero value ("not yet computed").

`Compute(path, size)` delegates to `ComputeFileInfo` and returns only the signature.

### Properties

- **64-bit**: packed as `crc << 32 | sum`, matching the C `Checksum_t` struct in memory
- **Non-cryptographic**: designed for speed, not security
- **CRC-adjacent**: the XOR-then-rotate step is similar to CRC but with an extra << 9 term (non-standard)
- **File size folded in**: ensures files with same first 32KB but different sizes have different signatures
- **Uniqueness**: empirically, collision probability on real file collections is very low (< 0.001%)

### Implementation Note

This is not a standard CRC. The polynomial `(x >> 8) ^ ((x & 0xff) << 24) ^ ((x & 0xff) << 9)` is specific to finddupe. The Go implementation must match this exactly for signature compatibility with the C version.

## 2. Duplicate Detection

### Overview

Duplicate detection is a **state machine** in `internal/dupe/detector.go`. The detector performs no I/O: callers feed it files and executor outcomes, and it returns `[]dupe.Execution` work items.

```
detector.Insert(fi)                      → []Execution        // a newly scanned file
detector.OnHashDone(key, fi, incomplete)                      // a HashCalc finished
detector.OnCompareDone(key, a, b, incomplete)                 // a HashComp finished
detector.NextFinal(limit)                → ([]Execution, bool) // end-of-scan work
```

Hashing is streaming, elimination is not. `Insert` only schedules the hashing work
the new file needs; `NextFinal` decides who is kept once the whole scan is known,
because the keeper can only be chosen after every member of a content group has been
seen. The file whose hash happens to finish first is an accident of scheduling, and
for files larger than the scan-time checksum window it is unrelated to the order the
files were given in.

Two-level grouping:
1. **Primary key**: `GroupKey{Signature, Size}` — the weak CRC plus the file size.
2. **Secondary key**: the full SHA-256 of the file content (`[32]byte`), with the all-zero value meaning "not yet computed".

A `GroupKey` therefore maps to a `keyState`: one bucket per known SHA-256, plus a
pending set for files whose full hash is still unknown. A *bucket* is the unit of
content identity — only files inside the same bucket are duplicates, and one group can
hold several unrelated contents that happen to share a weak signature and a size.

### Strategy by Group Size

| Files sharing the key | Action |
|-----------------------|--------|
| 1 file | Store it. No hashing — unique files cost nothing beyond the weak checksum, and the per-bucket maps are not even allocated. |
| Exactly 2 unhashed files | Emit one `HashComp` (chunked comparison with early-stop). A verdict of "the two differ" settles the pair: neither file is hashed further. |
| 3 or more files | Emit `HashCalc` for the file just inserted — O(1) per insert, whatever the group size. Files an early-stopped comparison left behind are completed by `NextFinal`. |

### Pseudocode

```go
func (d *Detector) Insert(fi FileInfo) []Execution {
    key := GroupKey{Signature: fi.Signature, Size: fi.Size}
    st, exists := d.groups[key]
    if !exists {
        d.groups[key] = &keyState{first: fi, count: 1}   // no inner map yet
        return nil
    }

    st.count++
    st.addFile(fi)                     // into its SHA-256 bucket, or the pending set

    if fi.SHA256 != zeroSHA {          // small files arrive hashed by the scan
        return nil
    }
    if st.count == 2 {                 // exactly two unhashed files: compare them
        return []Execution{{Key: key, Type: HashComp, Files: st.pendingPair()}}
    }
    return []Execution{{Key: key, Type: HashCalc, Files: []FileInfo{fi}}}
}
```

### Consistency Rules

- **Keeper (deterministic)**: elimination is deferred to the end of the scan.
  `NextFinal` completes every group, then decides each content bucket: its members are
  sorted by the keeper policy and the first one is kept, while the others become
  victims (one `DupeElim` each). The default policy is
  `DefaultKeeperPolicy` — reference files first, then the file with more hardlinks
  (deleting or replacing a file that still has other links frees no storage), then the
  smallest path. With `--prefer-compressed` a compressed member is preferred ahead of
  the hardlink and path rules (the probe runs once per member of a content group).
  The choice no longer depends on which hash finished first, so a run is
  reproducible; `--ref` remains the way to force a specific original.
- **No double elimination**: each bucket's plan is built once (one task per victim) and
  drained in bounded batches. Hardlinked aliases are collapsed *after* the policy
  ordering, so the surviving path of an inode is the preferred one and a path can
  never be both keeper and victim.
- **No concurrent double hashing**: only the file just inserted is scheduled while
  streaming, and `NextFinal` is called only when nothing is in flight.
- **Partial resume**: when a comparison stops early (or a hash attempt is interrupted),
  the more advanced `HashState`/`HashOffset` is persisted, so the next attempt resumes
  instead of re-reading from the start.
- **Complete hash**: `OnHashDone`/`OnCompareDone` move a file from the pending set into
  its concrete SHA-256 bucket. A comparison that returns two still-unhashed files
  *without* an error proved that they differ, and a two-file group needs no further
  hashing.
- **Unverifiable files**: a hash that cannot be completed (I/O error, or a file that
  keeps changing) is retried at most `maxFailedHashRetries` times by `NextFinal` and
  then left alone — a file whose content was never verified is never eliminated.
- **CoW-detect mode**: with `WithCoWDetect()` (`find --cow`) the same `NextFinal` pass
  turns each content bucket with at least two distinct physical files into one
  `CoWDetect` execution carrying all its members, instead of emitting eliminations.

### Comparison to the C Original

The C version uses a **binary search tree** with root at index 1, `Larger`/`Smaller` for navigation, and `Same` for collision chains. The Go version's two-level hash map is semantically equivalent:

| C concept | Go equivalent |
|-----------|---------------|
| `FileData[]` array | `groups` map |
| BST navigation (`Larger`/`Smaller`) | Hash map lookup (O(1)) |
| `Same` chain for collisions | `map[[32]byte][]FileInfo` buckets |
| `CheckDuplicate()` function | `Detector.Insert()` + outcome callbacks + `NextFinal()` |

### Same-Physical-File Refusal (Every Action)

Duplicate detection always operates on content, but before taking any action the executor checks physical identity. Inode numbers are only unique per device, so **both** `Dev` and `Inode` are compared (`samePhysicalFile`, with `Inode != 0` guarding against platforms that cannot report an identity):

```
Before acting on a DupeElim (keeper = Files[0], victim = Files[1]):
  if samePhysicalFile(keeper, victim):   # Inode != 0, same Dev, same Inode
      if report mode and not --hardlink:
          → ResultVerifiedDuplicate      # still reported as a duplicate
      else:
          → ResultAlreadyHardlinked      # no file is touched
  else if victim.IsRef and action != report:
      → ResultSkippedRef                 # reference files are never eliminated
  else if action != report and (keeper or victim changed since it was hashed):
      → ResultSkippedChanged             # the duplicate decision is stale
  else if action == hardlink and not sameDevice(keeper, victim):
      → ResultSkippedCrossDevice         # hard links cannot span volumes
  else:
      → execute the configured action
```

"Changed since it was hashed" means the current `Size`/`ModTime` no longer match
the values recorded with the content hash, so the pair is left alone rather than
eliminated against a stale decision.

This applies to **every** action: delete, hardlink, and CoW clone all refuse to
touch a path that is already the same physical file as the keeper. Acting on such
a pair would break the existing hardlink — in particular, cloning one name of a
hardlinked pair would replace it with an unshared copy.

This means:
- Files with **same content + same `(Dev, Inode)`** (already hardlinked) →
  `ResultAlreadyHardlinked` (silently skipped; in verbose mode logged as
  `already hardlinked`), except in report mode without `--hardlink`, where the
  pair is still reported as a duplicate
- Files with **same content + different `(Dev, Inode)`** → reported as duplicates
- Files with **different content** → not duplicates (weak-checksum collision, kept in separate SHA-256 buckets)

## 3. Chunked Comparison (`HashComp`)

### Purpose

When exactly two unhashed files share a weak-checksum bucket, compare them **incrementally with early-stop** instead of hashing both completely. Since most weak-checksum collisions are not real duplicates, stopping at the first differing chunk saves most of the I/O.

### Algorithm

```
Given: fileA, fileB, chunk size derived from the file size

1. Open both files, restoring any saved SHA-256 state and seeking to HashOffset
2. Verify each file still has the size recorded during the scan (and, when a
   partial digest is being resumed, the same modification time); a file that
   changed fails with dupe.ErrFileChanged instead of producing a digest of the
   wrong bytes
3. remaining = size - HashOffset
4. While remaining > 0:
   a. Read up to chunkSize bytes from each file
   b. If either read is short/zero → truncated: stop, keep partial state
      (the bytes that were read are fed into the digests first, so HashOffset
      never runs ahead of the hasher)
   c. Feed both chunks to their SHA-256 hashers
   d. If the accumulated hashes differ → stop early, keep partial state
   e. remaining -= bytes read
5. End of file with equal hashes → both SHA-256 values are complete
```

The executor returns the updated `FileInfo` records in the `Outcome`; the detector then treats a complete hash like an `OnHashDone` result and continues matching.

The signature path enforces the same rule: `checksum.ComputeFileInfo` stats the
open file and refuses to sign it when its size is no longer the size the walker
reported.

### Chunk Sizing

| File size | Chunk size |
|-----------|------------|
| ≤ 64 KB | one read (the whole file) |
| ≤ 16 MB | 64 KB — makes early-stop cheap |
| > 16 MB | 1 MB — fewer syscalls |

### Performance Considerations

- Early exit on the first differing chunk (don't read the rest)
- Fixed memory: two chunk buffers regardless of file size
- Partial hash state is saved so a later `HashComp`/`HashCalc` can resume from `HashOffset` instead of re-reading from the start
- `Insert` is O(1) in the group size, and end-of-scan work is emitted in bounded batches (`NextFinal(limit)`), so a scan with millions of duplicates neither stalls the coordinator nor materializes every task at once

## 4. Action Execution

### Verification Flow (`DoExecution`)

The executor is stateless. It receives one `dupe.Execution` and returns an `Outcome`:

```go
func (e *Executor) DoExecution(ctx context.Context, ex dupe.Execution) (Outcome, error)
```

```
switch ex.Type:
  HashCalc  → hash Files[0] fully (resuming from HashState/HashOffset)
  HashComp  → compare Files[0] and Files[1] in chunks (Section 3)
  DupeElim  → act on the victim (Files[1]); Files[0] is the keeper
  CoWDetect → query the whole group's extents and report per-file shared bytes
```

For a `DupeElim` execution:

```
1. If keeper and victim are the same physical file (`(Dev, Inode)`, Inode != 0)
      → report mode without --hardlink: ResultVerifiedDuplicate (still reported)
      → otherwise: ResultAlreadyHardlinked (no file is touched)
2. If the victim is a reference file and the action is not "report"
      → ResultSkippedRef (reference files are never eliminated)
3. If the action is not "report" and either file no longer matches the size and
   modification time recorded when its content was hashed
      → ResultSkippedChanged (the duplicate decision is stale; nothing is touched)
4. Hardlink only: if keeper and victim are on different devices
      → ResultSkippedCrossDevice (a hardlink can never span volumes)
5. Otherwise execute the configured action
```

Step 3 is the re-check that makes elimination safe on a live filesystem: the
hashes are computed earlier in the scan, so a file that was rewritten, appended
to, or replaced in the meantime must never be eliminated against that stale
decision. The readers enforce the same rule while hashing (Section 3).

The same-physical-file check applies to delete, hardlink, and CoW clone alike; it
prevents an existing hardlink from being broken.

### Delete

```
1. Check the victim's permissions
2. If read-only and not --rdonly → ResultSkippedRO
3. If read-only and --rdonly → chmod to add write permission
4. os.Remove(victimPath)
5. → ResultDeleted
```

### Hardlink

```
1. Re-read the keeper's link count and refuse at the NTFS limit (1023)
   → else ResultHardlinkLimit
2. If the victim is read-only and not --rdonly → ResultSkippedRO
3. os.Link(keeperPath, temporaryNameNextToVictim)
4. os.Rename / MoveFileEx(temporaryName, victimPath) — atomic replace
5. → ResultHardlinked
```

The link count is re-read instead of trusting `FileInfo.NumLinks`, which comes
from the scan and is stale after the first link is created; on Unix the
filesystem's own (far higher or absent) limit is left to the kernel.

The link is created under a temporary name next to the victim and renamed over
it, so the victim is either the untouched original file or the finished
hardlink — never missing. A plain remove-then-link destroys the victim whenever
linking fails: different devices, a filesystem without hard links, the link
limit, or a keeper that vanished. Hard links share the keeper's inode, so the
victim necessarily takes on the keeper's permissions and timestamps; the
victim's own metadata is *not* restored, because restoring it would silently
rewrite the keeper's metadata as well.

### CoW Clone

CoW elimination is implemented per platform. The victim is replaced with a block clone of the keeper:

- **Linux** (btrfs/XFS): `FICLONE` ioctl via `golang.org/x/sys/unix` `IoctlFileClone`.
- **macOS** (APFS): `clonefile(2)` via `unix.Clonefile`.
- **Windows** (ReFS/Dev Drive): `FSCTL_DUPLICATE_EXTENTS_TO_FILE` for the
  cluster-aligned part, with the trailing partial cluster copied explicitly to
  its own offset in the destination and the destination preallocated.

All platforms go through `cloneReplace(src, dst)` in `internal/action/cow.go`:

```
0. If keeper and victim are the same physical file, or they are on the same
   device and their extent layouts compare Equal,
   → ResultAlreadyShared (no clone, no file change)
1. Create a temporary file next to the victim
2. Clone the keeper into the temporary path (platform-specific)
3. Complete the clone's data layout from the keeper: the clone holds the keeper's
   bytes, so attributes that describe where those bytes live are the keeper's.
   macOS needs this explicitly — a decmpfs-compressed file keeps its payload in
   com.apple.ResourceFork, its header in com.apple.decmpfs and its state in the
   UF_COMPRESSED flag, and a clone that loses them reads back as zero bytes
4. Restore the victim's metadata on the temporary file: ownership, mode
   (including setuid/setgid/sticky), extended attributes (which carry POSIX
   ACLs on Linux and resource forks on macOS), timestamps, and (macOS) BSD
   file flags. Storage attributes and storage flags are exempt from that sync in
   both directions, because they belong to the cloned data. Unlike a hardlink, a
   clone has its own inode, so the victim's identity can be preserved instead of
   inheriting the keeper's
5. Check that the clone reports the keeper's size. A clone that does not is never
   put in place: the temporary file is removed, the victim is untouched and the
   action is reported as failed
6. Atomically replace the victim (os.Rename on Unix,
   MoveFileEx(REPLACE_EXISTING) on Windows)
```

The extent check is only a "skip the work" fast path: content equality was already
established by the detector, so cloning anyway is safe and idempotent. It is
deliberately conservative — `extent.Equal` returns false for empty lists, for
encoded (compressed) extents, and for unknown (zero) physical addresses, and any
uncertainty (unsupported filesystem, query failure) simply means "clone".
Physical offsets are only meaningful within one device, so two files with
different `Dev` values are never treated as sharing storage. Extent lengths are
clamped to the file's size: filesystems allocate whole blocks, and without the
clamp a fully shared 100000-byte file would report 102400 shared bytes (a ratio
above 100%).

Unsupported filesystems return `ErrCoWNotSupported` and the victim is left
untouched. → `ResultCoWCloned` on success, `ResultAlreadyShared` when the pair
already shares all storage.

### CoW Detection (`find --cow`)

Detection is opt-in because it costs an extra open + extent query per file. It is
a single final pass: after the input is fully drained and every hash/comparison
has finished, the detector state is complete and the pipeline asks for groups.

1. `dupe.Detector.NextFinal()` walks every content bucket with at least two
   **distinct physical files**, keeping one path per `(Dev, Inode)`. Hardlinked
   aliases collapse to a single member; use `find --listlink` to list hardlink
   groups.
2. The detector turns each bucket into one `CoWDetect` execution instead of one
   execution per pair, drained in bounded batches.
3. `detectCoW` computes, for every member, how many of its bytes are already
   shared with another group member:
   - if any extent in the group carries the filesystem's `Shared` flag
     (Linux `FIEMAP_EXTENT_SHARED`), `extent.SharedFlagBytes` is used. This is a
     per-file signal: it says the extent is shared with someone, not with whom;
   - otherwise the physical start address is used as identity within the same
     device via `extent.SharedWithGroup`, capped to the shorter extent.
   `Outcome.FileShared []int64` carries one value per `Outcome.Files` entry; it is
   nil when extent information is unavailable for the whole group.

`find --cow` therefore reports a group, not a pair:

```
CoW candidate group (2 files, identical content):
    '/data/b.bin'  shared: 100.0% (128 kB of 128 kB)
    '/data/a.bin'  shared:   0.0% (0 B of 128 kB)

Files:     256 kB in      2 files
Dupes:     128 kB in      1 files
  1 CoW groups found (128 kB of file bytes already shared)
```

Independent copies are listed too, with 0% shared — they are exactly the members
that should end up CoW-sharing. `CoWSharedBytes` is a per-file sum: each shared
range is counted once per member, so it must not be read as physical bytes saved.
Same-inode (hardlinked) aliases are not separate members.

### What the `Dupes:` line counts

`Dupes:` follows the *decision*, not the action: a pair whose content was verified
identical counts as a duplicate even when the elimination was skipped (read-only,
`--ref`, cross-device hardlink, NTFS link limit) or failed, because the duplicate
storage is still there. Three outcomes are deliberately excluded: a pair that
turned out to be the same inode (already hardlinked) or to already share all its
extents (already shared) is not duplicate storage, and a pair whose decision was
withdrawn because a file changed during the scan is not a duplicate any more. The
action-specific counters (`N files deleted`, `N reference files skipped`, …) are
reported separately, so both questions stay answerable.

On filesystems without extent reporting `FileShared` stays nil: the group members
are still listed, with a note that extent information is unavailable. The
group-wide `extent.SharedWithGroup` (physical overlap, with a physical-start match
for compressed btrfs) remains available as a library helper. See
[cross-platform.md](cross-platform.md) for the per-platform ioctls.

## 5. Glob Pattern Matching (`**`)

### Purpose

Match file paths against patterns with `**` (recursive wildcard) support, matching the semantics of the original C `myglob.c`.

### Algorithm

Implemented as a recursive component-matcher in `fswalker.matchComponents`:

```go
// matchComponents recursively matches pattern components against path components.
// ** in the pattern matches zero or more path components.
func matchComponents(patParts, nameParts []string) bool {
    if len(patParts) == 0 {
        return len(nameParts) == 0
    }
    if patParts[0] == "**" {
        // Try matching ** against 0, 1, 2, ... name parts
        for i := 0; i <= len(nameParts); i++ {
            if matchComponents(patParts[1:], nameParts[i:]) {
                return true
            }
        }
        return false
    }
    if len(nameParts) == 0 {
        return false
    }
    matched, _ := filepath.Match(patParts[0], nameParts[0])
    if !matched {
        return false
    }
    return matchComponents(patParts[1:], nameParts[1:])
}
```

### Pattern Examples

| Pattern | Matches | Does Not Match |
|---------|---------|----------------|
| `**` | Everything | Nothing |
| `**/*.jpg` | `a.jpg`, `dir/a.jpg`, `a/b/c/d.jpg` | `a.png` |
| `/data/**/*.txt` | `/data/a.txt`, `/data/dir/b.txt` | `/other/a.txt` |
| `*.go` | `main.go` (in current dir) | `cmd/main.go` |

### Integration with filepath.WalkDir

The walker calls `filepath.WalkDir` to enumerate files, then filters each path against the user's patterns. For non-recursive patterns (no `**`), subdirectories are skipped via `filepath.SkipDir`. On Unix the walker also fills `Dev`/`Inode`/`NumLinks` from the already-available stat struct via `fileid.FromFileInfo`; where `os.FileInfo` exposes no identity it reports "unknown", and the scanner fills it from the open handle later.

## 6. Multi-Threading Design

### Parallelization Strategy

The checksum computation (`os.Open` + read 32KB + CRC + identity retrieval) is the primary CPU/I/O bottleneck and runs in a **bounded worker pool**. Executor work (full hashing, comparison, elimination) runs in a second pool of the same size.

```
Walker (1 goroutine):          filepath.WalkDir → stat → walkResultCh
Scanner feeder (1 goroutine):  read walkResultCh → pool.Submit (may block)
Worker pool (N goroutines):    os.Open → CRC + Dev/Inode/NumLinks → fileInfoCh
Coordinator (1 goroutine):     owns Detector; Insert/OnHashDone/OnCompareDone
                               → dispatch Executions
Executor pool (N goroutines):  DoExecution → Outcome → coordinator
Progress (1 goroutine):        ticker → stderr
```

The worker pool uses a semaphore channel (`chan struct{}`) with buffer size `threads`. `pool.Submit` blocks when all workers are busy, providing natural back-pressure.

### File Open Optimization

Files are opened **once** per scan in the parallel worker-pool goroutines (via `checksum.ComputeFileInfo`). On Windows, `GetFileInformationByHandle` is called on the same handle — avoiding a second `CreateFile` call that would serialize I/O in the single-threaded walker.

## 7. Progress Reporting

The progress reporter runs on a ticker (500ms) and reads `Stats` atomics:

```
loop:
  sleep 500ms
  read stats atomics (lock-free)
  print "\033[2K\rScanned N files..."
  if context cancelled: exit
```

The `\033[2K\r` ANSI escape clears the line and returns to column 1, enabling in-place updates.
