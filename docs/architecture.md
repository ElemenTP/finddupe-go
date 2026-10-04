# Architecture

## Overview

finddupe-go uses a **coordinator/executor pipeline**. A shared worker pool performs parallel file I/O and checksum computation, while a single **coordinator** goroutine owns all duplicate state in the `dupe.Detector` state machine. The coordinator turns executor completions into follow-up work, and a pool of stateless **executor** goroutines runs the resulting `dupe.Execution` items.

This replaces the older design in which the detector and executor were each a single pipeline stage: duplicate state still has a single writer (the coordinator), but hashing and comparison now run in parallel across N executor workers.

## Pipeline Topology

```
                    ┌────────────────────────────────────────────────────┐
                    │                pipeline.Run                        │
                    │  - Parse CLI → Config                              │
                    │  - Create channels, worker pool, detector, executor│
                    │  - SIGINT/SIGTERM → cancel ctx                     │
                    │  - Print final summary                             │
                    └────────────────────────────────────────────────────┘
                                        │
        ┌───────────────────────────────┼───────────────────────────────┐
        ▼                               ▼                               ▼
┌───────────────────────┐   ┌───────────────────────┐   ┌───────────────────────┐
│ walkAll (1 goroutine) │   │ Progress (1 goroutine)│   │ Signal handler        │
│                       │   │                       │   │                       │
│ Walk(RefPaths) first  │   │ Reads dupe.Stats      │   │ SIGINT/SIGTERM →      │
│   (IsRef = true)      │   │ Prints every 500ms    │   │   cancel()            │
│ then Walk(Paths)      │   │ via ANSI escape codes │   │                       │
│ → walkResultCh        │   │                       │   │                       │
└───────────┬───────────┘   └───────────────────────┘   └───────────────────────┘
            │
            ▼ walkResultCh (buffered, threads*4)
┌───────────────────────────────────────────────────────────┐
│        scanChecksums (1 feeder goroutine)                 │
│                                                           │
│ For each Result from the walker:                          │
│   1. Check for errors → log, count CantReadFiles          │
│   2. Drain the whole reference phase (pending.Wait())     │
│      before submitting the first normal file              │
│   3. pool.Submit(checksum.ComputeFileInfo)                │
│ → fileInfoCh                                              │
└───────────────────────────┬───────────────────────────────┘
                            │
                            ▼ worker pool (N = threads goroutines)
┌───────────────────────────────────────────────────────────┐
│  checksum.ComputeFileInfo(path, size)                     │
│    → opens the file once                                  │
│    → reads 32KB, computes CRC + sum → Signature           │
│    → fileIdentity(f) → Dev, Inode, NumLinks               │
│    → SHA-256 at zero extra cost for files ≤ 32KB          │
│  Set fi fields, send FileInfo on fileInfoCh               │
└───────────────────────────┬───────────────────────────────┘
                            │
                            ▼ fileInfoCh (buffered, threads*4)
┌───────────────────────────────────────────────────────────┐
│         coordinator (1 goroutine) — owns Detector         │
│                                                           │
│ On FileInfo:                                              │
│   detector.Insert(fi) → []Execution → executionCh         │
│                                                           │
│ On Outcome from outcomeCh:                                │
│   reportOutcome(...)                                      │
│   HashCalc → detector.OnHashDone(key, fi)                 │
│   HashComp → detector.OnCompareDone(key, a, b)            │
│   DupeElim / CoWDetect → nothing further                  │
│   → follow-up Executions → executionCh                    │
│                                                           │
│ CoW-detect mode: input drained && inFlight == 0           │
│   → detector.CoWGroups() → one CoWDetect per group        │
│                                                           │
│ Termination: input drained && inFlight == 0               │
│   → close(executionCh)                                    │
└───────────┬───────────────────────────────────────────────┘
            │
            ▼ executionCh (buffered, threads*4)
┌───────────────────────────────────────────────────────────┐
│      executor workers (N = threads goroutines)            │
│                                                           │
│ action.Executor.DoExecution(ctx, ex) → Outcome            │
│   HashCalc: full SHA-256 (resume from HashState/Offset)   │
│   HashComp: chunked compare with early-stop               │
│   DupeElim: delete / hardlink / CoW clone / report        │
│   CoWDetect: per-file shared bytes for one content group  │
│ → outcomeCh                                               │
└───────────────────────────┬───────────────────────────────┘
                            │
                            ▼ outcomeCh (buffered, threads*4) → coordinator
```

## Goroutine Responsibilities

