# Packages

## Package Layout

All implementation code lives under `internal/` — there is no public API (this is a CLI tool, not a library).

```
finddupe-go/
├── main.go                    # Entry point
├── cmd/                       # CLI layer (cobra commands)
│   ├── root.go                # Root command + Execute()
│   ├── find.go                # find subcommand
│   ├── dedupe.go              # dedupe subcommand
│   └── version.go             # version subcommand
├── internal/
│   ├── config/                # Configuration
│   │   └── config.go          # Config struct, Mode, Action types
│   ├── dupe/                  # Core types and duplicate-detection state machine
│   │   ├── fileinfo.go        # FileInfo, InodeKey, GroupKey, Execution, ExecutionType
│   │   ├── detector.go        # Detector state machine (Insert/OnHashDone/OnCompareDone)
│   │   └── stats.go           # Thread-safe statistics (+ ZeroLenCounter)
│   ├── fswalker/              # Filesystem traversal
│   │   ├── walker.go          # Walker, Result, WalkOptions, glob matching
│   │   ├── walker_unix.go     # Unix: Dev/Inode/NumLinks from stat (getFileIdentity)
│   │   └── walker_windows.go  # Windows: returns zeros — done in checksum
│   ├── checksum/              # File signature + identity computation
│   │   ├── checksum.go        # Compute, ComputeFileInfo (returns Info), ComputeFromReader
│   │   ├── inode_unix.go      # Unix: fileIdentity from f.Stat().Sys()
│   │   └── inode_windows.go   # Windows: fileIdentity from GetFileInformationByHandle
│   ├── action/                # Stateless duplicate elimination + CoW detection
│   │   ├── executor.go        # Executor, DoExecution, Outcome, Result
│   │   ├── delete.go          # File deletion (+ readonly handling)
│   │   ├── hardlink.go        # Delete-then-link hardlink creation
│   │   ├── hardlink_unix.go   # Unix: os.Link
│   │   ├── hardlink_windows.go# Windows: os.Link (CreateHardLinkW) + link limit
│   │   ├── cow.go             # cloneReplace + detectCoW (CoWDetect execution)
│   │   ├── clone_linux.go     # Linux: FICLONE ioctl (btrfs/XFS)
│   │   ├── clone_darwin.go    # macOS: clonefile(2) (APFS)
│   │   ├── clone_windows.go   # Windows: FSCTL_DUPLICATE_EXTENTS_TO_FILE (ReFS)
│   │   ├── clone_other.go     # Other platforms: ErrCoWNotSupported
│   │   ├── replace_unix.go    # Unix: atomic os.Rename
│   │   └── replace_windows.go # Windows: MoveFileEx(REPLACE_EXISTING)
│   ├── extent/                # Physical extent query for CoW detection
│   │   ├── extent.go          # Extent, SharedBytes, ErrUnsupported
│   │   ├── query_linux.go     # Linux: FS_IOC_FIEMAP
│   │   ├── query_darwin.go    # macOS: fcntl(F_LOG2PHYS_EXT) (undocumented)
│   │   ├── query_windows.go   # Windows: FSCTL_GET_RETRIEVAL_POINTERS
│   │   └── query_other.go     # Other platforms: Supported()==false
│   ├── worker/                # Worker pool
│   │   └── pool.go            # Bounded goroutine pool
│   ├── pipeline/              # Orchestration
│   │   └── pipeline.go        # Run, runListLink, coordinate, printSummary
│   └── progress/              # Progress display
│       └── progress.go        # ANSI progress reporter
├── test/                      # System integration tests
│   ├── doc.go                 # Package doc (package test)
│   └── system_test.go         # System tests (package test_test)
└── docs/                      # Documentation (this directory)
```

## Package Responsibilities

### `cmd/` — CLI Layer

**Purpose**: Parse command-line arguments, validate them, construct a `config.Config`, and invoke `pipeline.Run()`.

- `root.go`: Root cobra command. `Execute()` called from `main.go`.
- `find.go`: `finddupe find` — defines find flags (`--hardlink`, `--listlink`, `--cow`, …), enforces their mutual exclusivity.
- `dedupe.go`: `finddupe dedupe` — defines dedupe flags and validates that exactly one action is set.
- `version.go`: `finddupe version` — version, build time, platform, Go version.

**Dependencies**: `github.com/spf13/cobra`, `internal/config`, `internal/pipeline`

### `internal/config` — Configuration

