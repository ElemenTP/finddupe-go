# Testing Strategy

## Overview

Two test suites complement each other:

- **Unit tests**: `internal/*/` — test individual packages in isolation
- **System tests**: `test/system_test.go` — test the compiled binary end-to-end against real filesystems

All tests run with the `-race` flag (see [Race Detection](#race-detection)).

## Test Requirements (from .golangci.yml)

- **`paralleltest`**: All test functions must call `t.Parallel()`
- **`testpackage`**: Tests use a separate `_test` package
- **`tparallel`**: Detects inappropriate `t.Parallel()` usage

`exhaustruct` is present in the config file but **disabled** (commented out in the enabled-linters list), so tests do not have to initialize every struct field.

## Unit Tests by Package

### `internal/checksum` (9 tests)

| Test | Description |
|------|-------------|
| `TestCompute_EmptyFile` | Empty file → signature 0 |
| `TestCompute_SmallFile` | File smaller than 32KB → non-zero signature |
| `TestCompute_Deterministic` | Same content → same signature |
| `TestCompute_DifferentFiles` | Different content → different signature |
| `TestCompute_FileSizeFoldedIn` | Same content, different reported sizes → different signatures |
| `TestCompute_LargeFile` | File larger than 32KB → only first 32KB used (same first 32KB collides) |
| `TestComputeFromReader` | Reader-based computation |
| `TestCompute_NonExistentFile` | Non-existent path → error |
| `TestCompute_CRCAndSumComponents` | Signature packs crc and sum correctly |

### `internal/dupe` (Detector, 14 tests)

| Test | Description |
|------|-------------|
| `TestDetector_FirstInsert` | First file → no executions; stats updated |
| `TestDetector_TwoUnhashedFiles_EmitHashComp` | Exactly two unhashed files → one `HashComp` |
| `TestDetector_ThirdUnhashedFile_EmitHashCalc` | Third unhashed file → `HashCalc` for the new file only |
| `TestDetector_KnownShaMatch_EmitDupeElim` | Known matching SHA-256 → `DupeElim` keeper/victim |
| `TestDetector_KnownShaMismatch_NoExec` | Known different SHA-256 → no work |
| `TestDetector_ThreeIdenticalFiles_AllReported` | Regression: 4 identical files → exactly 3 eliminations, one keeper |
| `TestDetector_NoDoubleElimination` | A scheduled victim is never handed out twice |
| `TestDetector_PartialProgressPreserved` | Early-stop state carried into a later `HashCalc` |
| `TestDetector_PartialProgressKeepsLargerOffset` | The most advanced partial offset wins |
| `TestDetector_CRCCollisionSeparatesBuckets` | Different SHA-256 buckets → matched against the right keeper |
| `TestDetector_CoWDetectMode` | `WithCoWDetect()` emits no per-pair work and never schedules victims; `CoWGroups()` groups by SHA-256 and collapses hardlinked aliases |
| `TestDetector_InsertInodeGroups` | `InsertInode`/`InodeGroups` group by `(Dev, Inode)` |
| `TestDetector_Empty` | Zero state: `Len()==0`, no inode groups, non-nil stats |
| `TestDetector_SamePathInsertedTwice` | Inserting one path twice is ignored (overlapping patterns cannot self-eliminate) |

### `internal/action` (14 tests)

| Test | Description |
|------|-------------|
| `TestDoExecution_HashCalc_Complete` | Full-file SHA-256 for a multi-chunk file |
| `TestDoExecution_HashCalc_Resumes` | Hashing resumes from `HashState`/`HashOffset` |
| `TestDoExecution_HashComp_Identical` | Identical pair → both SHA-256 complete and equal |
| `TestDoExecution_HashComp_EarlyStop` | Differing chunks stop early and save partial state |
| `TestDoExecution_DupeElim_Delete` | Victim deleted, keeper preserved |
| `TestDoExecution_DupeElim_RefVictimSkipped` | Reference victim → `ResultSkippedRef`, file preserved |
| `TestDoExecution_DupeElim_SkipHardlinked` | Same `(Dev, Inode)` pair → `ResultAlreadyHardlinked` |
| `TestDoExecution_DupeElim_Report` | Report action → `ResultVerifiedDuplicate` |
| `TestDoExecution_DupeElim_Hardlink` | Victim replaced with a hardlink to the keeper |
| `TestDoExecution_DupeElim_ReadOnly` | Read-only victim skipped, then deleted with `IncludeReadonly` |
| `TestDoExecution_CoWClone` | Victim replaced with a CoW clone; skips when unsupported |
| `TestDoExecution_SamePhysicalFile_NoAction` | Delete/hardlink/CoW all return `ResultAlreadyHardlinked` for a hardlinked pair and leave the victim intact |
| `TestDoExecution_CoWClone_SkipsAlreadyShared` | A second clone of an already-shared pair is a no-op (`ResultAlreadyShared`) |
| `TestDoExecution_CoWDetect_GroupRatios` | `FileShared` for `[original, clone, independent copy]` is `[size, size, 0]` |

### `internal/fswalker` (11 tests)

| Test | Description |
|------|-------------|
| `TestWalk_EmptyDir` | Empty dir → no results |
| `TestWalk_SingleFile` | One file detected |
| `TestWalk_Recursive` | Nested dirs walked |
| `TestWalk_GlobExtension` | `*.txt` pattern filtering |
| `TestWalk_GlobStarRecursive` | `**/*.txt` recursive glob |
| `TestWalk_GlobStarAllJPG` | `**/*.jpg` in nested dirs |
| `TestWalk_ZeroLength_Skipped` | Zero-length skipped by default |
| `TestWalk_ZeroLength_Included` | `-z` includes zero-length |
| `TestWalk_ContextCancellation` | Cancelled context stops walk |
| `TestWalk_FileInfoFields` | FileInfo populated correctly |
| `TestWalk_MultiplePatterns` | Multiple patterns combined |

### `internal/worker` (5 tests)

| Test | Description |
|------|-------------|
| `TestPool_AllTasksExecute` | N tasks, all complete |
| `TestPool_Bounded` | Concurrency limited to pool size |
| `TestPool_ContextCancellation` | Cancelled ctx prevents execution |
| `TestPool_WaitBlocks` | `Wait()` blocks until completion |
| `TestPool_ZeroSize` | Size 0 → defaults to `runtime.NumCPU()` |

### `internal/extent` (6 tests)

| Test | Description |
|------|-------------|
| `TestSharedBytes` | Table test: partial/identical/disjoint/multiple overlaps, encoded extents ignored, shared-logical fallback |
| `TestQuery_HardlinksShareExtents` | Two hardlinked 64KB files share every byte; skips if `ErrUnsupported` |
| `TestQuery_IndependentCopiesShareNothing` | Two independent copies share nothing; skips if `ErrUnsupported` |
| `TestEqual` | Equal layouts compare true; length/order/physical differences, empty lists, encoded extents, and zero physical addresses compare false |
| `TestSharedFlagBytes` | Only extents flagged `Shared` contribute their lengths |
| `TestSharedWithOthers` | Physical-start identity within a group, capped to the shorter extent; nil others → 0 |

The `Query` tests skip themselves with `t.Skipf` when the filesystem cannot report extents, so they run meaningfully on Linux (FIEMAP/`FICLONE`), macOS (APFS), and Windows (ReFS).

### Filesystem-dependent tests

The CoW and extent tests need a filesystem that supports reflinks / extent
queries. `t.TempDir()` uses `$TMPDIR`, which on many Linux systems is `tmpfs` and
therefore reports neither extents nor clones. So `TestQuery_*`,
`TestDoExecution_CoWClone`, `TestDoExecution_CoWClone_SkipsAlreadyShared`,
`TestDoExecution_CoWDetect_GroupRatios`, `TestDedupeCoW_CreateAndDetect` and
`TestFind_CoW_IndependentCopiesZeroShared` probe the default temp dir first and
then fall back to a temporary directory inside the package working directory
(normally the repository, which is often on the developer's real btrfs/XFS/APFS
volume). They skip only when neither location supports the feature, so a plain
`go test ./...` exercises CoW on a btrfs workspace even when `/tmp` is tmpfs.

To force a specific filesystem explicitly:

```bash
TMPDIR=/path/on/btrfs go test ./... -count=1
```

### `internal/pipeline` (3 tests)

| Test | Description |
|------|-------------|
| `TestRun_CancelledContext` | A pre-cancelled context returns `context.Canceled` without deadlocking |
| `TestRun_EmptyPatterns` | No patterns completes cleanly |
| `TestRun_ListLink` | `--listlink` mode runs end to end |

## System Tests (`test/system_test.go`)

System tests build the `finddupe` binary once in `TestMain` and run it against real temp directories. The package is split: `test/doc.go` declares `package test` while the tests use `package test_test`.

There are **52 test functions** plus the `TestMain` harness — `grep -c '^func Test' test/system_test.go` reports **53** because it also matches `TestMain`.

### Find Mode (18 tests)

Basic duplicates, no duplicates, empty directory, zero-length files (skipped/included), verbose mode, threads flag, multiple paths, glob patterns, recursive glob, large files, CRC near-collision (same first chunk, different after), binary files, many duplicates, single file, multiple duplicate groups, `TestFind_ThreeOrMoreIdenticalFiles` (the regression test that 4 identical files yield exactly 3 duplicate reports), and `TestFind_DuplicatePathArgs_NoSelfDuplicate` (a directory passed twice must not report a file against itself).

### Dedupe `--delete` (9 tests)

Basic deletion, content preservation, missing action flag error, multiple actions error, read-only handling (skipped, forced with `-r`), many duplicates, `TestDedupeDelete_ThreeIdenticalFiles` (3 identical files → 2 deletions, 1 survivor), and `TestDedupeDelete_DuplicatePathArgs_NoDataLoss` (overlapping patterns must not delete every copy).

### Dedupe `--hardlink` (4 tests)

Hardlink creation, content preservation, same-inode verification, read-only handling.

### CoW (4 tests)

`TestDedupeCoW_Unsupported` (graceful failure when cloning is unavailable), `TestDedupeCoW_CreateAndDetect` (clone then detect the group; expects `CoW candidate group` and `shared: 100.0%`; conditionally `t.Skip`s when CoW is unsupported), `TestFind_CoW_IndependentCopiesZeroShared` (independent copies are still listed as a group, but at 0% shared), and `TestDedupe_CoW_PreservesHardlink` (an existing hardlink must not be broken by `dedupe --cow`).

### Reference Paths (1 test)

`TestDedupe_RefKeptAndDuplicateRemoved` — the `--ref` file is kept and the non-reference duplicate is removed.

### Hardlink Listing (1 test)

`TestFind_ListLink` — lists the group, prints both paths, does not run duplicate detection, and reports the group count.

### Edge Cases / Error Handling (8 tests)

No paths error, nonexistent path, no subcommand error, zero threads, many threads, version output, help output, subcommand help.

### Nested Directories and Complex Trees (2 tests)

`TestNestedDirectories`, `TestDedupe_NestedDirectories`.

### Glob Edge Cases (2 tests)

`TestGlob_StarExtension`, `TestGlob_CurrentDirPattern`.

### Stats (2 tests)

`TestStats_Find` (exact `Files:`/`Dupes:` counts) and `TestStats_FileSizes` (byte totals).

### Concurrency (1 test)

`TestConcurrentRuns` — three concurrent runs must not corrupt global state.

## Manual Platform Probes (macOS / Windows)

The automated suite covers Linux FIEMAP and `FICLONE` when the workspace volume
supports them. macOS (`F_LOG2PHYS_EXT`) and Windows
(`FSCTL_GET_RETRIEVAL_POINTERS`, ReFS block clones) need a real APFS/ReFS
machine, so two committed tools support hand-run validation:

- `testtools/extentdump` — `extentdump <file>...` prints each file's
  `dev`/`inode`/`numLinks` and every extent's
  `logical`/`physical`/`length`/`shared`/`encoded`, plus `sharedFlagBytes`. Use
  it to confirm what the platform extent API actually reports.
- `testscripts/build-bundles.sh` (also `make cow-test-bundles`) cross-compiles
  `finddupe` + `extentdump` for linux-amd64, darwin-amd64/arm64, and
  windows-amd64/arm64 into `bin/cow-test/<platform>/`, together with the probe
  script and README. `bin/` is gitignored, so bundles are build output.
- `testscripts/cow-probe.sh` (macOS/Linux bash) and
  `testscripts/cow-probe-windows.ps1` (Windows PowerShell) exercise independent
  copies, `dedupe --cow` then a second run, a pre-existing clone, an existing
  hardlink (must stay untouched), and compressible content, printing marked
  sections to send back.

See `testscripts/README.md` for the exact commands and how to interpret the
numbers.

## Race Detection

All tests should pass with `-race`. Because the race detector needs cgo, set `CGO_ENABLED=1` (the normal build uses `CGO_ENABLED=0`):

```bash
CGO_ENABLED=1 go test -race ./... -count=1
```

Key race-prone areas:
- `Stats` atomic counters
- `Detector` map access (single coordinator goroutine, guarded by its own mutex)
- Worker pool concurrent submissions
- Pipeline channels and the `inFlight` counter

## CI Test Matrix

`.github/workflows/ci.yml` runs three jobs:

- **test**: `ubuntu-latest`, `macos-latest`, `windows-latest`; each builds, vets, and
  runs the suite. The Go version is read from `go.mod` via `actions/setup-go`
  (`go-version-file`). The race detector runs on Ubuntu only
  (`go test ./... -race -count=1`); the other platforms run `go test ./... -count=1`.
- **lint**: `golangci-lint` v2.14.0 on Ubuntu.
- **cross-compile**: `make all-arch` on Ubuntu (all 21 supported targets).

Minimum: compile-test on all platforms:
```bash
GOOS=linux   GOARCH=amd64 go build ./...
GOOS=darwin  GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...
```

## Test Coverage

Target: >80% coverage on each package.

```bash
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

Critical paths requiring high coverage:
- Checksum and identity computation (correctness-critical)
- Detector state machine (`Insert` / `OnHashDone` / `OnCompareDone`, no double elimination)
- Chunked comparison / early-stop and resume
- Hardlink creation and deletion logic
- Same-physical-file refusal in every action (`ResultAlreadyHardlinked`)
- Extent overlap arithmetic, `extent.Equal`, and the per-file sharing signals
- CoW group construction (`CoWGroups` / inode collapse) and the per-member ratios

## Running Tests

```bash
# Unit tests
go test ./internal/... -count=1

# System tests (compile + run)
go test ./test/... -count=1 -timeout 120s

# All tests
go test ./... -count=1 -timeout 120s

# Race detector
CGO_ENABLED=1 go test -race ./... -count=1
```
