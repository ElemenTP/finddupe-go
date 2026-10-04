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

The physical identity comes from the platform helper `fileIdentity(f)`:
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
detector.Insert(fi)          → []Execution   // a newly scanned file
detector.OnHashDone(key, fi) → []Execution   // a HashCalc finished
detector.OnCompareDone(key, a, b) []Execution// a HashComp finished
```

Two-level grouping:
1. **Primary key**: `GroupKey{Signature, Size}` — the weak CRC plus the file size.
2. **Secondary key**: the full SHA-256 of the file content (`[32]byte`), with the all-zero value meaning "not yet computed".

A `GroupKey` therefore maps to `map[[32]byte][]FileInfo`: one bucket for each known SHA-256, plus the zero-SHA bucket for files whose full hash is still unknown.

### Strategy by Group Size

| Files sharing the key | Action |
|-----------------------|--------|
| 1 file | Store it. No hashing — unique files cost nothing beyond the weak checksum. |
| Exactly 2 unhashed files | Emit one `HashComp` (chunked comparison with early-stop). |
| 3 or more files | Emit `HashCalc` for **every** unhashed file that has no task in flight, then match completed hashes. |

### Pseudocode

```go
func (d *Detector) Insert(fi FileInfo) []Execution {
    key := GroupKey{Signature: fi.Signature, Size: fi.Size}
    shaGroups, exists := d.groups[key]
    if !exists {
        d.groups[key] = map[[32]byte][]FileInfo{fi.SHA256: {fi}}
        return nil
    }

    // Fast path: the file already carries a complete SHA-256 (small files).
    if fi.SHA256 != zeroSHA {
        return d.placeSha(key, shaGroups, fi)
    }

    totalExisting := count(shaGroups)
    zeroFiles := shaGroups[zeroSHA]
    shaGroups[zeroSHA] = append(zeroFiles, fi)

    if totalExisting == 1 && len(zeroFiles) == 1 {
        // Exactly two unhashed files: compare directly.
        return []Execution{{Key: key, Type: HashComp,
            Files: []FileInfo{zeroFiles[0], fi}}}
    }

    // 3+ files: hash every unhashed file without a task in flight.
    return d.emitHashCalc(key, shaGroups)
}
```

### Consistency Rules

- **Keeper**: the first file placed in a SHA-256 bucket is the keeper and is never a victim.
- **No double elimination**: a file scheduled as a victim is recorded in the `scheduled` set, so it can never be selected as a keeper or handed out again.
- **No concurrent double hashing**: `inflight` tracks paths that already have a hash/compare task queued.
- **Partial resume**: when a comparison stops early, `OnCompareDone` persists the more advanced `HashState`/`HashOffset` for each file, so a later comparison resumes instead of re-reading from the start.
- **Complete hash**: `OnHashDone`/`OnCompareDone` move a file from the zero-SHA bucket into its concrete SHA-256 bucket and emit a `DupeElim` when it matches the bucket's keeper.
- **CoW-detect mode**: with `WithCoWDetect()` (`find --cow`), no per-pair work is
  emitted, every file stays in its SHA-256 bucket, and nothing is scheduled for
  elimination. After the input is drained, `CoWGroups()` returns one group per
  matching SHA-256 bucket with at least two distinct physical files, and the
  pipeline emits one `CoWDetect` execution per group.

### Comparison to the C Original

The C version uses a **binary search tree** with root at index 1, `Larger`/`Smaller` for navigation, and `Same` for collision chains. The Go version's two-level hash map is semantically equivalent:

| C concept | Go equivalent |
|-----------|---------------|
| `FileData[]` array | `groups` map |
| BST navigation (`Larger`/`Smaller`) | Hash map lookup (O(1)) |
| `Same` chain for collisions | `map[[32]byte][]FileInfo` buckets |
| `CheckDuplicate()` function | `Detector.Insert()` + outcome callbacks |

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
  else:
      → execute the configured action
```

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
2. remaining = size - HashOffset
3. While remaining > 0:
   a. Read up to chunkSize bytes from each file
   b. If either read is short/zero → truncated: stop, keep partial state
   c. Feed both chunks to their SHA-256 hashers
   d. If the accumulated hashes differ → stop early, keep partial state
   e. remaining -= bytes read
