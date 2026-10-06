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
| `--hardlink` | `-H` | bool | false | Skip already-hardlinked files when reporting duplicates. Files sharing the same `(Dev, Inode)` (already hardlinked to each other) are not reported; like every same-inode pair they are never counted as duplicate storage. |
| `--listlink` | `-l` | bool | false | List hardlink groups (files sharing a physical inode) and exit. Skips duplicate detection entirely. |
| `--cow` | `-c` | bool | false | CoW group mode: report every identical-content group with the per-file share of already-shared bytes. |
| `--verbose` | `-v` | bool | false | Verbose output: show hardlink skip details, file index information |
| `--zero` | `-z` | bool | false | Include zero-length files (skipped by default) |
| `--no-progress` | `-p` | bool | false | Hide the progress indicator |
| `--follow-symlinks` | `-j` | bool | false | Follow symbolic links / reparse points: they are resolved and scanned as their target, under the target's own path |
| `--threads` | `-t` | int | `0` (→ `runtime.NumCPU() × 2`, capped at 1024) | Number of scanner workers |
| `--ref` | — | string (repeatable) | — | Consume the following path/pattern as a reference (compare against, but never act on). Can be repeated. |

`--hardlink`, `--listlink`, and `--cow` are **mutually exclusive**. Selecting more than one fails with:

```
only one of --hardlink, --listlink, or --cow may be used
```

