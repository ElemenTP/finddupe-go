# Data Structures

## `FileInfo` — Per-File Record

Produced by the filesystem walker and completed by the checksum workers, then stored by the detector and carried through every `Execution` and `Outcome`.

```go
// FileInfo holds all metadata needed for duplicate detection on a single file.
type FileInfo struct {
    Path       string   // Absolute path to the file
    Size       int64    // File size in bytes
    ModTime    time.Time // Modification time when the content was read
    Signature  uint64   // 64-bit composite checksum (CRC32 << 32 | Sum32)
    SHA256     [32]byte // Full-content SHA-256; zero means "not yet computed"
    HashState  []byte   // Marshaled in-progress SHA-256 state (nil = not started)
    HashOffset int64    // Number of bytes already fed into the SHA-256 digest
    Dev        uint64   // Filesystem/volume id (st_dev on Unix, volume serial on Windows)
    Inode      uint64   // Inode / NTFS file index; unique only together with Dev
    NumLinks   uint64   // Number of hardlinks to this file (0 if unavailable)
    IsRef      bool     // True for files from a --ref path (never acted upon)
}
```

**Field rationale**:
- `Path`: string for maximum compatibility with Go's `os` package
- `Size`: int64 matches `os.FileInfo.Size()` return type
- `ModTime`: the modification time observed while the file's content was read (or scanned). Re-checked together with `Size` immediately before a file is eliminated, so a file that changed after its hash was computed is never acted upon
- `Signature`: uint64 — the C version uses two uint32 values (`Checksum_t`), packed into one uint64 for efficient map key usage
- `SHA256`: `[32]byte` — the zero value doubles as the "hash not yet computed" sentinel, so no separate flag is needed
- `HashState` / `HashOffset`: allow a partial SHA-256 to be resumed after an early-stopped comparison or an interrupted read instead of re-reading the file from the start
- `Dev`: uint64 — `st_dev` on Unix, volume serial number on Windows. Inode numbers are only unique per device, so `Dev` is part of every physical-identity comparison
- `Inode`: uint64 — on Unix, `syscall.Stat_t.Ino` via `f.Stat()`; on Windows, `GetFileInformationByHandle` (`nFileIndexHigh << 32 | nFileIndexLow`). Both come from the already-open handle in `checksum.ComputeFileInfo`, not from the walker on Windows
- `NumLinks`: uint64 — Windows NTFS supports up to 1023 links; Unix supports more but uint64 is safe
- `IsRef`: bool — marks files from `--ref` patterns; they participate in duplicate detection but are never deleted or replaced

**Relationship to C original**:
```
C FileData_t                     Go FileInfo
─────────────────────────        ─────────────────
Checksum.Crc + Checksum.Sum  →   Signature (packed uint64)
FileIndex.High + FileIndex.Low → Inode (packed uint64)
VolumeSerialNumber           →   Dev
NumLinks                      →  NumLinks
FileSize                      →  Size
(mtime re-check)              →  ModTime
FileName (WCHAR*)             →  Path (string)
(implicit via ref flag)       →  IsRef
```

## `InodeKey` — Physical File Identity

```go
// InodeKey identifies a physical file across the whole scan.
// Inode numbers are only unique per device, so Dev must be part of the key.
type InodeKey struct {
    Dev   uint64
    Inode uint64
}
```

Used by the detector's hardlink index (`map[InodeKey][]FileInfo`) for `find --listlink`, and by the executor to decide whether two paths are already hardlinked. Grouping by a bare inode number would merge unrelated files on different volumes.

## `GroupKey` — Duplicate Bucket Key

```go
// GroupKey is the composite key for grouping files by weak checksum and size.
type GroupKey struct {
    Signature uint64
    Size      int64
}
```

Pairing the signature with the size removes false collisions from the 32-bit sum wrapping at 4 GB, so the weak pass is (signature, size), not signature alone.

## `Execution` — Unit of Work