4. End of file with equal hashes → both SHA-256 values are complete
```

The executor returns the updated `FileInfo` records in the `Outcome`; the detector then treats a complete hash like an `OnHashDone` result and continues matching.

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
3. Otherwise execute the configured action
```

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
1. Check keeper.NumLinks < 1023 (Windows NTFS limit) → else ResultHardlinkLimit
2. If the victim is read-only and not --rdonly → ResultSkippedRO
3. os.Remove(victimPath) — delete the duplicate
4. os.Link(keeperPath, victimPath) — create a hardlink to the keeper
5. Restore the original file mode and modification time
6. → ResultHardlinked
```

The sequence "delete then link" (instead of linking over the existing file) is required because `os.Link` fails if the destination exists. On Windows `os.Link` wraps `CreateHardLinkW`; cross-volume hardlinks are impossible.

### CoW Clone

CoW elimination is implemented per platform. The victim is replaced with a block clone of the keeper:

- **Linux** (btrfs/XFS): `FICLONE` ioctl via `golang.org/x/sys/unix` `IoctlFileClone`.
- **macOS** (APFS): `clonefile(2)` via `unix.Clonefile`.
- **Windows** (ReFS/Dev Drive): `FSCTL_DUPLICATE_EXTENTS_TO_FILE`, cluster-aligned with the tail copied normally and the destination preallocated.

All platforms go through `cloneReplace(src, dst)` in `internal/action/cow.go`:

```
0. If keeper and victim are the same physical file, or their extent layouts
   compare Equal, → ResultAlreadyShared (no clone, no file change)
1. Create a temporary file next to the victim
2. Clone the keeper into the temporary path (platform-specific)
3. Preserve the victim's mode and mtime on the temporary file
4. Atomically replace the victim (os.Rename on Unix,
   MoveFileEx(REPLACE_EXISTING) on Windows)
```

The extent check is only a "skip the work" fast path: content equality was already
established by the detector, so cloning anyway is safe and idempotent. It is
deliberately conservative — `extent.Equal` returns false for empty lists, for
encoded (compressed) extents, and for unknown (zero) physical addresses, and any
uncertainty (unsupported filesystem, query failure) simply means "clone".

Unsupported filesystems return `ErrCoWNotSupported` and the victim is left
untouched. → `ResultCoWCloned` on success, `ResultAlreadyShared` when the pair
already shares all storage.

### CoW Detection (`find --cow`)

Detection is opt-in because it costs an extra open + extent query per file. It is
a single final pass: after the input is fully drained and every hash/comparison
has finished, the detector state is complete and the pipeline asks for groups.

1. `dupe.Detector.CoWGroups()` returns every SHA-256 bucket with at least two
   **distinct physical files**, keeping one path per `(Dev, Inode)`. Hardlinked
   aliases collapse to a single member; use `find --listlink` to list hardlink
   groups.
2. The pipeline turns each group into one `CoWDetect` execution
   (`cowGroupExecutions`) instead of one execution per pair.
3. `detectCoW` computes, for every member, how many of its bytes are already
   shared with another group member:
   - if any extent in the group carries the filesystem's `Shared` flag
     (Linux `FIEMAP_EXTENT_SHARED`), `extent.SharedFlagBytes` is used. This is a
     per-file signal: it says the extent is shared with someone, not with whom;
   - otherwise the physical start address is used as identity within the same
     device via `extent.SharedWithOthers`, capped to the shorter extent.
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

On filesystems without extent reporting `FileShared` stays nil: the group members
are still listed, with a note that extent information is unavailable. The
pairwise `extent.SharedBytes` (physical overlap, with a shared-logical fallback
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

The walker calls `filepath.WalkDir` to enumerate files, then filters each path against the user's patterns. For non-recursive patterns (no `**`), subdirectories are skipped via `filepath.SkipDir`. On Unix the walker also fills `Dev`/`Inode`/`NumLinks` from the already-available stat struct via `getFileIdentity`; on Windows it returns zeros and the identity is filled later by the parallel checksum path.

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