**Purpose**: Define the `Config` struct and related types. Pure data — no behavior.

**Key fields**: `Mode`, `Action`, `Paths`, `RefPaths`, `Threads`, `Verbose`, `ListLink`, `CoWDetect`, `ShowProgress`, `FollowSymlinks`, `IncludeZeroLen`, `IncludeReadonly`, `SkipHardlinked`

**Dependencies**: None (std types only)

### `internal/dupe` — Core Types and Detection

**Purpose**: Define fundamental data types and implement the duplicate-detection **state machine**.

**Types exported**: `FileInfo`, `InodeKey`, `GroupKey`, `Execution`, `ExecutionType`, `Stats`, `Detector`, `Option`

**Execution types**: `HashCalc`, `HashComp`, `DupeElim`, `CoWDetect`

**Detector API**:
```go
func NewDetector(stats *Stats, opts ...Option) *Detector
func WithCoWDetect() Option

func (d *Detector) Insert(fi FileInfo) []Execution
func (d *Detector) OnHashDone(key GroupKey, fi FileInfo) []Execution
func (d *Detector) OnCompareDone(key GroupKey, a, b FileInfo) []Execution
func (d *Detector) InsertInode(fi FileInfo)
func (d *Detector) InodeGroups() [][]FileInfo
func (d *Detector) Len() int
func (d *Detector) Stats() *Stats
```

- `Insert` — checksum-based insertion; returns the work a new file triggers.
- `OnHashDone` / `OnCompareDone` — feed executor outcomes back in and return follow-up work.
- `InsertInode` / `InodeGroups` — `(Dev, Inode)` hardlink index used by `find --listlink`.

**Dependencies**: None (stdlib only)

### `internal/fswalker` — Filesystem Walker

**Purpose**: Walk directories matching glob patterns. On Unix, also retrieves `Dev`/`Inode`/`NumLinks` from stat. On Windows, identity retrieval is deferred to the parallel checksum path.

**Interface**:
```go
type Walker struct{}
func New() *Walker
func (w *Walker) Walk(ctx, patterns, opts) <-chan Result

type Result struct {
    Info dupe.FileInfo
    Err  error
}
```

**Platform helpers**: `getFileIdentity(path, info)` in `walker_unix.go` / `walker_windows.go`.

**Features**: `**` recursive glob matching, symlink following control (with loop prevention), zero-length file filtering, `ZeroLenCounter` callback.

**Dependencies**: `internal/dupe` (for `FileInfo`)

### `internal/checksum` — Checksum + Identity Computation

**Purpose**: Compute the 64-bit composite file signature AND retrieve `Dev`/`Inode`/`NumLinks` from a single file-open call.

**Public API**:
```go
const BytesToChecksum = 32768

type Info struct {
    Signature uint64
    Dev       uint64
    Inode     uint64
    NumLinks  uint64
    SHA256    [32]byte
}

func Compute(path string, size int64) (uint64, error)
func ComputeFileInfo(path string, size int64) (Info, error)
func ComputeFromReader(r io.Reader, size int64) (uint64, error)
```

**Platform-specific**:
- `inode_unix.go`: `fileIdentity(f *os.File)` reads `Dev`/`Inode`/`NumLinks` from `f.Stat().Sys().(*syscall.Stat_t)`
- `inode_windows.go`: `fileIdentity(f *os.File)` calls `GetFileInformationByHandle` on `f.Fd()`

`ComputeFileInfo` is the primary function used by the pipeline — it opens the file once and returns all metadata, keeping I/O in the parallel worker-pool path. For files ≤ 32KB it also returns the full SHA-256 at no extra I/O cost.

**Dependencies**: None (stdlib + platform syscalls)

### `internal/action` — Stateless Action Executor

**Purpose**: Run `dupe.Execution` work items: full hashing, chunked comparison, elimination, and CoW detection.

**API**:
```go
type Executor struct{ ... }
type Options struct {
    Action          config.Action
    IncludeReadonly bool
    SkipHardlinked  bool
}
func New(opts Options) *Executor
func (e *Executor) DoExecution(ctx context.Context, ex dupe.Execution) (Outcome, error)

type Outcome struct {
    Kind        dupe.ExecutionType
    Key         dupe.GroupKey
    Files       []dupe.FileInfo
    Result      Result
    Shared      bool
    SharedBytes int64
}
```

