# Testing Strategy

## Overview

Two test suites complement each other:

- **Unit tests**: `internal/*/` — test individual packages in isolation
- **System tests**: `test/system_test.go` — test the compiled binary end-to-end against real filesystems

All tests run with the `-race` flag where available.

## Test Requirements (from .golangci.yml)

- **`paralleltest`**: All test functions must call `t.Parallel()`
- **`testpackage`**: Tests use a separate `_test` package
- **`exhaustruct`**: Tests must initialize all struct fields

## Unit Tests by Package

### `internal/checksum`

| Test | Description |
|------|-------------|
| `TestCompute_EmptyFile` | Empty file → signature 0 |
| `TestCompute_SmallFile` | File smaller than 32KB → entire file used |
| `TestCompute_Deterministic` | Same content → same signature |
| `TestCompute_DifferentFiles` | Different content → different signature |
| `TestCompute_FileSizeFoldedIn` | Same content, different reported sizes → different signatures |
| `TestCompute_LargeFile` | File larger than 32KB → only first 32KB used |
| `TestComputeFromReader` | Reader-based computation |
| `TestCompute_NonExistentFile` | Non-existent path → error |
| `TestCompute_CRCAndSumComponents` | Signature packs crc and sum correctly |

### `internal/dupe` (Detector)

| Test | Description |
|------|-------------|
| `TestDetector_FirstInsert` | First file → nil |
| `TestDetector_DuplicateSignature` | Same checksum → DupeGroup |
| `TestDetector_MultipleCollisions` | Third collision → one group (against first) |
| `TestDetector_DifferentSignatures` | Different checksums → all nil |
| `TestDetector_StatsUpdate` | Stats incremented correctly |
| `TestDetector_HardlinkMode` | InsertHardlink groups by inode |
| `TestDetector_HardlinkMode_SingleLink` | NumLinks==1 files skipped |
| `TestDetector_EmptyDetector` | Zero state verification |

### `internal/fswalker`

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

### `internal/action`

| Test | Description |
|------|-------------|
| `TestVerifyFullFile_Identical` | Same content → true |
| `TestVerifyFullFile_Different` | Different content → false |
| `TestVerifyFullFile_DifferentSizes` | Different sizes → false |
| `TestVerifyFullFile_LargeFiles` | >64KB files compared correctly |
| `TestVerifyFullFile_DifferAfterFirstChunk` | Mismatch after 100KB detected |
| `TestDelete_Success` | Duplicate deleted |
| `TestHardlink_Created` | Hardlink created, `os.SameFile` true |
| `TestHardlink_NotDuplicate` | Non-duplicate → ResultNotDuplicate |
| `TestCoW_Unsupported` | CoW returns unsupported error |
| `TestReport_NoAction` | Report mode: file preserved |

### `internal/worker`

| Test | Description |
|------|-------------|
| `TestPool_AllTasksExecute` | N tasks, all complete |
| `TestPool_Bounded` | Concurrency limited to pool size |
| `TestPool_ContextCancellation` | Cancelled ctx prevents execution |
| `TestPool_WaitBlocks` | Wait() blocks until completion |
| `TestPool_ZeroSize` | Size 0 → default to NumCPU |

## System Tests (`test/system_test.go`)

System tests compile the `finddupe` binary once via `TestMain` and run it against real temp directories. 46 tests in total:

### Find Mode (16 tests)

Basic duplicates, no duplicates, empty directory, zero-length files (skipped/included), verbose mode, threads flag, multiple paths, glob patterns, recursive glob, large files, CRC near-collision, binary files, many duplicates, single file, multiple duplicate groups.

### Dedupe --delete (7 tests)

Basic deletion, content preservation, missing action flag error, multiple actions error, readonly file handling (skipped, forced with `-r`), many duplicates.

### Dedupe --hardlink (4 tests)

Hardlink creation, content preservation, same-inode verification, readonly handling.

### Edge Cases / Error Handling (7 tests)

No paths error, nonexistent path, no subcommand error, zero threads, many threads, version output, help output, subcommand help.

### Additional (4 tests)

Nested directories (find + dedupe), glob edge cases, stats verification, concurrent runs.

## Race Detection

All tests should pass with `-race`:

```bash
CGO_ENABLED=1 go test -race ./...
```

Key race-prone areas:
- `Stats` atomic counters
- `Detector` map access (single goroutine)
- Worker pool concurrent submissions
- Pipeline channels

## Benchmarks

```go
// internal/checksum/checksum_test.go
func BenchmarkCompute(b *testing.B)

// internal/action/action_test.go
func BenchmarkVerifyFullFile(b *testing.B)

// internal/dupe/detector_test.go
func BenchmarkDetectorInsert(b *testing.B)
```

## CI Test Matrix

```yaml
strategy:
  matrix:
    os: [ubuntu-latest, macos-latest, windows-latest]
    go: ['1.26']
```

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
- Checksum computation (correctness-critical)
- Duplicate detection insert/lookup
- Full file verification
- Hardlink creation/deletion logic

## Running Tests

```bash
# Unit tests
go test ./internal/... -count=1

# System tests (compile + run)
go test ./test/... -count=1 -timeout 120s

# All tests
go test ./... -count=1 -timeout 120s
```