The detector never touches files. It emits `Execution` values that describe work for the stateless executor.

```go
// ExecutionType identifies the kind of work an Execution carries.
type ExecutionType uint8

const (
    HashCalc   ExecutionType = iota // Compute the full SHA-256 of Files[0]
    HashComp                         // Compare Files[0] and Files[1] in chunks
    DupeElim                         // Eliminate Files[1:] keeping Files[0]
    CoWDetect                        // Report per-file shared bytes for a content group
)

// Execution represents a concrete unit of work for the executor.
type Execution struct {
    Key   GroupKey       // Composite (signature, size) key the files belong to
    Type  ExecutionType  // Kind of work to perform
    Files []FileInfo     // Files this execution operates on
}
```

`ExecutionType` implements `fmt.Stringer` (`"HashCalc"`, `"HashComp"`, `"DupeElim"`, `"CoWDetect"`).

**File ordering contract**:
- `HashCalc`: `Files[0]` is the file to hash fully; it may carry a partial `HashState`/`HashOffset` to resume from.
- `HashComp`: `Files[0]` and `Files[1]` are the pair to compare.
- `DupeElim`: `Files[0]` is the keeper and `Files[1]` is the victim. The keeper is never written to; hardlinking a victim links it to the keeper's inode and therefore also gives it the keeper's permissions and timestamps.
- `CoWDetect`: `Files` holds every member of one identical-content group (one path per `(Dev, Inode)`; hardlinked aliases collapse to a single representative), and the outcome's `FileShared` slice has one entry per member.

## `Stats` — Statistics Accumulator

Thread-safe counter for scan and action results. All fields use `atomic.Int64` for lock-free concurrent access.

```go
type Stats struct {
    TotalFiles      atomic.Int64 // Total files processed
    TotalBytes      atomic.Int64 // Total bytes across all files
    DuplicateFiles  atomic.Int64 // Confirmed duplicate files found
    DuplicateBytes  atomic.Int64 // Bytes in duplicate files
    HardlinkGroups  atomic.Int64 // Hardlink groups found (find --listlink)
    CantReadFiles   atomic.Int64 // Files that could not be read/opened
    ZeroLengthFiles atomic.Int64 // Zero-length files skipped or included
    DeletedFiles    atomic.Int64 // Files deleted (dedupe --delete)
    HardlinkedFiles atomic.Int64 // Files replaced with hardlinks (dedupe --hardlink)
    CoWClonedFiles  atomic.Int64 // Files replaced with CoW clones (dedupe --cow)
    CoWGroups       atomic.Int64 // CoW (identical-content) groups found by find --cow
    CoWSharedBytes  atomic.Int64 // Per-file sum of already-shared bytes (each range once per file)
    SkippedROFiles  atomic.Int64 // Read-only files skipped
    SkippedRefFiles atomic.Int64 // Reference files skipped
    SkippedChangedFiles atomic.Int64
    FailedFiles        atomic.Int64 // Eliminations that were attempted and failed // Files whose pair changed after hashing
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
type Action int
const (
    ActionReport   Action = iota // No action (find mode default)
    ActionDelete                 // Delete duplicate files
    ActionHardlink               // Replace duplicates with hardlinks
    ActionCoWClone               // Replace duplicates with CoW clones
)

type Config struct {
    Action          Action
    Paths           []string
    RefPaths        []string
    Threads         int    // 0 = runtime.NumCPU() * 2 workers
    Verbose         bool
    ListLink        bool   // find --listlink: list hardlink groups and exit
    CoWDetect       bool   // find --cow: report identical-content groups with per-file shared ratios
    ShowProgress    bool   // default: true
    FollowSymlinks  bool
    IncludeZeroLen  bool
    IncludeReadonly bool   // dedupe mode, Windows
    SkipHardlinked  bool   // find --hardlink: skip same-(Dev,Inode) pairs
}
```

There is no signature-printing field or flag; signature printing was removed as dead code (see "Future work" in [cli-spec.md](cli-spec.md)).

