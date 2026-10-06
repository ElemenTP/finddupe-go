# Testing Strategy

## Overview

Two test suites complement each other:

- **Unit tests**: `internal/*/` — test individual packages in isolation
- **System tests**: `test/system_test.go` — test the compiled binary end-to-end against real filesystems

All tests run with the `-race` flag (see [Race Detection](#race-detection)).

## Test Requirements (from .golangci.yml)

- **`paralleltest`**: All test functions must call `t.Parallel()`
- **`testpackage`**: Tests use a separate `_test` package. An in-package test file is allowed with an explanatory `//nolint:testpackage` comment when the behavior under test is unexported (`internal/pipeline/coordinator_test.go`, `internal/action/action_internal_test.go`).
- **`tparallel`**: Detects inappropriate `t.Parallel()` usage

`exhaustruct` is present in the config file but **disabled** (commented out in the enabled-linters list), so tests do not have to initialize every struct field.

## Unit Tests by Package

### `internal/checksum` (11 tests)

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
| `TestComputeFileInfo_SizeChanged` | File no longer the scanned size → `dupe.ErrFileChanged` instead of a misleading signature |
| `TestComputeFileInfo_ModTime` | `Info.ModTime` reports the file's modification time |

### `internal/dupe` (Detector, 21 tests)

| Test | Description |
|------|-------------|
| `TestDetector_FirstInsert` | First file → no executions; stats updated |
| `TestDetector_TwoUnhashedFiles_EmitHashComp` | Exactly two unhashed files → one `HashComp` |
| `TestDetector_ThirdUnhashedFile_EmitHashCalc` | Third unhashed file → `HashCalc` for the new file only |
| `TestDetector_KnownShaMatch_DeferredToFinal` | A matching SHA-256 is decided by `NextFinal`, not during the scan |
| `TestDetector_KnownShaMismatch_NoExec` | Different SHA-256 in one group → no work (only a bucket's members are duplicates) |
| `TestDetector_ThreeIdenticalFiles_AllReported` | Three identical files → two eliminations, one keeper, no victim twice |
| `TestDetector_EarlyStoppedCompareIsCompleted` | Regression: files stranded by an early-stopped comparison are hashed by `NextFinal`, so a duplicate against a later-finished file is found |
| `TestDetector_SettledPairNotHashed` | A two-file comparison that proved a difference needs no hashing |
| `TestDetector_FailedCompareIsRetried` | A comparison that could not run leads to a hash attempt, and unhashable files stop being retried |
| `TestDetector_KeeperPolicyPrefersReference` | A reference wins the keeper choice even when it was inserted last |
| `TestDetector_KeeperPolicyPrefersMoreHardlinks` | The victim is the file whose removal actually frees storage |
| `TestDetector_KeeperPolicyIsStableRegardlessOfInsertOrder` | Same keeper for either insertion order |
| `TestDetector_CustomKeeperPolicy` | `WithKeeperPolicy` replaces the built-in order |
| `TestDetector_CompressionPreference` | `WithCompressionPreference` keeps the compressed member ahead of the path order |
| `TestDetector_CompressionPreferenceIsNotProbedPerComparison` | The probe runs once per member, not once per comparison |
| `TestDetector_KeeperChooser` | The chooser decides the keeper; declining (or naming a non-member) leaves the group alone |
| `TestDetector_KeeperChooserDeclines` | A chooser that refuses every group produces no eliminations |
| `TestDetector_KeeperChooserCoWDetect` | The chosen keeper leads the CoW group, so it is the clone source |
| `TestDetector_FinalBatchesAreBounded` | A large group is emitted over several bounded `NextFinal` calls |
| `TestDetector_PartialProgressKeepsLargerOffset` | The most advanced partial offset wins |
| `TestDetector_CRCCollisionSeparatesBuckets` | Different sizes stay separate groups |
| `TestDetector_HardlinkedAliasesCollapse` | Aliases of one inode never eliminate each other, and the preferred path survives |
| `TestDetector_EveryPathOfAVictimInodeIsAVictim` | A victim is a path: both paths of the other physical file are acted on (the regression that left a hardlink behind on the old inode) |
| `TestDetector_HardlinkedAliasesAreReported` | `WithHardlinkedAliases` lists the keeper's own alias; without it that pair is dropped |
| `TestDetector_CoWDetectMode` | One `CoWDetect` per content bucket, members in keeper order |
| `TestDetector_InsertInodeGroups` | `InsertInode`/`InodeGroups` group by `(Dev, Inode)` |
| `TestDetector_Empty` | Zero state: no executions, no inode groups, non-nil stats |
| `TestQuery_ExtentsCoverTheFile` | A written file's extents cover all of its bytes (a macOS request that was not rewritten per step reported 4 KiB of a 1 MiB file) |
| `TestQuery_SubClusterFileIsNotAnError` | A resident sub-cluster file (NTFS keeps tiny files in their record) is an empty mapping, not an error |
| `TestPreserveMetadata_UsesTheCallersStat` | The clone's metadata comes from the stat the freshness check made, not from a second read of the source (a stale value passed in must win) |
| `fsprobe.CapableDir` | Shared by the action, extent and system suites: tries the default temp dir, then the working directory, and skips the test when neither filesystem supports the feature (one copy instead of three) |
| `TestDetector_SamePathInsertedTwice` | Inserting one path twice is ignored (overlapping patterns cannot self-eliminate) |

### `internal/action` (27 tests)

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
| `TestDoExecution_DupeElim_Hardlink_LinkFailureKeepsVictim` | Regression: a failed link leaves the victim (and its content) untouched and no temp file behind |
| `TestDoExecution_DupeElim_Hardlink_PreservesKeeperMetadata` | Hardlinking does not rewrite the keeper's mode/mtime |
| `TestDoExecution_DupeElim_CrossDeviceSkipsHardlink` | Different `Dev` → `ResultSkippedCrossDevice`, both files preserved |
| `TestCloneFile_CrossDeviceIsSkipped` | A `--cow` pair on two devices is skipped before any attempt (the clone could not share blocks across volumes); a zero device stays conservative and is still attempted |
| `TestWalk_SymlinkedPrefixReportsFilesOnce` | A directory reached under two spellings (walk root spelled through a symlink, link resolving to the canonical path) is walked once |
| `TestDoExecution_DupeElim_ChangedFileSkipped` | A file changed after hashing → `ResultSkippedChanged` for delete/hardlink/CoW |
| `TestDoExecution_DupeElim_UnchangedFileIsActedOn` | A matching size/mtime does not block the action |
| `TestDoExecution_CoWClone_CrossDeviceNotShared` | Identical extent layouts on different devices are not treated as shared |
| `TestCopyTailAt_DestinationOffset` | Regression: the unaligned clone tail lands at its own offset, not at 0 |
| `TestSameDevice` | Unknown (zero) `Dev` is accepted; differing devices are not |
| `TestHardlinkLimitReached` | The fresh limit check never refuses a normal or missing file |
| `TestDoExecution_CoWClone_PreservesVictimMetadata` | A clone keeps the victim's mode, mtime and (where supported) extended attributes instead of the keeper's |
| `TestDoExecution_SamePhysicalFile_UnknownIdentity` | Without a file index from the scan, two names for one file are still recognized (via `os.SameFile`) and left alone |
| `TestDoExecution_DupeElim_SymlinkVictimSkipped` | A path that became a symlink after hashing is never acted on, even when its target still matches |
| `TestDoExecution_CoWClone_PreservesReadOnlyVictimMetadata` | Regression: a read-only victim keeps its extended attributes (the final mode is applied after them) |
| `TestDataLayoutXattr` | The attributes that carry a file's data layout (macOS decmpfs/resource fork) are exempt from the victim-metadata sync; user metadata is not |
| `TestDoExecution_CoWClone_KeepsCompressedLayout` (darwin) | Regression: cloning a decmpfs-compressed keeper over an uncompressed victim kept the content, the compression attributes, the `UF_COMPRESSED` flag and the keeper's size (it used to leave a zero-length file) |
| `TestDoExecution_CoWClone_DoesNotInheritVictimCompression` (darwin) | The other half of the rule: cloning an uncompressed keeper over a compressed victim must not attach the victim's compressed container to the clone |

### `internal/fswalker` (33 tests)

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
| `TestWalk_MultipleRecursivePatterns` | Regression: the loop-prevention set is per pattern, so a second recursive pattern does not skip the first one's directories |
| `TestWalk_GlobInDirectoryComponent` | `sub/*/x.txt` and `*/s1/*.txt` match (a wildcard directory component used to match nothing) |
| `TestWalk_DoubleStarCollapse` | Repeated `**` behaves like a single one |
| `TestWalk_ManyDoubleStarsTerminate` | Regression: eight `**` against a 40-component path finish quickly (the matcher used to backtrack exponentially) |
| `TestWalk_PatternWithFilePathPrefix` | A pattern under a regular file matches nothing and is reported as a miss |
| `TestWalk_NonDirectoryPrefixReportsError` | A prefix that cannot be stat'ed for a reason other than "missing" is still reported |
| `TestWalk_SymlinkToFile_SkippedWithoutFollow` | Links are not reported unless `-j` |
| `TestWalk_SymlinkToFile_FollowedIsReportedAsTarget` | Regression: a followed link is reported under the target's path with the target's size, never as the link itself |
| `TestWalk_SymlinkToDir_Followed` | `-j` walks a directory link exactly once, even when the target is in the tree |
| `TestWalk_SymlinkDir_SkippedWithoutFollow` | A directory link is not descended into by default |
| `TestWalk_SymlinkLoop_Terminates` | A link to an ancestor terminates |
| `TestWalk_BrokenSymlink_FollowedIsIgnored` | Dangling links are skipped without errors |
| `TestWalk_ExplicitSymlinkArgument` | A link named directly resolves to its target |
| `TestWalk_FileInfoCarriesModTime` | `FileInfo.ModTime` is populated by the walker |
| `TestWalk_Fifo_NotOpened` / `TestWalk_ExplicitFifoArgument` | Regression: a FIFO is never opened (it used to block the scan with `--zero`) |
| `TestWalk_NoMatchError` | Missing path, empty directory and a glob with no hits each report exactly one no-match error |
| `TestWalk_MatchingPatternIsNotReported` | A pattern that matched a (skipped) zero-length file is not reported as a miss |
| `TestWalk_LiteralPathWithGlobCharacters` | A directory named `[2020] photos` is scanned as written instead of split on its brackets |
| `TestWalk_GlobInsideGlobNamedDir` | `[2020] photos/*.txt` is split at the existing directory, not at the first bracket |
| `TestWalk_GlobClassStillMatches` | `[ab].txt` is still treated as a character class |
| `TestSendResult` | The walker abandons a result send once the walk is cancelled, so it cannot park forever |

### `internal/worker` (5 tests)

| Test | Description |
|------|-------------|
| `TestPool_AllTasksExecute` | N tasks, all complete |
| `TestPool_Bounded` | Concurrency limited to pool size |
| `TestPool_ContextCancellation` | Cancelled ctx prevents execution |
| `TestPool_WaitBlocks` | `Wait()` blocks until completion |
| `TestPool_ZeroSize` | Size 0 → defaults to `runtime.NumCPU()` |

### `internal/extent` (12 tests)

| Test | Description |
|------|-------------|
| `TestSharedWithGroupOverlap` | Table test: partial/identical/disjoint/multiple overlaps, encoded extents without a physical start ignored |
| `TestQuery_SparseFileIsNotUnsupported` | Regression: a file with nothing allocated is "nothing shared", not "this filesystem is unsupported" |
| `TestBoundedExtentCount` | A driver-reported extent count is bounded by the buffer before it sizes an allocation or drives the walk |
| `TestQuery_HardlinksShareExtents` | Two hardlinked 64KB files share every byte; skips if `ErrUnsupported` |
| `TestQuery_LengthsClampedToFileSize` | Regression: extent lengths never run past EOF, so a sharing ratio cannot exceed 100% |
| `TestAppendBatch` (linux only) | FIEMAP batch bookkeeping: which offset the next request starts from, so a non-advancing response cannot loop forever |
| `TestQuery_IndependentCopiesShareNothing` | Two independent copies share nothing; skips if `ErrUnsupported` |
| `TestEqual` | Equal layouts compare true; length/order/physical differences, empty lists, encoded extents, and zero physical addresses compare false |
| `TestSharedFlagBytes` | Only extents flagged `Shared` contribute their lengths |
| `TestSharedWithGroup` | Per-member in-group sharing, capped to the shorter extent; unrelated members stay at 0 |
| `TestQuery_CloneSharesExtents` (darwin only) | A `cp -c` clone maps to the same physical extents (or clone ID) as its original; an independently written copy does not |
| `TestQuery_PartialClone` (darwin only) | Rewriting part of a clone with identical bytes is reported as partial sharing, not all-or-nothing; skips if the filesystem kept the extents shared |

The `Query` tests skip themselves with `t.Skipf` when the filesystem cannot report extents, so they run meaningfully on Linux (FIEMAP/`FICLONE`), macOS (APFS `F_LOG2PHYS_EXT`, clone ID for compressed files), and Windows (ReFS).

### Filesystem-dependent tests

The CoW and extent tests need a filesystem that supports reflinks / extent
queries. `t.TempDir()` uses `$TMPDIR`, which on many Linux systems is `tmpfs` and
therefore reports neither extents nor clones. So `TestQuery_*`,
`TestDoExecution_CoWClone`, `TestDoExecution_CoWClone_SkipsAlreadyShared`,
`TestDoExecution_CoWDetect_GroupRatios`,
`TestDoExecution_CoWClone_PreservesVictimMetadata`, `TestDedupeCoW_CreateAndDetect` and
`TestFind_CoW_IndependentCopiesZeroShared` probe the default temp dir first and
then fall back to a temporary directory inside the package working directory
(normally the repository, which is often on the developer's real btrfs/XFS/APFS
volume). They skip only when neither location supports the feature, so a plain
`go test ./...` exercises CoW on a btrfs workspace even when `/tmp` is tmpfs.

To force a specific filesystem explicitly:

```bash
TMPDIR=/path/on/btrfs go test ./... -count=1
```

### `internal/volinfo` (0 tests)

`ClusterSize` is the Windows-only volume probe shared by the CoW clone and the
extent query; it is covered by the Windows cross-build and exercised on real
hardware by the CoW probe scripts.

### `internal/progress` (1 test)

| Test | Description |
|------|-------------|
| `TestIsTerminal` | A regular file is not a terminal, so redirected output never gets escape sequences |

### `internal/pipeline` (12 tests)

`interactive_test.go` covers the prompt loop: keeping a member by number, `a`
applying the default choice to every later group without asking again, `s` skipping
one group, `q` stopping, the three-attempt bound on unusable answers, and end of
input. `progress.IsTerminal` is a real `isatty` (termios ioctl / GetConsoleMode),
so `/dev/null` is no longer mistaken for a terminal.

| Test | Description |
|------|-------------|
| `TestRun_CancelledContext` | A pre-cancelled context returns `context.Canceled` without deadlocking |
| `TestRun_EmptyPatterns` | No patterns completes cleanly |
| `TestRun_NoMatchFails` | A pattern that matched nothing fails the run and names the pattern |
| `TestRun_ListLink` | `--listlink` mode runs end to end |
| `TestCoordinateSaturatedChannels` | Regression: the coordinator does not deadlock when the execution and outcome channels are saturated |

## System Tests (`test/system_test.go`)

System tests build the `finddupe` binary once in `TestMain` and run it against real temp directories. The package is split: `test/doc.go` declares `package test` while the tests use `package test_test`.

There are **63 test functions** in `test/system_test.go` plus the `TestMain`
harness — `grep -c '^func Test' test/system_test.go` reports **64** because it
also matches `TestMain` — and one more in `test/output_unix_test.go` (Unix only,
because the crafted file name needs a control character that Windows forbids).

### Find Mode (19 tests)

Basic duplicates, no duplicates, empty directory, zero-length files (skipped/included), verbose mode, threads flag, multiple paths, glob patterns, recursive glob, large files, CRC near-collision (same first chunk, different after), binary files, many duplicates, single file, multiple duplicate groups, `TestFind_ThreeOrMoreIdenticalFiles` (the regression test that 4 identical files yield exactly 3 duplicate reports), `TestFind_DuplicatePathArgs_NoSelfDuplicate` (a directory passed twice must not report a file against itself), and `TestFind_ReportsAlreadyHardlinkedPair` (an already-hardlinked pair is listed with the `(hardlinked instances of same file)` tag and only `--hardlink` suppresses it).

### Dedupe `--delete` (10 tests)

Basic deletion, content preservation, missing action flag error, multiple actions error, read-only handling (skipped, forced with `-r`), many duplicates, `TestDedupeDelete_ThreeIdenticalFiles` (3 identical files → 2 deletions, 1 survivor), `TestDedupeDelete_DuplicatePathArgs_NoDataLoss` (overlapping patterns must not delete every copy), and `TestDedupeDelete_RemovesEveryPathOfAVictimInode` (every path of the duplicate's physical file is removed, the keeper's own hardlinks survive).

### Dedupe `--hardlink` (5 tests)

Hardlink creation, content preservation, same-inode verification, read-only handling, and `TestDedupeHardlink_LinksEveryPathOfAVictimInode` (the regression test that one run links every path of the victim's physical file, so a second run finds nothing).

### CoW (4 tests)

`TestDedupeCoW_Unsupported` (graceful failure when cloning is unavailable), `TestDedupeCoW_CreateAndDetect` (clone then detect the group; expects `CoW candidate group` and `shared: 100.0%`; conditionally `t.Skip`s when CoW is unsupported), `TestFind_CoW_IndependentCopiesZeroShared` (independent copies are still listed as a group, but at 0% shared), and `TestDedupe_CoW_PreservesHardlink` (an existing hardlink must not be broken by `dedupe --cow`).

### Reference Paths (2 tests)

`TestDedupe_RefKeptAndDuplicateRemoved` — the `--ref` file is kept and the non-reference duplicate is removed. `TestDedupe_RefKeepsLargeOriginal` is the regression test for files larger than the scan-time checksum window: the reference must still win the keeper choice (it used to arrive as a victim and leave the duplicate in place).

### Symlinks (2 tests)

`TestDedupe_SymlinksResolvedToTarget` (a followed link is scanned and acted upon as its target: `--hardlink` links the real inode and leaves the link alone) and `TestDedupe_DeleteSymlinkTargetNotLink` (the duplicate file is removed, never the link).

### Compression Preference (0 tests)

`--prefer-compressed` is covered by the detector unit tests and by the CLI
validation test (`--prefer-compressed` without `--cow` fails); producing a mixed
compressed/uncompressed group needs a filesystem that compresses, so it is not
part of the portable system suite.

### Failed Actions (2 tests)

`TestSummary_ReportsFailedActions` fails a CoW clone for real (skipping when the
filesystem supports cloning) and checks the `N files could not be processed` summary
line; `TestDedupeCoW_Unsupported` covers the graceful-failure path itself.

### Interactive Keeper (1 test)

`TestDedupe_InteractiveNeedsTerminal` — `--interactive` without a terminal on stdin
fails with a clear error instead of leaving every group untouched. The prompt
behaviour itself is covered by the pipeline unit tests (`interactive_test.go`):
keeping by number, `a` applying the default to every later group, `s` skipping one
group, `q` stopping, the retry bound for unusable answers, and end of input. The
full flow was exercised on a real pty during development (answers `2`, `a`, `s`,
`q`, garbage) and behaved as documented.

### Keeper Determinism (1 test)

`TestDedupe_KeeperIsDeterministic` — the same input keeps the same path over repeated runs, whatever order the hashes finish in.

### Hardlink Listing (1 test)

`TestFind_ListLink` — lists the group, prints both paths, does not run duplicate detection, and reports the group count.

### Edge Cases / Error Handling (11 tests)

No paths error, nonexistent path (fails with a no-match error), no subcommand error, zero threads, many threads, version output, help output, subcommand help, `TestOutputStreams` (results and summary on stdout, diagnostics on stderr, no escape sequences on a non-terminal stderr), and `TestOutput_EscapesControlCharactersInPaths` (a file name containing a newline cannot forge a result line).

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
supports them. macOS (`F_LOG2PHYS_EXT`, clone ID for compressed files) and Windows
(`FSCTL_GET_RETRIEVAL_POINTERS`, ReFS block clones) need a real APFS/ReFS
machine, so two committed tools support hand-run validation:

- `testtools/extentdump` — `extentdump <file>...` prints each file's
  `dev`/`inode`/`numLinks` and every extent's
  `logical`/`physical`/`length`/`shared`/`encoded`, plus `sharedFlagBytes`. Use
  it to confirm what the platform extent API actually reports.
- `testtools/darwinfiemap` (darwin only) — probes whether macOS can enumerate a
  file's logical→physical mapping with `fcntl(F_LOG2PHYS_EXT)` / `F_LOG2PHYS`
  through the libSystem wrapper (`unix.FcntlInt`, no raw syscall). It prints the
  raw per-step results for four input conventions and compares files block by
  block, so we can tell whether an APFS clone is detectable from physical
  offsets without `getattrlist`.
- `testscripts/build-bundles.sh` (also `make cow-test-bundles`) cross-compiles
  `finddupe` + `extentdump` for linux-amd64, darwin-amd64/arm64, and
  windows-amd64/arm64 into `bin/cow-test/<platform>/`, together with the probe
  script and README. `bin/` is gitignored, so bundles are build output.
- The probes have been run on real hardware: an Apple Silicon MacBook (macOS 27
  / APFS, with and without `afsctool` compression) and a Windows 11 desktop with
  a ReFS 3.14 Dev Drive. Both reported 0% before cloning, 100% after, a no-op
  second `dedupe --cow`, and untouched hardlinks.
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
- Detector state machine (`Insert` / `OnHashDone` / `OnCompareDone` / `NextFinal`, no double elimination, deterministic keeper)
- Chunked comparison / early-stop and resume
- Hardlink creation and deletion logic
- Same-physical-file refusal in every action (`ResultAlreadyHardlinked`)
- Extent overlap arithmetic, `extent.Equal`, and the per-file sharing signals
- CoW group construction (buckets collapsed by inode) and the per-member ratios

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
