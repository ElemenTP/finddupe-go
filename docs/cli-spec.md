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

## `finddupe find` — Scan and Report

```
finddupe find [flags] <path/pattern> [path/pattern...]
```

Scans the specified paths/patterns for duplicate files and reports them. No files are modified.

### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--hardlink` | `-H` | bool | false | Skip already-hardlinked files when reporting duplicates. Files sharing the same inode (already hardlinked to each other) are not counted as duplicates. |
| `--sigs` | `-s` | bool | false | Print file signatures only (no duplicate detection) |
| `--verbose` | `-v` | bool | false | Verbose output: show hardlink skip details, file index information |
| `--zero` | `-z` | bool | false | Include zero-length files (skipped by default) |
| `--no-progress` | `-p` | bool | false | Hide the progress indicator |
| `--follow-symlinks` | `-j` | bool | false | Follow symbolic links / reparse points |
| `--threads` | `-t` | int | `GOMAXPROCS` | Number of scanner workers |
| `--ref` | — | string (repeatable) | — | Mark following path/pattern as reference (compare against but never act on). Can appear multiple times among path arguments. |

### Arguments

One or more path/pattern arguments. Each can be:
- A directory path → scan recursively (equivalent to `dir/**`)
- A glob pattern → scan matching files
- Prefixed with `--ref` → mark as reference pattern

### Output (Normal Mode)

```
Duplicate: '/path/to/original.jpg'
With:      '/path/to/copy.jpg'

Files:    1234 MB in 5000 files
Dupes:     100 MB in  234 files
  Zero-length files skipped: 5
  Unreadable files: 2
```

### Output (Verbose Mode, `-v`)

Shows additional detail including skipped hardlinked pairs:

```
Already hardlinked: '/path/to/file.txt' and '/path/to/link.txt'
Duplicate: '/path/to/original.jpg'
With:      '/path/to/copy.jpg'

Files:    1234 MB in 5000 files
Dupes:     100 MB in  234 files
```

### Output (Signatures Mode, `-s`)

Not yet implemented.

### Output (With `--hardlink`, skipping already-hardlinked files)

```
Duplicate: '/path/to/original.jpg'
With:      '/path/to/copy.jpg'

Files:    1234 MB in 5000 files
Dupes:     100 MB in  234 files
```

Hardlinked pairs (same inode) are silently skipped and not counted as duplicates.

With `-v`, each skipped hardlinked pair is logged:
```
Already hardlinked: '/path/to/a.txt' and '/path/to/b.txt'
```

## `finddupe dedupe` — Scan and Eliminate

```
finddupe dedupe [flags] <path/pattern> [path/pattern...]
```

Scans the specified paths/patterns for duplicate files and takes action on them.

### Action Flags (Mutually Exclusive)

| Flag | Short | Description |
|------|-------|-------------|
| `--delete` | `-d` | Delete duplicate files |
| `--hardlink` | `-H` | Replace duplicates with hardlinks to the original |
| `--cow` | `-c` | Replace duplicates with CoW (Copy-on-Write) clones |

Exactly one action flag must be specified for `dedupe` mode. If none is specified, the command exits with an error.

### Other Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--sigs` | `-s` | bool | false | Print file signatures (no actions taken) |
| `--verbose` | `-v` | bool | false | Verbose output |
| `--zero` | `-z` | bool | false | Include zero-length files |
| `--no-progress` | `-p` | bool | false | Hide the progress indicator |
| `--follow-symlinks` | `-j` | bool | false | Follow symbolic links / reparse points |
| `--threads` | `-t` | int | `GOMAXPROCS` | Number of scanner workers |
| `--rdonly` | `-r` | bool | false | Also operate on read-only files (Windows) |
| `--ref` | — | string (repeatable) | — | Mark following path/pattern as reference (compare against but never act on) |

### Output

```
Deleted:    /path/to/duplicate1.txt
Hardlinked: /path/to/duplicate2.jpg

Files:    1234 MB in 5000 files
Dupes:     100 MB in  234 files
  2 files deleted
  1 files replaced with hardlinks
```

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

1. **Dedupe requires an action**: At least one of `--delete`, `--hardlink`, or `--cow` must be specified
2. **Actions are mutually exclusive**: Only one of `--delete`, `--hardlink`, `--cow` may be specified
3. **`--hardlink` semantics differ by mode**:
   - `find --hardlink` (`-H`): skip already-hardlinked files during duplicate detection
   - `dedupe --hardlink` (`-H`): replace duplicates by creating hardlinks to the original
4. **Paths are required**: At least one path/pattern argument must be provided
5. **Ref paths**: `--ref` can appear before any path argument to mark it as a reference. Multiple `--ref` flags can be used
6. **Threads must be positive**: If `--threads` is 0 or negative, default to `runtime.NumCPU()`
7. **Cross-drive hardlinks**: On Windows, hardlinking across different drives is impossible — the pipeline checks this

## Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--help` | `-h` | Show help for any command |
| `--version` | `-v` | Show version (only on root command) |

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Error (invalid arguments, I/O failure, etc.) |
| 2 | Interrupted (SIGINT/SIGTERM) |

## Flag Mapping: C Original to Go

| C Flag (`finddupe.exe`) | Go `finddupe find` | Go `finddupe dedupe` | Notes |
|--------------------------|-------------------|----------------------|-------|
| `-hardlink` | `-H, --hardlink` (skip hardlinked) | `-H, --hardlink` (create hardlinks) | Different semantics per mode |
| `-del` | N/A | `-d, --delete` | |
| N/A | N/A | `-c, --cow` | New: CoW clone |
| `-bat <file>` | N/A | `--bat <file>` | |
| `-v` | `-v, --verbose` | `-v, --verbose` | |
| `-sigs` | `-s, --sigs` | `-s, --sigs` | Not yet implemented |
| `-rdonly` | N/A | `-r, --rdonly` | |
| `-ref` | `--ref` | `--ref` | Changed from flag to inline marker |
| `-z` | `-z, --zero` | `-z, --zero` | |
| `-u` | N/A | N/A | C: suppress warnings. Go: use `-v` |
| `-p` | `-p, --no-progress` | `-p, --no-progress` | |
| `-j` | `-j, --follow-symlinks` | `-j, --follow-symlinks` | |
| `-listlink` | N/A | N/A | C: list hardlink groups. Go: not implemented |
| N/A | `-t, --threads <n>` | `-t, --threads <n>` | New (Go multi-threading) |