## Detector Internal State

The detector is a mutex-guarded state machine. It owns **all** duplicate state; the executor is stateless.

```go
type Detector struct {
    mu sync.Mutex

    // groups maps the composite key to its SHA-256 sub-buckets. The zeroSHA
    // bucket holds files whose full hash is still unknown (possibly partial).
    groups map[GroupKey]map[[32]byte][]FileInfo

    // inodes groups files by physical identity for --listlink.
    inodes map[InodeKey][]FileInfo

    // groups maps the composite key to one keyState: a bucket per known
    // SHA-256 plus the files whose digest is still unknown.
    groups map[GroupKey]*keyState

    // coWDetect emits one CoWDetect per content bucket instead of per-victim
    // DupeElim work (find --cow).
    coWDetect bool

    // policy orders the members of a content bucket; the first is kept.
    policy KeeperPolicy

    // chooser, when set, decides the keeper instead of the policy.
    chooser KeeperChooser

    // finalKeys / finalIdx walk the groups once the scan is over.
    finalKeys []GroupKey
    finalIdx  int

    // failed counts finalization hash attempts that could not be completed.
    failed map[string]int

    stats *Stats
}
```

**API**:
```go
func NewDetector(stats *Stats, opts ...Option) *Detector
func WithCoWDetect() Option
func WithKeeperPolicy(policy KeeperPolicy) Option
func WithKeeperChooser(chooser KeeperChooser) Option

func (d *Detector) Insert(fi FileInfo) []Execution
func (d *Detector) OnHashDone(key GroupKey, fi FileInfo, incomplete bool)
func (d *Detector) OnCompareDone(key GroupKey, a, b FileInfo, incomplete bool)
func (d *Detector) NextFinal(limit int) ([]Execution, bool)
func (d *Detector) InsertInode(fi FileInfo)
func (d *Detector) InodeGroups() [][]FileInfo
func (d *Detector) Len() int
func (d *Detector) Stats() *Stats
```

**Strategy by group size** (files sharing one `GroupKey`):
1. **1 file** → store it; no SHA-256 work yet (avoids unnecessary I/O), and no per-bucket map is allocated.
2. **Exactly 2 unhashed files** → emit one `HashComp` (chunked comparison with early-stop). A verdict of "different" settles the pair; an interrupted attempt keeps its partial state and is resumed by `NextFinal`.
3. **3+ files** → emit `HashCalc` for the file just inserted only (O(1) per insert); leftovers from an early-stopped comparison are completed by `NextFinal`.

**Consistency rules**:
- Elimination is deferred to `NextFinal`. For every content bucket whose content is fully known, the members are ordered by the **keeper policy** and the first one is kept; every other member becomes a victim. The keeper is therefore reproducible, not "whichever hash finished first".
- Hardlinked aliases are collapsed after the policy ordering, so the surviving path of an inode is the preferred one and a path can never be both keeper and victim.
- A file whose hash cannot be completed is retried at most `maxFailedHashRetries` times and then left alone: a file whose content was never verified is never eliminated.
- With `WithCoWDetect()` (`find --cow`), `NextFinal` emits one `CoWDetect` per bucket that has at least two distinct physical files, keeping one path per `(Dev, Inode)` so hardlinked aliases collapse to a single member.
- `InsertInode` ignores files with `Inode == 0` or `NumLinks < 2`, and groups by `InodeKey` (Dev + Inode).

## Action `Outcome` and `Result`

`DoExecution` returns an `Outcome` that the coordinator feeds back to the detector.

