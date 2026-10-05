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
│   │   └── config.go          # Config struct, Action type
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
│   │   ├── extent.go          # Extent, SharedBytes, Equal, SharedFlagBytes, SharedWithOthers
│   │   ├── query_linux.go     # Linux: FS_IOC_FIEMAP
│   │   ├── query_darwin.go    # macOS: F_LOG2PHYS_EXT (libSystem), clone ID fallback
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
├── testtools/                 # Manual platform diagnostics (not part of the CLI)
│   └── extentdump/
│       └── main.go            # extentdump <file>...: identity + extents + shared flag
├── testscripts/               # Manual CoW probes for real APFS/ReFS machines
│   ├── build-bundles.sh       # cross-compile finddupe + extentdump into bin/cow-test/
│   ├── cow-probe.sh           # macOS/Linux bash probe
│   ├── cow-probe-windows.ps1  # Windows PowerShell probe
│   └── README.md              # how to build/run the probes and read the numbers
├── bin/                       # Build output (gitignored); cow-test bundles land here
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

**Key fields**: `Action`, `Paths`, `RefPaths`, `Threads`, `Verbose`, `ListLink`, `CoWDetect`, `ShowProgress`, `FollowSymlinks`, `IncludeZeroLen`, `IncludeReadonly`, `SkipHardlinked`

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
func (d *Detector) CoWGroups() [][]FileInfo
func (d *Detector) Len() int
func (d *Detector) Stats() *Stats
```

- `Insert` — checksum-based insertion; returns the work a new file triggers.
- `OnHashDone` / `OnCompareDone` — feed executor outcomes back in and return follow-up work.
- `InsertInode` / `InodeGroups` — `(Dev, Inode)` hardlink index used by `find --listlink`.
- `CoWGroups` — used only in CoW-detect mode (`find --cow`), after the input is drained: every SHA-256 bucket with at least two distinct physical files becomes one group, keeping one path per `(Dev, Inode)` so hardlinked aliases collapse to a single member. It emits no per-pair work.

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

// NoMatchError travels Err when a pattern matched nothing (see below).
type NoMatchError struct{ Pattern string }
```

**Platform helpers**: `getFileIdentity(path, info)` in `walker_unix.go` / `walker_windows.go`.

**Features**: `**` recursive glob matching, literal paths (a pattern that names an existing path is scanned as written, so `/data/[2020] photos` is not split on its brackets), symlink following control (with loop prevention), zero-length file filtering, `ZeroLenCounter` callback, and no-match reporting: a pattern that matches no usable file yields one `*NoMatchError` on the channel, which the pipeline turns into a failed run. Only regular files are reported: symlinks are skipped unless `FollowSymlinks` is set (then they are resolved and reported with the target's size/identity, and links to directories are walked through the `seen` set), and devices/FIFOs/sockets are always ignored so nothing blocks on an open.

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
    ModTime   time.Time
}

func Compute(path string, size int64) (uint64, error)
func ComputeFileInfo(path string, size int64) (Info, error)
func ComputeFromReader(r io.Reader, size int64) (uint64, error)
```

**Platform-specific**:
- `inode_unix.go`: `fileIdentity(f *os.File)` reads `Dev`/`Inode`/`NumLinks` from `f.Stat().Sys().(*syscall.Stat_t)`
- `inode_windows.go`: `fileIdentity(f *os.File)` calls `GetFileInformationByHandle` on `f.Fd()`

`ComputeFileInfo` is the primary function used by the pipeline — it opens the file once and returns all metadata, keeping I/O in the parallel worker-pool path. For files ≤ 32KB it also returns the full SHA-256 at no extra I/O cost. It stats the open file and returns `dupe.ErrFileChanged` when its size is no longer the size the walker reported, so a file that grew or shrank cannot be signed as if it had the scanned content.

**Dependencies**: `internal/dupe` (the `ErrFileChanged` sentinel), stdlib, platform syscalls

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
    Kind       dupe.ExecutionType
    Key        dupe.GroupKey
    Files      []dupe.FileInfo
    Result     Result
    Err        error   // failure behind ResultError, reported by the coordinator
    FileShared []int64 // CoWDetect: already-shared bytes per Files entry
}
```

**Behavior by execution type**:
- `HashCalc`: full SHA-256 of one file, resuming from `HashState`/`HashOffset`.
- `HashComp`: chunked comparison of two files with early-stop; partial state is preserved for resume.
- `DupeElim`: delete / hardlink / CoW-clone the victim, or report it. Before anything else it refuses to act on the same physical file (`samePhysicalFile`: same non-zero `Dev` + `Inode`): report mode still reports the pair unless `SkipHardlinked` (`--hardlink`), every other action returns `ResultAlreadyHardlinked`. Reference victims are never eliminated. Before a destructive action it re-checks that both files still match the size and modification time recorded with their hash (`ResultSkippedChanged` otherwise), and a hardlink pair on different devices is refused (`ResultSkippedCrossDevice`).
- `CoWDetect`: query every member of one identical-content group and report `FileShared`, one already-shared byte count per member. The Linux `FIEMAP_EXTENT_SHARED` flag is used when any group extent carries it, otherwise physical-start identity is compared within the same device; `FileShared` is nil when extents are unavailable for the whole group.