There is no `--sigs`/`-s` flag: signature printing was removed as dead code and is noted as future work in the [C flag mapping](#flag-mapping-c-original-to-go).

### Arguments

One or more path/pattern arguments. Each can be:
- An existing file or directory path → scanned as written (a directory
  recursively, equivalent to `dir/**`). This wins over glob interpretation, so a
  path whose name contains glob characters (`/data/[2020] photos`) works.
- A glob pattern (when nothing exists at the literal path) → matching files are
  scanned. `**` matches zero or more directory components.

### `--ref` Behavior

`--ref <path/pattern>` marks the following argument as a reference path. Reference paths are:
- Walked **first**, and their whole checksum phase is drained before any normal file is submitted, so reference files are inserted first and become the **keeper** of their content group.
- Never eliminated: the executor returns `ResultSkippedRef` when a duplicate victim is a reference file (unless the action is `find`'s report-only mode). They are counted in `SkippedRefFiles`.

`--ref` is also the only way to control **which** of several identical files
survives. Without it the keeper is the first member in the keeper policy's order
(references first, then the file with more hardlinks, then the smallest path), so
the choice is reproducible and does not depend on how the parallel workers were
scheduled. The policy is applied once the whole scan is known, at the end of the
run, so no member is decided before its group is complete.

This differs from the original Windows finddupe, where `-ref` was a terminator
("everything after this argument is a reference") and references were walked
last, so a normal file matching a reference was never eliminated and the file
*outside* the reference set survived. In the Go version the reference path holds
the surviving file.

### Output (Normal Mode)

```
Duplicate: '/path/to/original.jpg'
With:      '/path/to/copy.jpg'
Duplicate: '/path/to/original.jpg'
With:      '/path/to/hardlinked-copy.jpg'
    (hardlinked instances of same file)

Files:    1234 MB in  5000 files
Dupes:     100 MB in   234 files
  5 files of zero length were skipped
  2 files could not be opened
```

A pair whose two paths are the same physical file (the same non-zero `(Dev, Inode)`,
i.e. an existing hardlink) is listed with the `(hardlinked instances of same file)`
tag, the way the original Windows tool tags it. It is a duplicate *name*, not a
second copy of the bytes, so it never counts in `Dupes:` — see
[algorithm.md](algorithm.md#what-the-dupes-line-counts).

### Output (Verbose Mode, `-v`)

In verbose mode skipped hardlinked pairs are logged at info level:

```
time=... level=INFO msg="already hardlinked" keeper=/path/to/a.txt victim=/path/to/b.txt
Duplicate: '/path/to/original.jpg'
With:      '/path/to/copy.jpg'
```

### Output (With `--hardlink`)

Duplicates with different `(Dev, Inode)` are reported as usual. Pairs that are already the same physical file (same non-zero `Dev` and `Inode`) are not reported at all; with `-v` each pair is logged as `already hardlinked`. Without `--hardlink`, such a pair is listed with the `(hardlinked instances of same file)` tag shown above.

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

`find --cow` reports **groups**, not pairs. After the input is drained and all
hashing is finished, every SHA-256 bucket with at least two distinct physical
files becomes one group (hardlinked aliases collapse to a single member), and one
`CoW candidate group` block is printed with the already-shared fraction of every
member. Independent copies are listed too, with 0% shared — they are exactly the
files that should end up CoW-sharing:

```
CoW candidate group (2 files, identical content):
    '/data/b.bin'  shared: 100.0% (128 kB of 128 kB)
    '/data/a.bin'  shared:   0.0% (0 B of 128 kB)

Files:     256 kB in      2 files
Dupes:     128 kB in      1 files
  1 CoW groups found (128 kB of file bytes already shared)
```

`FailedFiles` (`N files could not be processed`) counts eliminations that were
attempted and failed — a CoW clone on a volume that refuses it, for example. Such a
pair is left untouched and the run continues; the failure is also logged at error
level with the sentence joined onto one line.

`CoWSharedBytes` (the `X of file bytes already shared` total) is a per-file sum:
each shared range is counted once per member, so it must not be read as physical
bytes saved. When extent information is unavailable for the whole group, the
members are still listed with a note instead of per-file ratios.

Detection costs an extra open + extent query per file, which is why it is opt-in.
The ratio always answers the same question — how much of this file is shared with
another member of its group — by comparing physical extents within the same device
(Linux/macOS/Windows, and the APFS clone ID for compressed files). The kernel's
per-extent `FIEMAP_EXTENT_SHARED` flag is a different question ("shared with
someone") and is only a diagnostic. See [cross-platform.md](cross-platform.md).

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
- `--prefer-compressed` without `--cow` → `--prefer-compressed only applies to --cow: the clone source is what decides the layout`

### Other Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--verbose` | `-v` | bool | false | Verbose output |
| `--zero` | `-z` | bool | false | Include zero-length files |
| `--no-progress` | `-p` | bool | false | Hide the progress indicator |
| `--follow-symlinks` | `-j` | bool | false | Follow symbolic links / reparse points: they are resolved and scanned as their target, under the target's own path |
| `--threads` | `-t` | int | `0` (→ `runtime.NumCPU() × 2`, capped at 1024) | Number of scanner workers |
| `--rdonly` | `-r` | bool | false | Also operate on read-only files (skipped by default on every platform) |
| `--prefer-compressed` | `-C` | bool | false | `--cow` only: keep a compressed member as the clone source (see below) |
| `--interactive` | `-i` | bool | false | Ask which file to keep for every identical-content group (needs a terminal on stdin) |
| `--ref` | — | string (repeatable) | — | Consume the following path/pattern as a reference (compare against, but never act on) |

**Interactive keeper (`-i, --interactive`)**

Duplicates are decided after the whole scan, when every member of a content group
is known, so `-i` simply asks instead of applying the order above:

- the group is listed with size, modification time, hardlink count and compression
  state (and whether the file is a reference);
- a number keeps that file and eliminates the others;
- `a` keeps the default (policy) choice for this and every later group, so a scan
  with thousands of groups does not have to be answered one by one;
- `s` leaves this group alone; `q` leaves this group and every later group alone;
- an unusable answer is re-asked at most three times, then the group is left alone,
  and end of input stops the questions.

The listing goes to stdout with the rest of the report; the prompt goes to stderr so
the stdout stream stays parseable. A terminal on stdin is required — without one the
run fails with `--interactive needs a terminal on stdin to ask about each group`
rather than pretending every group was handled. A run driven by answers is, by
definition, not reproducible; the default keeper order is used for everything the
user does not answer.

**Keeper order** (which of several identical files is kept): reference files first,
then `--prefer-compressed` (when given), then the file with more hardlinks, then the
smallest path. With `--interactive` the choice is asked per group instead (see
below). A CoW clone inherits the **source's** extent layout, so keeping a
compressed member keeps the whole group compressed, while keeping an uncompressed
member spreads its uncompressed layout to every victim. `--hardlink` and `--delete`
do not change the data layout, which is why the flag is limited to `--cow`.

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

Results and the summary are printed on **stdout**; warnings, errors and the
progress line go to **stderr**. Paths are enclosed in single quotes, and control
characters in a path are escaped, so a crafted file name cannot forge extra
result lines.

Read-only victims that are skipped print `Skipping duplicate readonly file '<path>'.` and increment `SkippedROFiles`. Reference victims increment `SkippedRefFiles` and print nothing. Pairs that are already the same physical file (a hardlink) are never touched: `--delete` and `--hardlink` return `ResultAlreadyHardlinked`, and `--cow` returns `ResultAlreadyHardlinked` for the same physical file or `ResultAlreadyShared` when the two extent layouts are provably identical on the same device; in verbose mode these are logged (`already hardlinked` / `already shared`) and nothing is printed otherwise.

An action runs once per **duplicate path**, not once per physical file. If a file that
is being replaced or deleted still has other hardlinks, those other names are acted on
too: replacing one name of an inode only moves that name, so leaving the rest behind
would keep a second copy of the content and the next run would find it again.

Two safety skips protect a live filesystem:

- A pair whose keeper or victim no longer matches the size and modification time recorded when its content was hashed prints `Skipping '<victim>' (original '<keeper>'): one of them changed during the scan.` and increments `SkippedChangedFiles` (summarized as `N files skipped (changed during the scan)`). The same check runs inside the readers: a file that changed size (or, when resuming a partial digest, was modified) fails with `dupe.ErrFileChanged` and is counted as unreadable instead of being hashed.
- `dedupe --hardlink` refuses a pair on two different devices (a hardlink cannot span volumes) and logs `hardlink not possible across devices`, leaving both files untouched (`ResultSkippedCrossDevice`). `dedupe --cow` refuses the same pair for the same reason — storage blocks cannot be shared across volumes — instead of reporting that the filesystem does not support CoW. When a device is unknown (`Dev == 0`, identity unavailable) the attempt is made and a volume-boundary error from the clone is still reported as the same skip.

An action that fails logs `action failed` with both paths and the underlying error, for example the `ErrCoWNotSupported` message when `--cow` meets a filesystem without reflink support.

A pattern that matches no files at all fails the run: the walker reports
`no files matched "<pattern>"` and the process exits non-zero, after printing the
summary for whatever did match. An empty directory, a glob with no hits and a
misspelled path all behave this way, so a typo cannot pass for a successful
cleanup.

The progress line is only drawn when stderr is a terminal; redirected output gets
no escape sequences and no "Scanned N files..." chatter, and the line is cleared
before the summary is printed.

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
7. **Threads**: `--threads 0` (the default) uses `runtime.NumCPU() × 2` workers; negative values are also treated as the default, and the value is capped at 1024 (each worker owns pipeline slots and goroutines)
8. **Reference files are never eliminated**: a reference duplicate is reported as skipped, even in `dedupe` mode

## Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--help` | `-h` | Show help for any command |

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| Non-zero (1) | Error (invalid arguments, I/O failure, a pattern that matched no files), an elimination that failed (the summary reports `N files could not be processed`, the log has the reason), or interruption via SIGINT/SIGTERM |

Files that could not be *read* during the scan stay a warning: they are skipped and
counted, but they never change the exit code. A failed action is different — the
victim was left in place — so a script can rely on a zero exit meaning every
duplicate decision was carried out.

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
| `-ref` | `--ref` | `--ref` | Changed from a terminator to a repeatable value flag, and the surviving file is now the reference (see [`--ref` Behavior](#ref-behavior)) |
| `-z` | `-z, --zero` | `-z, --zero` | |
| `-u` | N/A | N/A | C: suppress warnings. Go: use `-v` |
| `-p` | `-p, --no-progress` | `-p, --no-progress` | |
| `-j` | `-j, --follow-symlinks` | `-j, --follow-symlinks` | |
| `-listlink` | `-l, --listlink` | N/A | C: list hardlink groups. Implemented for `find` |
| N/A | `-c, --cow` | N/A | New: report identical-content groups with per-file shared ratios |
| N/A | `-t, --threads <n>` | `-t, --threads <n>` | New (Go multi-threading) |

### Future Work

- `--sigs` (C `-sigs`): print computed file signatures without duplicate detection. The signature-printing config field and both command flags were removed as dead code.
