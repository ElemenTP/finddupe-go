# CLI Specification

## Commands

```
finddupe [command] [flags]

Commands:
  find      Find and report duplicate files
  dedupe    Find and eliminate duplicate files
  version   Show version information
  help      Help about any command
```

`version` is a subcommand, not a root `--version` flag.

## `finddupe find` — Scan and Report

```
finddupe find [flags] <path/pattern> [path/pattern...]
```

Scans the specified paths/patterns for duplicate files and reports them. No files are modified.

### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--hardlink` | `-H` | bool | false | Skip already-hardlinked files when reporting duplicates. Files sharing the same `(Dev, Inode)` (already hardlinked to each other) are not reported or counted. |
| `--listlink` | `-l` | bool | false | List hardlink groups (files sharing a physical inode) and exit. Skips duplicate detection entirely. |
| `--cow` | `-c` | bool | false | CoW search mode: report duplicate files that also share physical extents. |
| `--verbose` | `-v` | bool | false | Verbose output: show hardlink skip details, file index information |
| `--zero` | `-z` | bool | false | Include zero-length files (skipped by default) |
| `--no-progress` | `-p` | bool | false | Hide the progress indicator |
| `--follow-symlinks` | `-j` | bool | false | Follow symbolic links / reparse points |
| `--threads` | `-t` | int | `0` (→ `runtime.NumCPU() × 2`) | Number of scanner workers |
| `--ref` | — | string (repeatable) | — | Consume the following path/pattern as a reference (compare against, but never act on). Can be repeated. |

`--hardlink`, `--listlink`, and `--cow` are **mutually exclusive**. Selecting more than one fails with:

```
only one of --hardlink, --listlink, or --cow may be used
```