**Link/clone helpers**: `linkReplace` (hardlink under a temporary name, then atomic rename), `hardlinkLimitReached` (per OS: re-reads the link count on Windows, defers to the filesystem on Unix), `cloneReplace` (shared orchestration), `alreadyShared` (same physical file or, on the same device, `extent.Equal` → `ResultAlreadyShared`), `copyTailAt` (the unaligned tail of a block clone), `clonePlatformFile` (per OS), `replaceFile` (per OS), `preserveMetadata` (per OS: restores as much of the victim's metadata as the platform allows), `ErrCoWNotSupported`.

**Action results**: `ResultVerifiedDuplicate`, `ResultAlreadyHardlinked`, `ResultDeleted`, `ResultHardlinked`, `ResultCoWCloned`, `ResultSkippedRO`, `ResultSkippedRef`, `ResultSkippedChanged`, `ResultSkippedCrossDevice`, `ResultHardlinkLimit`, `ResultNotDuplicate`, `ResultError`, `ResultAlreadyShared`

**Dependencies**: `internal/dupe`, `internal/config`, `internal/extent`, `golang.org/x/sys` (CoW ioctls)

### `internal/progress` — Progress Line

**Purpose**: draw a live "Scanned N files..." line on stderr while the scan runs.

**API**: `New(stats) *Reporter`, `(*Reporter).Run(ctx)`, and
`IsTerminal(f *os.File) bool` — a dependency-free terminal check (character
device). The pipeline only starts the reporter when stderr is a terminal and
stops it, waiting for the line to be cleared, before printing the summary, so
redirected output never collects escape sequences and no stale progress line
sits next to a result.

### `internal/volinfo` — Volume Facts

**Purpose**: report filesystem-level facts about the volume a path lives on, so
the CoW clone and the extent query share one implementation and one cache.

**API**: `ClusterSize(path string) (uint64, error)` — the volume's allocation
unit, cached per volume root. Windows-only: other platforms return
`ErrUnsupported`, which callers translate into "CoW/extents are unavailable
here" rather than a hard failure.

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
func Equal(a, b []Extent) bool
func SharedFlagBytes(e []Extent) int64
func SharedWithOthers(own []Extent, others [][]Extent) int64
func Supported() bool
```

**Platform implementations**: Linux FIEMAP (`FS_IOC_FIEMAP`), macOS `fcntl(F_LOG2PHYS_EXT)` through the libSystem wrapper (`unix.FcntlInt`) with a `getattrlist(ATTR_CMNEXT_CLONEID)` fallback for decmpfs-compressed files, Windows `FSCTL_GET_RETRIEVAL_POINTERS`; other platforms return `ErrUnsupported`.

**Helpers**: `SharedBytes` is the pairwise physical-overlap helper (falling back to shared logical ranges for compressed btrfs). `Equal` is the conservative already-sharing fast path for `dedupe --cow`. `SharedFlagBytes` sums extents the filesystem marked `Shared` (a per-file signal). `SharedWithOthers` sums in-group physical-start matches, capped to the shorter extent, for filesystems without a shared flag.

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
- `cowGroupExecutions` — the one-shot final batch for `find --cow`: `detector.CoWGroups()` → one `CoWDetect` execution per identical-content group, dispatched after the input is drained and all hashing has finished.
- `runExecutor` — the per-worker executor loop.
- `reportOutcome` / `reportElimination` / `reportCoW` — user-facing output and stats. `reportCoW` prints one `CoW candidate group (N files, identical content):` block with a `shared: X% (Y of Z)` line per member, or the members only when `FileShared` is nil.
- `printSummary` — final statistics, including `N CoW groups found (X of file bytes already shared)`.

**Dependencies**: All other `internal/` packages

### `internal/progress` — Progress Display

**Purpose**: Display live-updating scan progress via ANSI escape codes.

**Dependencies**: `internal/dupe` (for `Stats`)

## Diagnostic Tooling (not part of the binary)

`find --cow` relies on platform extent APIs that cannot be validated on a
developer's Linux machine alone. Two directories exist for that:

### `testtools/extentdump`

A standalone CLI, built separately from the main binary:

```bash
go build -o extentdump ./testtools/extentdump
extentdump file1 file2
```

It prints `extent query supported=<bool>`, then for each file its
`dev`/`inode`/`numLinks` (through `checksum.ComputeFileInfo`) and every extent's
`logical`/`physical`/`length`/`shared`/`encoded`, followed by `sharedFlagBytes`
and the total logical bytes. This is exactly what finddupe sees on that platform.

### `testscripts/`

- `build-bundles.sh` cross-compiles `finddupe` + `extentdump` for
  linux-amd64, darwin-amd64/arm64, and windows-amd64/arm64 into
  `bin/cow-test/<platform>/`, adds the matching probe script and README, and
  zips or tars each bundle. `make cow-test-bundles` runs it.
- `cow-probe.sh` (macOS/Linux bash) and `cow-probe-windows.ps1` (Windows
  PowerShell) exercise independent copies, `dedupe --cow` plus a second run, a
  pre-existing clone, an existing hardlink (which must stay untouched), and
  compressible content. Linux uses `cp --reflink=never` for "independent" copies
  because plain `cp` reflinks on btrfs; macOS uses `cp -c` for a real clone.
- `README.md` explains how to run the bundles on APFS/ReFS and what the numbers
  mean.

The bundles themselves are build output under the gitignored `bin/` directory;
the committed sources are the probe scripts and the `extentdump` command.

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
