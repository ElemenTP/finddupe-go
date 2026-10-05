# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

finddupe-go is a Go implementation of the [finddupe](https://github.com/ElemenTP/finddupe) file deduplication tool. It supports finding duplicate files on multiple paths/glob patterns, identify already hardlinked files or CoW generated files, then do nothing, delete duplicated files, or replace with hardlinks/CoW copies. Licensed under MIT.

## Build Commands

```bash
# Build (requires go1.26+, pinned via go.mod)
make <your-os>-<cpu-arch>

# Build on dev machine
make linux-amd64

# Cross-compile every supported target (Linux/macOS/Windows)
make all-arch

# Build the CoW probe bundles for real-machine APFS/ReFS validation
make cow-test-bundles   # see testscripts/README.md
```

## Architecture

```
Filesystem visitor (walk paths/globs, produce file metadata)
        |
        v
Scanner (worker pool: weak signature + Dev/Inode/NumLinks, SHA-256 for <=32KB files)
        |
        v
Detector / coordinator (single goroutine; owns duplicate state, emits Execution work items)
        |
        v
Executor (worker goroutines; runs HashCalc / HashComp / DupeElim / CoWDetect, returns Outcome)
        |
        v
Coordinator (feeds outcomes back to the detector, dispatches follow-up work, reports results)
```

`dupe.Execution{Key, Type, Files}` is the unit of work. `dupe.Detector` is a state
machine: `Insert` returns the *hashing* work a new file triggers, `OnHashDone` /
`OnCompareDone` feed executor outcomes back in, and `NextFinal(limit)` decides the
duplicates once the scan is over — for every content bucket, a `KeeperPolicy` (refs
first, then more hardlinks, then the smallest path) picks the keeper and the other
members become victims. Elimination is never decided while files are still arriving,
so the keeper does not depend on which hash finished first.

### CoW / hardlink specifics

- CoW elimination: Linux `FICLONE`, macOS `clonefile(2)`, Windows ReFS
  `FSCTL_DUPLICATE_EXTENTS_TO_FILE`; unsupported filesystems return
  `action.ErrCoWNotSupported` and leave the victim untouched.
- CoW detection (`find --cow`) and hardlink listing (`find --listlink`) use
  `internal/extent` and the detector's inode index respectively.
- Inode identity is `(Dev, Inode)`: inode numbers are only unique per device.

### Startup Flow

`main.go` → cobra parses CLI args → `cmd/find.go|cmd/dedupe.go` → `pipeline.Run` creates the worker pool and coordinator, spawns the walker, scanner, and executor goroutines, and awaits completion or SIGINT.

## Lint Policy

Workspace-wide golangci lints are declared in the root `.golangci.yml` file.

## Regression Bar

Run before every commit:

```bash
go fmt ./
golangci-lint run ./...
CGO_ENABLED=1 go test ./... -race -count=1
```

## Key Dependencies

- **CLI args parser**: github.com/spf13/cobra
- **Platform syscalls**: golang.org/x/sys (CoW ioctls, extent queries)

## Original Project
Original finddupe project written in C is here as a reference: `project_root/finddupe-orig`