```go
type Outcome struct {
    Kind       dupe.ExecutionType // Mirrors the executed Execution's type
    Key        dupe.GroupKey      // Composite key the execution belonged to
    Files      []dupe.FileInfo    // Updated hash progress or participating files
    Result     Result             // Set for DupeElim outcomes
    Err        error              // Failure behind ResultError, reported by the coordinator
    FileShared []int64            // CoWDetect: already-shared bytes per Files entry
}

type Result int
const (
    ResultVerifiedDuplicate Result = iota // Confirmed duplicate (report mode)
    ResultAlreadyHardlinked               // Same-(Dev,Inode) pair; no file touched
    ResultDeleted                         // Duplicate deleted
    ResultHardlinked                      // Duplicate replaced with hardlink
    ResultCoWCloned                       // Duplicate replaced with CoW clone
    ResultSkippedRO                       // Read-only file skipped
    ResultSkippedRef                      // Reference file skipped
    ResultHardlinkLimit                   // NTFS link limit reached
    ResultError                           // Action failed
    ResultAlreadyShared                   // dedupe --cow: pair already shares all storage
    ResultSkippedChanged                  // A file changed after its content was hashed
    ResultSkippedCrossDevice              // Hardlink refused: the files are on different devices
)
```

For `HashCalc`/`HashComp` the outcome's `Files` carry the updated `SHA256`/`HashState`/`HashOffset`. For `DupeElim` they carry the keeper and victim; for `CoWDetect` they carry every member of the group, with `FileShared[i]` giving the already-shared bytes of `Files[i]`. `FileShared` is nil when extent information is unavailable for the whole group (unsupported filesystem), and the group is then reported without ratios.

## `extent.Extent` — Physical Extent

```go
type Extent struct {
    Logical  uint64 // Byte offset of the run within the file
    Physical uint64 // Device-relative physical offset (meaningful when !Encoded)
    Length   uint64 // Logical run length in bytes
    Shared   bool   // Filesystem's "shared with another file" hint, when known
    Encoded  bool   // On-disk representation is not a plain block range
}
```

`Encoded` covers, for example, compressed btrfs extents: their `Physical` offsets and logical lengths are not comparable, so they are excluded from physical comparison and only the shared-logical-range fallback is used.

Helper functions:

```go
func Equal(a, b []Extent) bool                         // identical, trusted layout
func SharedFlagBytes(e []Extent) int64                 // bytes the FS marked Shared
func SharedWithGroup(group [][]Extent) []int64             // per-member already-shared bytes
```

- On macOS, `Physical` is the device byte offset from `fcntl(F_LOG2PHYS_EXT)`; for decmpfs-compressed files, where the kernel returns `ENOTSUP`, it is the APFS clone ID instead, marked `Opaque` (family-level identity, exact match only).
- `SharedWithGroup` intersects physical ranges once for the whole group (a byte shared with several group members counts once); encoded and opaque extents fall back to an exact physical-start match.
- `Equal` is a conservative "already sharing" fast path for `dedupe --cow`: true only when both lists are non-empty, have the same length in the same logical order, every pair has equal `Logical`/`Physical`/`Length`, and no `Physical` is 0. `Encoded` extents are compared too: exact start+length identity stays sound under compression (only range arithmetic is not). Anything else returns false, which merely means the clone is attempted.
- `SharedFlagBytes` sums the lengths of extents the filesystem marked `Shared` (Linux `FIEMAP_EXTENT_SHARED`). It is a per-file signal: it says the extent is shared with someone, not with whom.
- `SharedWithGroup` is used for in-group ratios on filesystems without a shared flag; it merges every member's physical ranges and sweeps them once over compressed coordinates, so a shared run split at different boundaries per file is still counted once and every member is answered in one pass. `Opaque` extents (the APFS clone ID for compressed files) are excluded from range arithmetic and compared by exact key only, since unrelated keys can be numerically adjacent.

## Constants

```go
// internal/checksum
const BytesToChecksum = 32768 // First 32KB used for the weak signature

// internal/action

const (
    chunkSizeSmall       = 64 * 1024        // medium files: cheap early-stop
    chunkSizeLarge       = 1024 * 1024      // big files: fewer syscalls
    chunkSizeMediumLimit = 16 * 1024 * 1024 // largest file using chunkSizeSmall
)
```
