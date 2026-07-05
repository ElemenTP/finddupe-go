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
│   ├── dupe/                  # Core types and duplicate detection
│   │   ├── fileinfo.go        # FileInfo, DupeGroup types
│   │   ├── detector.go        # Hash map-based duplicate detector
│   │   └── stats.go           # Thread-safe statistics (+ ZeroLenCounter)
│   ├── fswalker/              # Filesystem traversal
│   │   ├── walker.go          # Walker interface + implementation
│   │   ├── walker_unix.go     # Unix inode from stat
│   │   └── walker_windows.go  # Windows inode stub (0,0 — done in checksum)
│   ├── checksum/              # File signature + inode computation
│   │   ├── checksum.go        # Compute, ComputeFileInfo, ComputeFromReader
│   │   ├── inode_unix.go      # Unix: inode from f.Stat().Sys()
│   │   └── inode_windows.go   # Windows: inode from GetFileInformationByHandle
│   ├── action/                # Duplicate elimination actions
│   │   ├── executor.go        # Executor, VerifyAndExecute, VerifyFullFile
│   │   ├── delete.go          # File deletion (+ readonly handling)
│   │   ├── hardlink.go        # Cross-platform hardlink creation
│   │   ├── hardlink_unix.go   # Unix: os.Link
│   │   ├── hardlink_windows.go# Windows: os.Link + link limit
│   │   ├── cow.go             # CoW clone (stub — returns ErrCoWNotSupported)
│   │   ├── cow_unix.go        # Unix CoW stub
│   │   ├── cow_windows.go     # Windows CoW stub
│   ├── worker/                # Worker pool
│   │   └── pool.go            # Bounded goroutine pool
│   ├── pipeline/              # Orchestration
│   │   └── pipeline.go        # Run, runNormalMode, printSummary
│   └── progress/              # Progress display
│       └── progress.go        # ANSI progress reporter
├── test/                      # System integration tests
│   └── system_test.go         # 46 system tests covering find/dedupe/edge cases
└── docs/                      # Documentation (this directory)
```

## Package Responsibilities

### `cmd/` — CLI Layer

**Purpose**: Parse command-line arguments, validate them, construct a `config.Config`, and invoke `pipeline.Run()`.

- `root.go`: Root cobra command. `Execute()` called from `main.go`.
- `find.go`: `finddupe find` — defines flags for find mode (all wired to Config).
- `dedupe.go`: `finddupe dedupe` — defines flags for dedupe mode, validates action flags.
- `version.go`: `finddupe version` — version, build time, platform, Go version.

**Dependencies**: `github.com/spf13/cobra`, `internal/config`, `internal/pipeline`

### `internal/config` — Configuration

**Purpose**: Define the `Config` struct and related types. Pure data — no behavior.

**Key fields**: `Mode`, `Action`, `Paths`, `RefPaths`, `Threads`, `Verbose`, `PrintSigs`, `ShowProgress`, `FollowSymlinks`, `IncludeZeroLen`, `IncludeReadonly`, `SkipHardlinked`

**Dependencies**: None (std types only)

### `internal/dupe` — Core Types and Detection

**Purpose**: Define fundamental data types and implement the duplicate detection algorithm.

**Types exported**: `FileInfo`, `DupeGroup`, `Stats`, `Detector`

**Detector API**:
- `Insert(fi FileInfo) []DupeGroup` — checksum-based insertion
- `InsertHardlink(fi FileInfo) bool` — inode-based grouping (reserved)
- `HardlinkGroups() [][]FileInfo` — returns all inode groups (reserved)
- `Len() int` — number of unique signatures
- `Stats() *Stats` — statistics accessor

**Dependencies**: None (stdlib only)

### `internal/fswalker` — Filesystem Walker

**Purpose**: Walk directories matching glob patterns. On Unix, also retrieves inode/link info from stat. On Windows, inode retrieval is deferred to the parallel checksum path.

**Interface**:
```go
type Walker struct{}
func New() *Walker
func (w *Walker) Walk(ctx, patterns, opts) <-chan Result
```

**Features**: `**` recursive glob matching, symlink following control, zero-length file filtering, `ZeroLen` counter callback.

**Dependencies**: `internal/dupe` (for `FileInfo`)

### `internal/checksum` — Checksum + Inode Computation

**Purpose**: Compute 64-bit composite file signature AND retrieve filesystem inode/link info from a single file-open call.

**Public API**:
```go
const BytesToChecksum = 32768

func Compute(path string, size int64) (uint64, error)
func ComputeFileInfo(path string, size int64) (sig uint64, inode uint64, numLinks uint64, err error)
func ComputeFromReader(r io.Reader, size int64) (uint64, error)
```

**Platform-specific**:
- `inode_unix.go`: `fileInode(f *os.File)` reads inode from `f.Stat().Sys().(*syscall.Stat_t)`
- `inode_windows.go`: `fileInode(f *os.File)` calls `GetFileInformationByHandle` on `f.Fd()`

`ComputeFileInfo` is the primary function used by the pipeline — it opens the file once and returns all metadata, keeping I/O in the parallel worker pool path.

**Dependencies**: None (stdlib + platform syscalls)

### `internal/action` — Action Executor

**Purpose**: Verify duplicates (full byte comparison) and execute elimination actions.

**API**:
```go
type Executor struct{ ... }
func New(opts Options) *Executor
func (e *Executor) VerifyAndExecute(ctx, group) (Result, error)
func VerifyFullFile(pathA, pathB string, expectedSize int64) (bool, error)
```

**Verification flow**:
1. If `SkipHardlinked`: check `Original.Inode == Candidate.Inode` → skip if same
2. `VerifyFullFile`: full byte comparison in 64KB chunks
3. If confirmed: execute action or report

**Action results**: `ResultVerifiedDuplicate`, `ResultAlreadyHardlinked`, `ResultDeleted`, `ResultHardlinked`, `ResultCoWCloned`, `ResultSkippedRO`, `ResultSkippedRef`, `ResultHardlinkLimit`, `ResultNotDuplicate`, `ResultError`

**Dependencies**: `internal/dupe`, `internal/config`

### `internal/worker` — Worker Pool

**Purpose**: Manage a bounded set of goroutines for parallel file operations.

**API**:
```go
type Pool struct { ... }
func New(size int) *Pool
func (p *Pool) Submit(ctx context.Context, fn func(context.Context))
func (p *Pool) Wait()
```

**Dependencies**: None (stdlib only)

### `internal/pipeline` — Orchestration

**Purpose**: Wire all components together, manage goroutine lifecycle, handle signals, format output.

**API**:
```go
func Run(ctx context.Context, cfg *config.Config) error
```

**Internal**: `runNormalMode` spawns and connects all pipeline goroutines. `printSummary` prints final statistics.

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
      │    └── internal/config
      ├── internal/worker
      └── internal/progress
           └── internal/dupe
```

## Interface Design Principles

1. **Accept interfaces, return structs**: Packages define interfaces for their dependencies (e.g., `fswalker.ZeroLenCounter`) but return concrete types
2. **Channels for data flow**: Producer-consumer stages communicate via channels with explicit direction in function signatures
3. **Context for cancellation**: Every long-running operation accepts `context.Context`
4. **Constructor functions**: Types use `New*` constructors; struct literals used only where allowed by exhaustruct lint
5. **No global state**: All state flows through the pipeline; stats use atomics for lock-free concurrent access