There is no `--sigs`/`-s` flag: signature printing was removed as dead code and is noted as future work in the [C flag mapping](#flag-mapping-c-original-to-go).

### Arguments

One or more path/pattern arguments. Each can be:
- A directory path → scan recursively (equivalent to `dir/**`)
- A glob pattern → scan matching files

### `--ref` Behavior

`--ref <path/pattern>` marks the following argument as a reference path. Reference paths are:
- Walked **first**, and their whole checksum phase is drained before any normal file is submitted, so reference files are inserted first and become the **keeper** of their content group.
- Never eliminated: the executor returns `ResultSkippedRef` when a duplicate victim is a reference file (unless the action is `find`'s report-only mode). They are counted in `SkippedRefFiles`.

### Output (Normal Mode)

```
Duplicate: '/path/to/original.jpg'
With:      '/path/to/copy.jpg'

Files:    1234 MB in  5000 files
Dupes:     100 MB in   234 files
  5 files of zero length were skipped
  2 files could not be opened
```

### Output (Verbose Mode, `-v`)

In verbose mode skipped hardlinked pairs are logged at info level:

```
time=... level=INFO msg="already hardlinked" keeper=/path/to/a.txt victim=/path/to/b.txt
Duplicate: '/path/to/original.jpg'
With:      '/path/to/copy.jpg'
```

### Output (With `--hardlink`)

Duplicates with different `(Dev, Inode)` are reported as usual. Pairs that are already hardlinks (same `Dev` and `Inode`, `NumLinks > 1`) are silently skipped and not counted as duplicates; with `-v` each pair is logged as `already hardlinked`.

### Output (`--listlink`)

`find --listlink` does **not** run duplicate detection. It groups files by `(Dev, Inode)` and prints one block per hardlink group, then exits:

```
Hardlink group, 2 hardlinked instances found:
    '/data/a.txt'
    '/data/b.txt'

Files:       1 MB in      2 files
Dupes:       0 B in      0 files
  1 hardlink groups found
```

Note: the summary always prints the generic `Files:`/`Dupes:` lines; each group adds a `Hardlink group, N hardlinked instances found:` block, and the summary adds `N hardlink groups found` when at least one was found.

### Output (`--cow`)

Reports duplicate content whose files also share physical extents. Independent copies of the same content are detected as duplicates but do not produce a `CoW group` line:

```
CoW group: '/data/a.bin' and '/data/b.bin' share 1 MB

Files:     128 MB in     2 files
Dupes:     128 MB in     1 files
  1 CoW groups found, 1 MB shared
```

CoW detection costs an extra open + extent query per file, which is why it is opt-in. On filesystems without extent reporting the files are still reported as duplicates, but no group is printed. See [cross-platform.md](cross-platform.md).

## `finddupe dedupe` — Scan and Eliminate

```
finddupe dedupe [flags] <path/pattern> [path/pattern...]
```

Scans the specified paths/patterns for duplicate files and takes action on them.

### Action Flags (Mutually Exclusive, Exactly One Required)

| Flag | Short | Description |
|------|-------|-------------|
| `--delete` | `-d` | Delete duplicate files |
| `--hardlink` | `-H` | Replace duplicates with hardlinks to the original |
| `--cow` | `-c` | Replace duplicates with CoW (Copy-on-Write) clones |

Exactly one action flag must be specified for `dedupe` mode:
- none → `no action specified: use --delete, --hardlink, or --cow`
- more than one → `only one action flag allowed: --delete, --hardlink, or --cow`

### Other Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--verbose` | `-v` | bool | false | Verbose output |
| `--zero` | `-z` | bool | false | Include zero-length files |
| `--no-progress` | `-p` | bool | false | Hide the progress indicator |
| `--follow-symlinks` | `-j` | bool | false | Follow symbolic links / reparse points |
| `--threads` | `-t` | int | `0` (→ `runtime.NumCPU() × 2`) | Number of scanner workers |
| `--rdonly` | `-r` | bool | false | Also operate on read-only files (Windows) |
| `--ref` | — | string (repeatable) | — | Consume the following path/pattern as a reference (compare against, but never act on) |

### Output

```
Deleted:    '/path/to/duplicate1.txt'
Hardlinked: '/path/to/duplicate2.jpg'
CoW cloned: '/path/to/duplicate3.bin'

Files:    1234 MB in  5000 files
Dupes:     100 MB in   234 files
  2 files deleted
  1 files replaced with hardlinks
  1 files replaced with CoW clones
```

Read-only victims that are skipped print `Skipping duplicate readonly file '<path>'.` and increment `SkippedROFiles`. Reference victims increment `SkippedRefFiles` and print nothing.

## `finddupe version`

```
finddupe version
```

Output:

```
finddupe 2.0.0 (Go rewrite with multi-threading support)
Built: 2026-01-15T10:30:00Z
Platform: linux/amd64
Go version: go1.26
CPUs: 16
```

## Flag Validation Rules

1. **Dedupe requires exactly one action**: one of `--delete`, `--hardlink`, or `--cow` must be specified
2. **Dedupe actions are mutually exclusive**: only one of `--delete`, `--hardlink`, `--cow`
3. **Find modes are mutually exclusive**: only one of `--hardlink`, `--listlink`, `--cow`
4. **`--hardlink` semantics differ by mode**:
   - `find --hardlink` (`-H`): skip already-hardlinked files during duplicate detection
   - `dedupe --hardlink` (`-H`): replace duplicates by creating hardlinks to the original
5. **Paths are required**: at least one path/pattern argument must be provided
6. **Ref paths**: `--ref <path>` consumes the next argument and can be repeated
7. **Threads**: `--threads 0` (the default) uses `runtime.NumCPU() × 2` workers; negative values are also treated as the default
8. **Reference files are never eliminated**: a reference duplicate is reported as skipped, even in `dedupe` mode

## Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--help` | `-h` | Show help for any command |

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| Non-zero (1) | Error (invalid arguments, I/O failure) or interruption via SIGINT/SIGTERM |

On SIGINT/SIGTERM the context is cancelled, `pipeline.Run` returns the context error, and `cmd.Execute` exits non-zero.

## Flag Mapping: C Original to Go

| C Flag (`finddupe.exe`) | Go `finddupe find` | Go `finddupe dedupe` | Notes |
|--------------------------|-------------------|----------------------|-------|
| `-hardlink` | `-H, --hardlink` (skip hardlinked) | `-H, --hardlink` (create hardlinks) | Different semantics per mode |
| `-del` | N/A | `-d, --delete` | |
| N/A | N/A | `-c, --cow` | CoW clone (implemented) |
| `-bat <file>` | N/A | N/A | |
| `-v` | `-v, --verbose` | `-v, --verbose` | |
| `-sigs` | N/A | N/A | Removed; future work |
| `-rdonly` | N/A | `-r, --rdonly` | |
| `-ref` | `--ref` | `--ref` | Changed from a flag to a repeatable value flag |
| `-z` | `-z, --zero` | `-z, --zero` | |
| `-u` | N/A | N/A | C: suppress warnings. Go: use `-v` |
| `-p` | `-p, --no-progress` | `-p, --no-progress` | |
| `-j` | `-j, --follow-symlinks` | `-j, --follow-symlinks` | |
| `-listlink` | `-l, --listlink` | N/A | C: list hardlink groups. Implemented for `find` |
| N/A | `-c, --cow` | N/A | New: detect CoW-shared duplicates |
| N/A | `-t, --threads <n>` | `-t, --threads <n>` | New (Go multi-threading) |

### Future Work

- `--sigs` (C `-sigs`): print computed file signatures without duplicate detection. The signature-printing config field and both command flags were removed as dead code.
