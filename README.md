# finddupe-go

A fast, cross-platform duplicate file finder and eliminator written in Go with multi-threading support.

## Features

- **Multi-threaded scanning**: Uses worker pools to scan files in parallel for improved performance
- **Cross-platform**: Supports Linux, macOS, and Windows
- **Hard link detection**: List existing hard link groups with `find --listlink`
- **CoW detection**: Report identical-content groups and how much of each file is already CoW-shared with `find --cow`
- **Duplicate deletion**: Safely delete duplicate files
- **Duplicate hard link**: Create hard links to eliminate duplicate files and save disk space
- **Duplicate CoW**: Create CoW clones to eliminate duplicate files and save disk space on filesystems that support reflinks/block clones
- **Reference paths**: Protect a set of original files with `--ref`; they become keepers and are never eliminated
- **Unicode support**: Handles filenames with Unicode characters properly
- **Long path support**: Long path support for Windows

## Installation

### From Source

```bash
git clone <repository-url>
cd finddupe-go
make <your-os>-<cpu-arch>
```

The compiled binary will be in the `bin/` directory.

### Using Make

```bash
# Build for specific platforms
make linux-amd64
make darwin-arm64
make windows-amd64

# Build for all supported platforms
make all-arch
```

## Usage

### Basic Syntax

```bash
finddupe find [options] <path/pattern> [path/pattern...]
finddupe dedupe [options] <path/pattern> [path/pattern...]
```

### Examples

**Find and report duplicates in a directory:**
```bash
finddupe find /home/user/photos
```

**Find duplicates and delete them:**
```bash
finddupe dedupe --delete /data
```

**Replace duplicates with hard links (saves space):**
```bash
finddupe dedupe --hardlink /backup
```

**Replace duplicates with CoW clones (saves space on btrfs/XFS/APFS/ReFS):**
```bash
finddupe dedupe --cow /btrfs-volume
```

**List existing hard link groups:**
```bash
finddupe find --listlink /data
```

**Report identical-content groups and their CoW sharing:**
```bash
finddupe find --cow /btrfs-volume
```

**Protect original files while deduplicating copies:**
```bash
finddupe dedupe --delete --ref /originals -- /copies
```

**Use multiple threads for faster scanning:**
```bash
finddupe find --threads 8 /large/dataset
```

**Find duplicates in all .jpg files in a tree:**
```bash
finddupe find /photos/**/*.jpg
```

### Pattern Matching

finddupe supports flexible pattern matching:

- `/path/to/dir` - Scan all files in directory (recursive)
- `/path/to/dir/*.txt` - Scan all .txt files in directory
- `**/*.jpg` - Scan all .jpg files in current tree (recursive)
- `/path/**/*.txt` - Recursively scan all .txt files under path

### Options

find mode:

| Option | Description |
|--------|-------------|
| `-H, --hardlink` | Skip already-hardlinked files when reporting duplicates |
| `-l, --listlink` | List hardlink groups (files sharing a physical inode) and exit |
| `-c, --cow` | Report identical-content groups with the per-file share of already-shared bytes |
| `-v, --verbose` | Verbose output |
| `-z, --zero` | Include zero-length files |
| `-p, --no-progress` | Hide progress indicator |
| `-j, --follow-symlinks` | Follow symbolic links |
| `-t <n>, --threads <n>` | Number of worker threads (default: CPU count × 2) |
| `--ref <path>` | Mark the next path/pattern as reference (compare against, never act on); repeatable |

`--hardlink`, `--listlink`, and `--cow` are mutually exclusive in find mode.

dedupe mode (exactly one action is required):

| Option | Description |
|--------|-------------|
| `-d, --delete` | Delete duplicate files (conflicts with `-H` and `-c`) |
| `-H, --hardlink` | Create hardlinks to eliminate duplicates (conflicts with `-d` and `-c`) |
| `-c, --cow` | Create CoW clones to eliminate duplicates (conflicts with `-d` and `-H`) |
| `-r, --rdonly` | Also operate on readonly files (for Windows) |
| `-v, --verbose` | Verbose output |
| `-z, --zero` | Include zero length files |
| `-p, --no-progress` | Hide progress indicator |
| `-j, --follow-symlinks` | Follow symbolic links |
| `-t <n>, --threads <n>` | Number of worker threads (default: CPU count × 2) |
| `--ref <path>` | Mark the next path/pattern as reference files (not to be eliminated); repeatable |

> **Note:** `--sigs`/`-s` (signature printing) is not implemented; it was removed as dead code and is listed as future work in [docs/cli-spec.md](docs/cli-spec.md).

### Example Scenarios

**Clean up backup directory:**
```bash
# Review first
finddupe find /backup --verbose

# Then clean up
finddupe dedupe /backup --delete
```

**Deduplicate photo collection:**
```bash
finddupe dedupe ~/Photos --hardlink --threads 12
```

**Find duplicates across multiple drives:**
```bash
finddupe find /drive1 /drive2 /drive3 --verbose
```

## How It Works

