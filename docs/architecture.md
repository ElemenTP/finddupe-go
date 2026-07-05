# Architecture

## Overview

finddupe-go uses a **producer-consumer pipeline** architecture with a shared worker pool for parallel file I/O and checksum computation. This design decouples filesystem traversal, checksum computation, duplicate detection, and action execution into independent stages connected by buffered channels.

## Pipeline Topology

```
                    ┌──────────────────────────────────────────────┐
                    │              main goroutine                  │
                    │  - Parse CLI → Config                        │
                    │  - Create channels, pool, detector, stats    │
                    │  - Handle SIGINT/SIGTERM → cancel ctx        │
                    │  - Print final summary                       │
                    └──────────────────────────────────────────────┘
                                        │
                    ┌───────────────────┼───────────────────┐
                    ▼                   ▼                    ▼
┌───────────────────────┐ ┌───────────────────────┐ ┌───────────────────────┐
│   Walker (1 goroutine) │ │ Progress reporter (1) │ │  Signal handler (1)   │
│                       │ │                       │ │                       │
│ filepath.WalkDir      │ │ Reads dupe.Stats      │ │ SIGINT/SIGTERM →      │
│   + ** glob matching  │ │ Prints every 500ms    │ │   cancel()            │
│   + stat only         │ │ via ANSI escape codes │ │                       │
│   → walkResultCh      │ │                       │ │                       │
└───────────┬───────────┘ └───────────────────────┘ └───────────────────────┘
            │
            ▼ walkResultCh (buffered, threads*4)
┌───────────────────────────────────────────────────────────┐
│        Scanner Feeder (1 goroutine)                       │
│                                                           │
│ For each Result from walker:                              │
│   1. Check for errors → log, count                       │
│   2. pending.Add(1)                                       │
│   3. pool.Submit(checksumWork) ← blocks if pool full     │
│   4. On completion: pending.Wait(), close(fileCh)        │
└───────────────────────────┬───────────────────────────────┘
                            │
                            ▼
┌───────────────────────────────────────────────────────────┐
│           Worker Pool (N = threads goroutines)             │
│                                                           │
│ For each task:                                            │
│   1. checksum.ComputeFileInfo(path, size)                 │
│      → opens file once                                    │
│      → reads 32KB, computes CRC + sum                     │
│      → gets inode (Unix: f.Stat; Windows: GetFileInfo...) │
│   2. Set fi.Signature, fi.Inode, fi.NumLinks              │
│   3. Send FileInfo to fileCh                              │
└───────────────────────────┬───────────────────────────────┘
                            │
                            ▼ fileCh (buffered, threads*4)
┌───────────────────────────────────────────────────────────┐
│                Detector (1 goroutine)                      │
│                                                           │
│ Reads FileInfo from fileCh:                                │
│   1. detector.Insert(fi) → DupeGroup or nil               │
│   2. If DupeGroup → send to groupCh                       │
│   3. Update stats (TotalFiles, TotalBytes)                │
│                                                           │
│ Uses hash map (map[uint64][]FileInfo) keyed by checksum.  │
│ Collision chain via slice append for same-signature files.│
└───────────────────────────┬───────────────────────────────┘
                            │
                            ▼ groupCh (buffered, threads)
┌───────────────────────────────────────────────────────────┐
│              Executor (1 goroutine)                        │
│                                                           │
│ For each DupeGroup:                                        │
│   1. If SkipHardlinked: check inodes → skip if same       │
│   2. Full byte-by-byte verification (64KB chunks)         │
│   3. If confirmed duplicate:                               │
│      - find mode:  print group, update stats              │
│      - dedupe mode: execute action (delete/hardlink/CoW)  │
│   4. If false positive: add to collision chain            │
└───────────────────────────────────────────────────────────┘
```

## Goroutine Responsibilities

| Goroutine | Count | Input | Output | Notes |
|-----------|-------|-------|--------|-------|
| Walker | 1 | Config.Paths | walkResultCh | I/O bound, sequential. Stat only — no file opens |
| Scanner feeder | 1 | walkResultCh | pool.Submit | Lightweight; feeds tasks to pool |
| Worker pool | N (--threads) | pool.Submit | fileCh | CPU+I/O bound. Opens files, computes checksums, gets inodes |
| Detector | 1 | fileCh | groupCh | Sequential for correctness, hash map O(1) |
| Executor | 1 | groupCh | stats | I/O bound (full file compare + action) |
| Progress | 1 | stats (atomics) | stdout | Runs on ticker, no channel |
| Signal handler | 1 | OS signals | cancel() | Side channel |

## Channel Buffer Sizing

| Channel | Size | Rationale |
|---------|------|-----------|
| walkResultCh | threads * 4 | Decouple walker from scanner feeder |
| fileCh | threads * 4 | Decouple worker pool from detector |
| groupCh | threads | Detector is much faster than executor |
| errCh | 1 | Only first fatal error matters |

## Data Flow