| Goroutine | Count | Input | Output | Notes |
|-----------|-------|-------|--------|-------|
| Walker | 1 | `Config.RefPaths` then `Config.Paths` | walkResultCh | I/O bound, sequential. Stat only — no file opens |
| Scanner feeder | 1 | walkResultCh | pool.Submit → fileInfoCh | Drains the reference phase before normal files |
| Worker pool | N (`--threads`) | pool.Submit | fileInfoCh | Opens files, computes checksums and `(Dev, Inode, NumLinks)` |
| Coordinator | 1 | fileInfoCh, outcomeCh | executionCh, stats | Sole owner/writer of the `Detector`; decides termination |
| Executor | N (`--threads`) | executionCh | outcomeCh | Stateless; hashing, comparison, elimination, CoW detection |
| Progress | 1 | stats (atomics) | stderr | Runs on a ticker, no channel |
| Signal handler | 1 | OS signals | cancel() | `signal.NotifyContext` side channel |

## Channel Buffer Sizing

| Channel | Size | Rationale |
|---------|------|-----------|
| walkResultCh | threads * 4 | Decouple the sequential walker from the scanner feeder |
| fileInfoCh | threads * 4 | Decouple the worker pool from the coordinator |
| executionCh | threads * 4 | Decouple the coordinator from the executor workers |
| outcomeCh | threads * 4 | Decouple executor completion from coordinator feedback |

All buffers use the same `channelBufferFactor = 4`. There is no error channel: per-file errors are logged and counted, and cancellation propagates through `context.Context`.

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
  ├── fswalker.Walk(RefPaths) → chan fswalker.Result        │
  │     (IsRef = true; walked and drained first)            │
  ├── fswalker.Walk(Paths)    → chan fswalker.Result        │
  │     │                                                   │
  │     ▼ (per file, parallel in pool)                      │
  │   checksum.ComputeFileInfo(path, size) → checksum.Info  │
  │     ├── os.Open (once per file)                         │
  │     ├── Read 32KB → CRC + sum → Signature               │
  │     ├── fileIdentity(f) → Dev, Inode, NumLinks          │
  │     └── SHA-256 for files ≤ 32KB                        │
  │     │                                                   │
  │     ▼                                                   │
  │   dupe.FileInfo{Path, Size, Signature, SHA256,          │
  │                 Dev, Inode, NumLinks, IsRef}            │
  │     │                                                   │
  │     ▼                                                   │
  │   coordinator: detector.Insert(fi) → []Execution        │
  │     │                                                   │
  │     ├── nil: stored in (GroupKey → SHA-256) buckets     │
  │     ├── HashComp: exactly two unhashed files            │
  │     ├── HashCalc: 3+ files, every unhashed file         │
  │     ├── DupeElim: identical hash, keeper + victim       │
  │     └── CoWDetect: none per pair; emitted as one        │
  │           execution per group after input drain         │
  │           │                                             │
  │           ▼                                             │
  │   executor workers: action.Executor.DoExecution         │
  │           │                                             │
  │           ├── HashCalc / HashComp → Outcome →           │
  │           │     coordinator → OnHashDone/OnCompareDone  │
  │           │                                             │
  │           └── DupeElim → report / act:                  │
  │                 ├── report:  "Duplicate: / With:"       │
  │                 ├── delete:  os.Remove                  │
  │                 ├── hardlink: os.Remove + os.Link       │
  │                 └── cow:      cloneReplace (FICLONE/…)  │
  │                                                         │
  │   CoWDetect → extent.Query + SharedFlagBytes/           │
  │              SharedWithOthers → "shared: N%" per member │
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
        │ Execute  │  │ Config     │  │ Config +   │
        │          │  │            │  │ validation │
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
                    │    runListLink(...)           │
                    │    coordinate(...)            │
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
  │ - ** matcher    │ │ - ComputeFile│ │ - Execution│ │ - Outcome    │
  │ - getFileIdentity│ │   Info()    │ │ - FileInfo │ │ - Delete     │
  │   (per OS)      │ │ - fileIdentity│ │ - Stats    │ │ - Hardlink   │
  │                 │ │   (per OS)   │ │ - InodeKey │ │ - CoW clone  │
  └─────────────────┘ └──────────────┘ └────────────┘ │   (per OS)   │
                                                      │ - CoW detect │
                                                      └──────┬───────┘
                            ┌──────────────┐                 │
                            │ internal/    │        ┌────────▼───────┐
                            │ worker       │        │ internal/      │
                            │ Pool         │        │ extent         │
                            └──────────────┘        │ Query/Equal/   │
                            ┌──────────────┐        │ Shared* (OS)   │
                            │ internal/    │        └────────────────┘
                            │ progress     │
                            │ Reporter     │
                            └──────────────┘