**Behavior by execution type**:
- `HashCalc`: full SHA-256 of one file, resuming from `HashState`/`HashOffset`.
- `HashComp`: chunked comparison of two files with early-stop; partial state is preserved for resume.
- `DupeElim`: delete / hardlink / CoW-clone the victim, or report it. Checks `SkipHardlinked` with `(Dev, Inode)` and refuses to eliminate reference victims.
- `CoWDetect`: query both files' extents (same device only) and report `Shared`/`SharedBytes`.

**CoW clone helpers**: `cloneReplace` (shared orchestration), `clonePlatformFile` (per OS), `replaceFile` (per OS), `ErrCoWNotSupported`.

**Action results**: `ResultVerifiedDuplicate`, `ResultAlreadyHardlinked`, `ResultDeleted`, `ResultHardlinked`, `ResultCoWCloned`, `ResultSkippedRO`, `ResultSkippedRef`, `ResultHardlinkLimit`, `ResultNotDuplicate`, `ResultError`

**Dependencies**: `internal/dupe`, `internal/config`, `internal/extent`, `golang.org/x/sys` (CoW ioctls)

### `internal/extent` — Physical Extent Query

**Purpose**: Query the physical extents backing files so CoW clones (which share extents) can be distinguished from independent copies.

**API**:
```go
var ErrUnsupported = errors.New("extent query not supported on this filesystem")

type Extent struct {
    Logical  uint64
    Physical uint64
    Length   uint64
    Shared   bool
    Encoded  bool
}

func Query(path string) ([]Extent, error)
func SharedBytes(a, b []Extent) int64
func Supported() bool
```

**Platform implementations**: Linux FIEMAP (`FS_IOC_FIEMAP`), macOS `F_LOG2PHYS_EXT` (undocumented), Windows `FSCTL_GET_RETRIEVAL_POINTERS`; other platforms return `ErrUnsupported`. `SharedBytes` prefers physical overlap of non-encoded extents and falls back to shared logical ranges (compressed btrfs).

**Dependencies**: `golang.org/x/sys` (Unix/Windows syscalls)

### `internal/worker` — Worker Pool

**Purpose**: Manage a bounded set of goroutines for parallel file operations.

**API**:
```go
type Pool struct { ... }
func New(size int) *Pool
func (p *Pool) Submit(ctx context.Context, fn func(context.Context)) bool
func (p *Pool) Wait()
```

**Dependencies**: None (stdlib only)

### `internal/pipeline` — Orchestration

**Purpose**: Wire all components together, manage goroutine lifecycle, handle signals, format output.

**API**:
```go
func Run(ctx context.Context, cfg *config.Config) error
```

**Internal**:
- `runListLink` — the `find --listlink` path (inode grouping without duplicate detection).
- `walkAll` — walks `RefPaths` first, then `Paths`.
- `scanChecksums` — feeds checksum tasks to the worker pool; drains the reference phase first.
- `coordinate` — the coordinator loop that owns the detector, dispatches executions, and decides termination.
- `runExecutor` — the per-worker executor loop.
- `reportOutcome` / `reportElimination` / `reportCoW` — user-facing output and stats.
- `printSummary` — final statistics.

**Dependencies**: All other `internal/` packages

### `internal/progress` — Progress Display

**Purpose**: Display live-updating scan progress via ANSI escape codes.

**Dependencies**: `internal/dupe` (for `Stats`)

## Dependency Graph

```
cmd/
 ├── internal/config
 └── internal/pipeline
      ├── internal/config
      ├── internal/dupe
      ├── internal/fswalker
      │    └── internal/dupe
      ├── internal/checksum
      ├── internal/action
      │    ├── internal/dupe
      │    ├── internal/config
      │    └── internal/extent
      ├── internal/worker
      └── internal/progress
           └── internal/dupe
```

## Interface Design Principles

1. **Accept interfaces, return structs**: Packages define interfaces for their dependencies (e.g., `fswalker.ZeroLenCounter`) but return concrete types
2. **Channels for data flow**: Producer-consumer stages communicate via channels with explicit direction in function signatures
3. **Context for cancellation**: Every long-running operation accepts `context.Context`
4. **Constructor functions**: Types use `New*` constructors; struct literals used only where allowed by lint
5. **Stateful detection, stateless execution**: The `Detector` owns all duplicate state behind one mutex; the `Executor` holds only immutable options. This is what lets executor workers run in parallel
6. **No global state**: All state flows through the pipeline; stats use atomics for lock-free concurrent access
