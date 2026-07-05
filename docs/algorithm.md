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

The function `checksum.ComputeFileInfo(path, size)` opens the file **once** and returns:
- `sig uint64` — the 64-bit checksum signature
- `inode uint64` — filesystem inode / NTFS file index
- `numLinks uint64` — hardlink count
- `err error`

This unified call avoids a second file-open in the walker goroutine:
- On **Unix**: inode is read from `f.Stat().Sys().(*syscall.Stat_t)`
- On **Windows**: inode is read from `GetFileInformationByHandle` on the open file handle

`Compute(path, size)` delegates to `ComputeFileInfo`, discarding inode/link info.

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

Files are grouped by their 64-bit checksum. Each group is a list of files that share the same checksum. When a new file arrives with a checksum that already exists, it is a **potential duplicate** and must be verified with a full byte-by-byte comparison.

### Hash Map Approach

```go
// groups maps checksum → files with that checksum
groups := make(map[uint64][]FileInfo)

func insert(fi FileInfo) []DupeGroup {
    group, exists := groups[fi.Signature]
    if !exists {
        groups[fi.Signature] = []FileInfo{fi}
        return nil // No potential duplicates
    }

    // Checksum collision — create ONE DupeGroup against the first stored file.
    // Only the first file in the group is used as the "original" to verify against.
    // This matches the C version's behavior of comparing against the first match.
    dupe := DupeGroup{Original: group[0], Candidate: fi}

    // Append to the collision chain regardless of verification outcome.
    // CRC collisions (false positives) stay in the chain for future comparison.
    groups[fi.Signature] = append(group, fi)
    return []DupeGroup{dupe}
}
```

### Comparison to C Original

The C version uses a **binary search tree** with root at index 1, `Larger`/`Smaller` for navigation, and `Same` for collision chains. The Go version's hash map + slice approach is semantically equivalent:

| C concept | Go equivalent |
|-----------|---------------|
| `FileData[]` array | `groups` map |
| BST navigation (`Larger`/`Smaller`) | Hash map lookup (O(1)) |
| `Same` chain for collisions | Slice for same-checksum files |
| `CheckDuplicate()` function | `Detector.Insert()` method |

### Hardlink-Aware Duplicate Detection (`--hardlink` in find mode)

When `--hardlink` is used with `find`, duplicate detection still operates on content checksums, but the verification step additionally checks the inode:

```
Before full byte comparison:
  if SkipHardlinked && original.Inode != 0 &&
     original.Inode == candidate.Inode &&
     original.NumLinks > 1:
       → Skip (ResultAlreadyHardlinked)
       → Don't report, don't count in stats

Otherwise:
  → Proceed with full byte-by-byte verification
```

This means:
- Files with **same content + same inode** (already hardlinked) → silently skipped
- Files with **same content + different inode** → reported as duplicates
- Files with **different content** → not duplicates (CRC collision, stored in chain)

## 3. Full File Verification

### Purpose

After checksum match, perform a full byte-by-byte comparison to confirm the files are truly identical. This handles:
- CRC collisions (theoretically possible with 64-bit space)
- Same first 32KB but different content after (e.g., video files with same header)

### Algorithm

```
Given: fileA path, fileB path, expected size

1. Quick check: if sizes differ → return false immediately
2. Quick check: if os.SameFile (same inode) → return true (same physical file)
3. Open both files for reading
4. Allocate two 64KB buffers
5. While bytes remain:
   a. Read up to 64KB from each file
   b. Compare the two buffers with bytes.Equal()
   c. If mismatch → close files, return false
6. Close files, return true
```

### Performance Considerations

- Early exit: return on first mismatch (don't read the rest)
- Memory: fixed 128KB allocation (two 64KB buffers) regardless of file size
- Same-file shortcut: `os.SameFile` avoids reading when inode matches

## 4. Action Execution

### Verification Flow (`VerifyAndExecute`)

```
1. If SkipHardlinked: check candidate.Inode vs original.Inode → skip if same
2. VerifyFullFile: full byte comparison
3. If not duplicate → ResultNotDuplicate
4. If candidate is reference file → ResultSkippedRef
5. Execute configured action → Result{Deleted,Hardlinked,CoWCloned,VerifiedDuplicate}
```

### Delete

```
1. Check candidate file permissions
2. If readonly and not --rdonly → skip (ResultSkippedRO)
3. If readonly and --rdonly → chmod to add write permission
4. os.Remove(candidatePath)
5. Update stats
```

### Hardlink

```
1. Verify source file exists
2. Check source NumLinks < 1023 (Windows NTFS limit)
3. os.Remove(candidatePath) — delete the duplicate
4. os.Link(originalPath, candidatePath) — create hardlink
5. Restore file mode and modification time
6. Update stats
```

The sequence "delete then link" (instead of `os.Link` directly on the existing file) is required because:
- `os.Link` fails if the destination exists
- We delete the duplicate's directory entry first, then create a new one pointing to the original's inode

### CoW Clone (Stub)

Returns `ErrCoWNotSupported` on all platforms. Real implementation deferred:
- **Linux (btrfs/xfs)**: `ioctl FICLONERANGE`
- **macOS (APFS)**: `clonefile(2)`
- **Windows (ReFS)**: `FSCTL_DUPLICATE_EXTENTS_TO_FILE`

## 5. Glob Pattern Matching (`**`)

### Purpose

Match file paths against patterns with `**` (recursive wildcard) support, matching the semantics of the original C `myglob.c`.

### Algorithm

Implemented as a recursive component-matcher in `fswalker/matchComponents`:

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

The walker calls `filepath.WalkDir` to enumerate files, then filters each path against the user's patterns. For non-recursive patterns (no `**`), subdirectories are skipped via `filepath.SkipDir`.

## 6. Multi-Threading Design

### Parallelization Strategy

The checksum computation (which includes `os.Open` + read 32KB + CRC calculation + inode retrieval) is the CPU/I/O bottleneck and runs in a **bounded worker pool**:

```
Walker (1 goroutine):     filepath.WalkDir → stat → send to channel
Scanner feeder (1 goroutine): read channel → pool.Submit (may block)
Worker pool (N goroutines):  os.Open → CRC + inode → send to fileCh
Detector (1 goroutine):  hash map insert → create DupeGroups
Executor (1 goroutine):  full comparison → action
```

The worker pool uses a semaphore channel (`chan struct{}`) with buffer size `threads` to limit concurrency. `pool.Submit` blocks when all workers are busy, providing natural back-pressure.

### File Open Optimization

Files are opened **once** per scan in the parallel worker pool goroutines (via `checksum.ComputeFileInfo`). On Windows, `GetFileInformationByHandle` is called on the same handle — avoiding a second `CreateFile` call that would serialize I/O in the single-threaded walker.

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
