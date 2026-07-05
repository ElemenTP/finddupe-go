# Data Structures

## `FileInfo` — Per-File Record

Produced by the filesystem walker + checksum computation, consumed by the duplicate detector.

```go
// FileInfo holds all metadata needed for duplicate detection on a single file.
type FileInfo struct {
    Path      string // Absolute path to the file
    Size      int64  // File size in bytes
    Signature uint64 // 64-bit composite checksum (CRC32 << 32 | Sum32)
    Inode     uint64 // Filesystem object identifier (inode on Unix, file index on Windows)
    NumLinks  uint64 // Number of hardlinks to this file (0 if unavailable)
    IsRef     bool   // True if file is from a --ref path (never to be acted upon)
}
```

**Field rationale**:
- `Path`: string for maximum compatibility with Go's `os` package
- `Size`: int64 matches `os.FileInfo.Size()` return type
- `Signature`: uint64 — the C version uses two uint32 values (`Checksum_t`), packed into one uint64 for efficient map key usage
- `Inode`: uint64 — on Unix, retrieved from `syscall.Stat_t.Ino` via `f.Stat()`; on Windows, retrieved from `GetFileInformationByHandle` (`nFileIndexHigh << 32 | nFileIndexLow`). Both are obtained from the already-open file handle in `checksum.ComputeFileInfo`, not in the walker
- `NumLinks`: uint64 — Windows NTFS supports up to 1023 links; Unix supports more but uint64 is safe
- `IsRef`: bool — marks files from `--ref` patterns; they participate in duplicate detection but are never deleted or replaced

**Relationship to C original**:
```
C FileData_t                     Go FileInfo
─────────────────────────        ─────────────────
Checksum.Crc + Checksum.Sum  →   Signature (packed uint64)
FileIndex.High + FileIndex.Low → Inode (packed uint64)
NumLinks                      →  NumLinks
FileSize                      →  Size
FileName (WCHAR*)             →  Path (string)
(implicit via ref flag)       →  IsRef
```

## `DupeGroup` — Potential Duplicate Pair

Produced by the detector when a checksum match is found, consumed by the executor for verification.

```go
// DupeGroup represents a pair of files that have the same checksum.
type DupeGroup struct {
    Signature uint64   // The checksum that matched
    Original  FileInfo // The first file stored with this checksum
    Candidate FileInfo // The newly discovered file with the same checksum
}
```

The executor performs a full byte-by-byte comparison between `Original` and `Candidate`. If they match, they are confirmed duplicates and the configured action is executed on `Candidate`.

## `Stats` — Statistics Accumulator

Thread-safe counter for scan and action results. All fields use `atomic.Int64` for lock-free concurrent access.

```go
type Stats struct {
    TotalFiles      atomic.Int64 // Total files processed
    TotalBytes      atomic.Int64 // Total bytes across all files
    DuplicateFiles  atomic.Int64 // Confirmed duplicate files found
    DuplicateBytes  atomic.Int64 // Bytes in duplicate files
    CantReadFiles   atomic.Int64 // Files that could not be read/opened
    ZeroLengthFiles atomic.Int64 // Zero-length files skipped or included
    HardlinkGroups  atomic.Int64 // Hardlink groups found (reserved)
    DeletedFiles    atomic.Int64 // Files deleted (dedupe --delete)
    HardlinkedFiles atomic.Int64 // Files replaced with hardlinks (dedupe --hardlink)
    CoWClonedFiles  atomic.Int64 // Files replaced with CoW clones (dedupe --cow)
    SkippedROFiles  atomic.Int64 // Read-only files skipped
    SkippedRefFiles atomic.Int64 // Reference files skipped
}
```

`Stats` implements the `fswalker.ZeroLenCounter` interface:
```go
func (s *Stats) AddZeroLen(delta int64) {
    s.ZeroLengthFiles.Add(delta)
}
```

## `Config` — Runtime Configuration

Constructed by `cmd/find.go` or `cmd/dedupe.go` from CLI flags.

```go
type Mode int
const (
    ModeFind   Mode = iota // find — scan and report only
    ModeDedupe              // dedupe — scan and take action
)

type Action int
const (
    ActionReport   Action = iota // No action (find mode default)
    ActionDelete                 // Delete duplicate files
    ActionHardlink               // Replace duplicates with hardlinks
    ActionCoWClone               // Replace duplicates with CoW clones
)

type Config struct {
    Mode            Mode
    Action          Action
    Paths           []string
    RefPaths        []string
    Threads         int    // 0 = use runtime.NumCPU()
    Verbose         bool
    PrintSigs       bool   // Not yet implemented
    ShowProgress    bool   // default: true
    FollowSymlinks  bool
    IncludeZeroLen  bool
    IncludeReadonly bool   // dedupe mode, Windows
    SkipHardlinked  bool   // find mode --hardlink: skip same-inode pairs
}
```

## Detector Internal State

```go
type Detector struct {
    // Maps checksum → files sharing that checksum (collision chain)
    groups map[uint64][]FileInfo

    // Maps inode → files sharing that inode (reserved for hardlink detection)
    inodeGroups map[uint64][]FileInfo

    stats *Stats
}
```

**Insertion algorithm**:
1. Look up by `fi.Signature` in `groups`
2. If no entry → create group with `fi` as first element → return `nil`
3. If entry exists → create `DupeGroup{Original: group[0], Candidate: fi}` → return it
4. Always append `fi` to the group (collision chain)

Only the **first stored file** in each checksum group is used as the "original" for comparison. This matches the C version's behavior.

## Action Result

```go
type Result int
const (
    ResultVerifiedDuplicate Result = iota // Confirmed duplicate (find mode)
    ResultAlreadyHardlinked              // Same inode pair skipped (--hardlink)
    ResultDeleted                         // Duplicate deleted
    ResultHardlinked                      // Duplicate replaced with hardlink
    ResultCoWCloned                       // Duplicate replaced with CoW clone
    ResultSkippedRO                      // Read-only file skipped
    ResultSkippedRef                     // Reference file skipped
    ResultHardlinkLimit                  // NTFS link limit reached
    ResultNotDuplicate                   // Files differ (CRC collision)
    ResultError                          // Action failed
)
```

## Constants

```go
const (
    BytesToChecksum = 32768   // First 32KB used for signature
    ChunkSize       = 0x10000 // 64KB chunk size for full file comparison
    MaxHardlinks    = 1023    // Windows NTFS hardlink limit per file
)
```