```

## Key Design Decisions

### 1. Two-Level Hash Map instead of a Binary Search Tree

The C version uses a binary search tree for storing file signatures. The Go version uses `map[GroupKey]map[[32]byte][]FileInfo`:

- The **outer** map is keyed by the composite `(Signature, Size)`, which is the weak-checksum candidate set.
- The **inner** map is keyed by the full SHA-256; the all-zero key holds files whose full hash is not yet known (possibly a partial state).
- The **first** file in each SHA-256 bucket is the keeper and is never a victim.

For collision handling (different files with the same weak checksum), the bucket serves as the same chain. The composite key removes size-wrap collisions that a signature-only key would keep together.

### 2. Single File-Open Per Scan

Files are opened **once** in the worker-pool goroutines via `checksum.ComputeFileInfo`. It returns a `checksum.Info{Signature, Dev, Inode, NumLinks, SHA256}` from one `os.Open`. On Windows, `GetFileInformationByHandle` is called on the same handle — avoiding a second `CreateFile` that would serialize I/O in the single-threaded walker. Files ≤ 32KB also get their SHA-256 for free because the whole file is already in the CRC buffer.

### 3. Checksum Computation as the Parallelization Point

Reading 32KB + CRC + `(Dev, Inode, NumLinks)` retrieval is the most I/O-intensive per-file operation. The worker pool parallelizes this stage, while the walker (stat only) stays sequential.

### 4. Detector as a Pure State Machine

`dupe.Detector` performs no I/O. Its methods take a file or an outcome and return the next `[]Execution`; all mutation happens under one mutex. This makes the duplicate state trivially race-free: only the coordinator goroutine ever calls the detector. Executors are stateless and can therefore run in parallel without sharing duplicate state.

### 5. Work Strategy by Group Size

- **1 file**: store, no hashing (cheapest possible path for unique files).
- **Exactly 2 unhashed files**: one `HashComp` compares them in chunks and stops at the first difference, saving partial hash state. This avoids hashing two full files when they are usually different.
- **3+ files**: `HashCalc` for every unhashed file without a task in flight, then O(1) matching against completed SHA-256 buckets. This is what guarantees that all of N identical files are reported (N−1 eliminations).

### 6. Reference Files Are Walked First (and Never Eliminated)

`walkAll` walks `RefPaths` before `Paths` and marks them `IsRef`; `scanChecksums` drains the entire reference phase before submitting any normal file. Reference files are therefore inserted first and become the keeper of their content group. In addition, the executor refuses to eliminate a reference victim (`ResultSkippedRef`) unless the action is `ActionReport`, so a reference file can never be deleted, hardlinked, or cloned.

### 7. Cancel-on-Signal

The pipeline listens for SIGINT and SIGTERM via `signal.NotifyContext`. On signal the context is cancelled, which propagates through all goroutines via `ctx.Done()`. Each stage checks the context before processing the next item, and the coordinator closes `executionCh`, which lets the executor workers exit and close `outcomeCh`.

### 8. CoW Detection as One Final Group Pass

`find --cow` does not emit per-pair work while files stream in. Instead the detector
keeps every identical file in its SHA-256 bucket, and only after the input is drained
and `inFlight == 0` does the coordinator call `detector.CoWGroups()` and dispatch one
`CoWDetect` execution per group (`cowGroupExecutions`). Grouping at the end means a
file cannot be grouped before its hash was compared, and it reports the whole
identical-content set at once: one path per `(Dev, Inode)` (hardlinked aliases
collapse) with a per-member already-shared byte count. Independent copies are
members too, at 0% shared, because they are precisely the files that should CoW-share.

## Termination

The coordinator owns the termination condition:

1. `fileInfoCh` closes when the walker and the checksum workers are done.
2. The coordinator tracks `inFlight` (dispatched executions not yet completed).
3. Once the input is drained **and** `inFlight == 0`, it runs the one-shot final
   batch: in CoW-detect mode, `detector.CoWGroups()` produces one `CoWDetect`
   execution per identical-content group (`cowGroupExecutions`). Only then is the
   detector state final, so the grouping cannot miss a file that is still hashing.
4. It closes `executionCh`.
5. The executor workers observe the closed channel and exit; a `sync.WaitGroup` then closes `outcomeCh`.
6. The coordinator drains the remaining outcomes and returns.

## Error Handling Strategy

- **Per-file errors** (e.g., can't read a file): logged, counted in stats (`CantReadFiles`), processing continues.
- **Action errors** (e.g., can't delete a file): reported as `ResultError`, logged, counted, processing continues.
- **Hash/compare errors**: logged in `runExecutor` (DupeElim errors are reported by `reportElimination` instead).
- **Context cancellation**: all goroutines exit and `pipeline.Run` returns `ctx.Err()`.
- **`--listlink`** takes a separate, simpler path (`runListLink`) that skips duplicate detection entirely.