1. **Scanning**: Multiple worker threads walk paths/globs and compute a fast 64-bit checksum of the first 32KB of each file, plus its device/inode/link metadata.
2. **Grouping**: A single coordinator goroutine owns a state machine (`dupe.Detector`) that groups files by `(signature, size)` and then by full SHA-256.
3. **Comparison strategy**: one file is stored without hashing; exactly two unhashed files are compared in chunks with early-stop; three or more files get a full parallel SHA-256 pass. This guarantees every duplicate of N identical files is reported.
4. **Execution**: Stateless executor workers hash, compare, and eliminate files as directed by the coordinator, feeding their results back for follow-up work.
5. **Action**: Either report, delete, replace with hard links, or replace with CoW clones. With `--ref`, reference files are walked first, become keepers, and are never eliminated.
6. **CoW report**: `find --cow` runs one final pass after all hashing has finished. Every identical-content group (one path per physical file; hardlinked aliases collapse) is reported with the already-shared fraction of each member. Independent copies are listed too, at 0% shared — they are exactly the files that should CoW-share.
7. **Safe CoW elimination**: `dedupe --cow` never acts on a pair that is the same physical file (an existing hardlink) and skips pairs whose extent layout already proves they share all storage, so re-running it is a no-op.

### CoW Output

`find --cow` prints one block per group, then a summary line:

```
CoW candidate group (2 files, identical content):
    '/data/b.bin'  shared: 100.0% (128 kB of 128 kB)
    '/data/a.bin'  shared:   0.0% (0 B of 128 kB)

  1 CoW groups found (128 kB of file bytes already shared)
```

The `X of file bytes already shared` total is a per-file sum: a shared range is
counted once per member, so it is not the amount of physical storage saved.

### CoW Diagnostics

Validating extent reporting on macOS (APFS) and Windows (ReFS) needs a real
machine. `testtools/extentdump` prints exactly what finddupe sees per file, and
`testscripts/build-bundles.sh` (`make cow-test-bundles`) builds per-platform
`finddupe` + `extentdump` bundles with a probe script under the gitignored
`bin/cow-test/`. See [testscripts/README.md](testscripts/README.md).

## Performance Tips

- Use `-t` or `--threads` to match your CPU core count for optimal performance
- SSD storage will provide significantly faster scanning than HDD
- Network drives will be slower due to I/O limitations
- The first scan of a directory will be slower as the OS caches file metadata
- `find --cow` costs an extra open + extent query per file, so use it only when you need CoW detection

## Platform-Specific Notes

### Linux/macOS
- Hard links work within the same filesystem
- Symbolic links are not followed by default (use `-j` to follow)
- File permissions are preserved when creating hard links and CoW clones
- **CoW clone** (`dedupe --cow`): Linux uses `FICLONE` on btrfs/XFS; macOS uses `clonefile(2)` on APFS
- **CoW detection** (`find --cow`): Linux uses FIEMAP; macOS uses `fcntl(F_LOG2PHYS_EXT)` through the libSystem wrapper, with the APFS clone ID (`getattrlist` `ATTR_CMNEXT_CLONEID`) as the fallback for decmpfs-compressed files, which the kernel refuses to map
- Verified on macOS 27 / APFS: uncompressed files report real per-extent sharing (including partial percentages), compressed files report family-level 100%/0%, and a re-run of `dedupe --cow` is a no-op
- `clonefile(2)` keeps the *source's* compression: `dedupe --cow` clones from the group keeper, so a compressed member cloned from an uncompressed keeper loses its compression (content is unchanged)
- On unsupported filesystems the CoW clone is refused and the victim is left untouched (`ErrCoWNotSupported`)
- `dedupe --cow` never touches a pair that is already the same physical file, so an existing hardlink is preserved

### Windows
- Hard links require NTFS
- **CoW clone** (`dedupe --cow`) requires ReFS (including Dev Drive) via `FSCTL_DUPLICATE_EXTENTS_TO_FILE`; NTFS is not supported
- **CoW detection** (`find --cow`) uses `FSCTL_GET_RETRIEVAL_POINTERS`; verified on a ReFS 3.14 Dev Drive, including idempotent re-runs and untouched hardlinks
- Administrator privileges may be needed for some operations
- Long paths (260+ characters) are fully supported

## Comparison with Original C Version

| Feature | C Version | Go Version |
|---------|-----------|------------|
| Multi-threading | No | Yes |
| Cross-platform | Windows only | Linux, macOS, Windows |
| Hard link listing | Yes | Yes (`find --listlink`) |
| CoW clone | Partial | Yes (Linux/macOS/Windows, per-filesystem) |
| CoW detection | No | Yes (`find --cow`) |
| Unicode support | Limited | Full |
| Long paths | Limited | Full |
| Performance | Good | Better (multi-threaded) |

## License

Original C version by Matthias Wandel.
Go rewrite with multi-threading support by ElemenTP.
MIT License

## Contributing

Contributions are welcome! Please feel free to submit issues or pull requests.
