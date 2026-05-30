# finddupe-go

A fast, cross-platform duplicate file finder and eliminator written in Go with multi-threading support.

## Features

- **Multi-threaded scanning**: Uses worker pools to scan files in parallel for improved performance
- **Cross-platform**: Supports Linux, macOS, and Windows
- **Hard link detection**: Find existing hard link groups
- **CoW detection**: Find CoW generated same files (if FS supports CoW)
- **Duplicate deletion**: Safely delete duplicate files
- **Duplicate hard link**: Create hard links to eliminate duplicate files and save disk space
- **Duplicate CoW**: Create CoW copies to eliminate duplicate files and save disk space (if FS supports CoW)
- **Unicode support**: Handles filenames with Unicode characters properly
- **Long path support**: long path for windows

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

**Use multiple threads for faster scanning:**
```bash
finddupe find --threads 8 /large/dataset
```

**Find duplicates in all .jpg files in a tree:**
```bash
finddupe find /photos/**/*.jpg
```

**Find existing hard link groups:**
```bash
finddupe find --hardlink /data
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
| `-h, --hardlink` | list hardlink groups |
| `-c, --cow` | list cow generated same copies |
| `-s, --sigs` | Print computed file signatures |
| `-v, --verbose` | Verbose output |
| `-z, --zero` | Include zero length files |
| `-p, --no-progress` | Hide progress indicator |
| `-j, --follow-symlinks` | Follow symbolic links |
| `-t <n>, --threads <n>` | Number of worker threads (default: CPU count) |

dedupe mode:
| Option | Description |
|--------|-------------|
| `-d, --delete` | Delete duplicate files (conflicts with -h and -c) |
| `-h, --hardlink` | Create hardlinks to eliminate duplicates (conflicts with -d and -c) |
| `-c, --cow` | Create cow dupes to eliminate duplicates (conflicts with -d and -h) |
| `-s, --sigs` | Print computed file signatures |
| `-r, --rdonly` | Also operate on readonly files (for Windows) |
| `-v, --verbose` | Verbose output |
| `-z, --zero` | Include zero length files |
| `-p, --no-progress` | Hide progress indicator |
| `-j, --follow-symlinks` | Follow symbolic links |
| `-t <n>, --threads <n>` | Number of worker threads (default: CPU count) |
| `--ref` | Mark following pattern as reference files (not to be eliminated), can use multiple times |

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

1. **Scanning**: Uses multiple worker threads to scan files in parallel
2. **Checkum Calculation**: Computes a fast checksum of the first 32KB of each file
3. **Grouping**: Groups files with matching checksums
4. **Verification**: Performs full byte-by-byte comparison on potential duplicates
5. **Action**: Either reports, deletes, or replaces duplicates with hard links

## Performance Tips

- Use `-t` or `--threads` to match your CPU core count for optimal performance
- SSD storage will provide significantly faster scanning than HDD
- Network drives will be slower due to I/O limitations
- The first scan of a directory will be slower as the OS caches file metadata

## Platform-Specific Notes

### Linux/macOS
- Hard links work within the same filesystem
- Symbolic links are not followed by default (use `-j` to follow)
- File permissions are preserved when creating hard links

### Windows
- Hard links require NTFS filesystem
- Administrator privileges may be needed for some operations
- Long paths (260+ characters) are fully supported

## Comparison with Original C Version

| Feature | C Version | Go Version |
|---------|-----------|------------|
| Multi-threading | No | Yes |
| Cross-platform | Windows only | Linux, macOS, Windows |
| Unicode support | Limited | Full |
| Long paths | Limited | Full |
| Modern features | No | Yes |
| Performance | Good | Better (multi-threaded) |

## License

Original C version by Matthias Wandel.
Go rewrite with multi-threading support by ElemenTP.
MIT License

## Contributing

Contributions are welcome! Please feel free to submit issues or pull requests.