```
CLI args
  │
  ▼
config.Config ──────────────────────────────────────────────┐
  │                                                         │
  ▼                                                         │
pipeline.Run(ctx, cfg)                                      │
  │                                                         │
  ├── fswalker.Walk(paths, opts) → chan fswalker.Result     │
  │     │                                                   │
  │     ▼ (per file, sequential)                            │
  │   os.Stat → FileInfo{Path, Size}                        │
  │     │                                                   │
  │     ▼ (per file, parallel in pool)                      │
  │   checksum.ComputeFileInfo(path, size)                  │
  │     ├── os.Open (once per file)                         │
  │     ├── Read 32KB → CRC + sum → uint64                  │
  │     └── fileInode(f) → inode, numLinks                  │
  │     │                                                   │
  │     ▼                                                   │
  │   dupe.FileInfo{Path, Size, Signature, Inode, NumLinks} │
  │     │                                                   │
  │     ▼                                                   │
  │   dupe.Detector.Insert(fi)                              │
  │     │                                                   │
  │     ├── nil: stored in hash map (first occurrence)      │
  │     │                                                   │
  │     └── DupeGroup: checksum collision detected          │
  │           │                                             │
  │           ▼                                             │
  │   action.Executor.VerifyAndExecute(group)               │
  │           │                                             │
  │           ├── SkipHardlinked check (same inode?)        │
  │           │                                             │
  │           ├── false positive → add to collision chain   │
  │           │                                             │
  │           └── confirmed duplicate                       │
  │                 │                                       │
  │                 ├── find mode: print to stderr          │
  │                 │                                       │
  │                 └── dedupe mode:                        │
  │                       ├── delete:  os.Remove            │
  │                       ├── hardlink: os.Remove + os.Link │
  │                       └── cow:      unsupported (stub)  │
  │                                                         │
  ▼                                                         │
Final summary (stats + results) printed to stderr           │
```

## Component Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                         main.go                             │
│                       cmd.Execute()                         │
└─────────────────────────────┬───────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
        ┌──────────┐  ┌────────────┐  ┌────────────┐
        │ root.go  │  │  find.go   │  │ dedupe.go  │
        │          │  │            │  │            │
        │ Version  │  │ Flags      │  │ Flags      │
        │ Execute  │  │ Config     │  │ Config     │
        └──────────┘  └─────┬──────┘  └─────┬──────┘
                            │               │
                            └───────┬───────┘
                                    │
                                    ▼
                    ┌───────────────────────────────┐
                    │    internal/config            │
                    │    Config struct              │
                    └───────────────┬───────────────┘
                                    │
                                    ▼
                    ┌───────────────────────────────┐
                    │    internal/pipeline          │
                    │    Run(ctx, cfg) error        │
                    │    runNormalMode(...)         │
                    │    printSummary(...)          │
                    └───┬───────┬───────┬───────┬───┘
                        │       │       │       │
            ┌───────────┘       │       │       └──────────────┐
            ▼                   ▼       ▼                      ▼
  ┌─────────────────┐ ┌──────────────┐ ┌────────────┐ ┌──────────────┐
  │ internal/       │ │ internal/    │ │ internal/  │ │ internal/    │
  │ fswalker        │ │ checksum     │ │ dupe       │ │ action       │
  │                 │ │              │ │            │ │              │
  │ - Walk()        │ │ - Compute()  │ │ - Detector │ │ - Executor   │
  │ - ** matcher    │ │ - ComputeFile│ │ - Stats    │ │ - Delete     │
  │ - inode (unix)  │ │   Info()     │ │ - FileInfo │ │ - Hardlink   │
  │                 │ │ - fileInode  │ │ - DupeGroup│ │ - CoW (stub) │
  └─────────────────┘ │   (per OS)   │ └────────────┘ │ -       │
                       └──────────────┘                └──────────────┘
                            ┌──────────────┐
                            │ internal/    │
                            │ worker       │
                            │ Pool         │
                            └──────────────┘
                            ┌──────────────┐
                            │ internal/    │
                            │ progress     │
                            │ Reporter     │
                            └──────────────┘
```

## Key Design Decisions

### 1. Hash Map over Binary Search Tree

The C version uses a binary search tree for storing file signatures. The Go version uses `map[uint64][]FileInfo` keyed by the 64-bit checksum. This is:
- **Simpler**: ~30 lines vs ~100 lines of tree traversal code
- **Faster**: O(1) average lookup vs O(log n)
- **More idiomatic**: Go's hash map is highly optimized

For collision handling (different files with the same checksum), the slice value in the map serves as the "same chain" — identical semantics to the C version's `Same` pointer.

### 2. Single File-Open Per Scan

Files are opened **once** in the worker pool goroutines via `checksum.ComputeFileInfo`. This function returns the checksum signature, inode, and hardlink count from a single `os.Open` call. On Windows, `GetFileInformationByHandle` is called on the same handle — avoiding a second `CreateFile` that would serialize I/O in the single-threaded walker.

### 3. Checksum Computation as the Parallelization Point

The checksum computation (reading 32KB + CRC + inode retrieval) is the most I/O-intensive per-file operation. The worker pool parallelizes this stage, while the walker (stat only) and detector (hash map insert) remain sequential.

### 4. Cancel-on-Signal

The pipeline listens for SIGINT and SIGTERM. On signal, the context is cancelled, which propagates through all goroutines via `ctx.Done()`. Each stage checks context before processing the next item, enabling clean shutdown.

### 5. Single Uniform Pipeline

The pipeline always uses the same checksum-based duplicate detection path. There is no separate "hardlink search mode." Instead, the `--hardlink` flag in find mode enables a **pre-verification inode check** in the executor that skips already-hardlinked file pairs.

## Error Handling Strategy

- **Fatal errors** (e.g., can't create output file): sent on `errChan`, pipeline returns immediately
- **Per-file errors** (e.g., can't read a file): logged, counted in stats, processing continues
- **Action errors** (e.g., can't delete a file): logged, counted, processing continues
- **Context cancellation**: all goroutines exit, pipeline returns `ctx.Err()`
