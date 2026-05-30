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
```

## Architecture

```
Producer-Consumer Architecture

Filesystem visitor (visit filesystem, produce information of files)
        |
        v
Duplication manager (consume file information, judge if is duplication and record, produce action event)
        |
        v
Action executor (consume action event and execute actions: delete, hardlink or CoW)

All running on worker pool
```

### Startup Flow

`main.go` → use cobra to parse CLI args → `cmd/find.go|cmd/dedupe.go` → create worker pool → spawn Filesystem visitor, Duplication manager, Action executor on worker pool → await all tasks to finish or SIGINT/SIGTERM.

## Lint Policy

Workspace-wide golangci lints are declared in the root `.golangci.yml` file.

## Regression Bar

Run before every commit:

```bash
go fmt ./
golangci-lint run ./...
```

## Key Dependencies

- **CLI args parser**: github.com/spf13/cobra

## Original Project
Original finddupe project written in C is here as a reference: `project_root/finddupe-orig`