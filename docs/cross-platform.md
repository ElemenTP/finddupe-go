# Cross-Platform Considerations

## Overview

finddupe-go targets three platforms: **Linux**, **macOS**, and **Windows**. Platform-specific code is isolated behind build-tagged files (`_unix.go` / `_windows.go`).

## Platform Abstraction Strategy

```
internal/
├── checksum/
│   ├── inode_unix.go      // //go:build unix
│   └── inode_windows.go   // //go:build windows
├── fswalker/
│   ├── walker_unix.go     // //go:build unix
│   └── walker_windows.go  // //go:build windows
└── action/
    ├── hardlink_unix.go   // //go:build unix
    ├── hardlink_windows.go// //go:build windows
    ├── cow_unix.go        // //go:build unix
    └── cow_windows.go     // //go:build windows
```

## 1. Inode / File Index Retrieval

### Strategy

Inode retrieval happens in the **parallel worker pool goroutines**, not in the sequential walker. This is critical for performance: on Windows, `GetFileInformationByHandle` requires a file handle. Opening files in the walker would serialize all I/O and kill multi-threading.

### Unix (Linux, macOS)

`checksum/inode_unix.go`:
```go
func fileInode(f *os.File) (inode uint64, numLinks uint64) {
    info, _ := f.Stat()
    stat, ok := info.Sys().(*syscall.Stat_t)
    // read stat.Ino, stat.Nlink
}
```

Called from `checksum.ComputeFileInfo` after `os.Open`, so the file is already open.

### Windows

`checksum/inode_windows.go`:
```go
func fileInode(f *os.File) (inode uint64, numLinks uint64) {
    var info syscall.ByHandleFileInformation
    syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info)
    inode = (uint64(info.FileIndexHigh) << 32) | uint64(info.FileIndexLow)
    numLinks = uint64(info.NumberOfLinks)
}
```

The NTFS file index serves the same role as the Unix inode. `GetFileInformationByHandle` is called on the already-open handle from `os.Open` — no separate `CreateFile` call.

### Fallback

If inode retrieval fails, `Inode` and `NumLinks` are 0. Duplicate detection still works via checksum, but the `--hardlink` flag (skip same-inode pairs) will be ineffective.

## 2. Walker Inode Retrieval

### Unix

`fswalker/walker_unix.go`:
```go
func getInode(_ string, info fs.FileInfo) (inode uint64, numLinks uint64) {
    stat, ok := info.Sys().(*syscall.Stat_t)
    return stat.Ino, uint64(stat.Nlink)
}
```
Called from the walker callback — reads from the already-available stat struct (essentially free).

### Windows

`fswalker/walker_windows.go`:
```go
func getInode(_ string, _ fs.FileInfo) (inode uint64, numLinks uint64) {
    return 0, 0
}
```
Returns zero. Inode retrieval happens later in `checksum.ComputeFileInfo` where the file handle is already open and the operation is parallelized.

## 3. Hardlink Creation

### Unix

```go
func createPlatformHardlink(linkPath, targetPath string) error {
    return os.Link(targetPath, linkPath)
}
```
Go's `os.Link` wraps the `link(2)` syscall. Hardlinks must be on the same filesystem.

### Windows

```go
func createPlatformHardlink(linkPath, targetPath string) error {
    return os.Link(targetPath, linkPath)
}
```
Go's `os.Link` wraps `CreateHardLinkW`. Additional considerations:
- **NTFS hardlink limit**: 1023 links per file. Checked against `NumLinks` before linking.
- **Administrator privileges**: May be required (caller's responsibility).
- **Cross-drive**: Impossible. Must validate drives are the same.

## 4. CoW (Copy-on-Write) Clone

Not yet implemented — all platforms return `ErrCoWNotSupported`.

Planned implementations:
- **Linux**: `ioctl FICLONERANGE` (btrfs/xfs) via `golang.org/x/sys/unix`
- **macOS**: `clonefile(2)` via `golang.org/x/sys/unix`
- **Windows**: `FSCTL_DUPLICATE_EXTENTS_TO_FILE` via DeviceIoControl (ReFS only)

## 5. Read-Only Files

### Unix

Read-only determined by file permission bits. To make writable: `os.Chmod(path, 0666)`.

### Windows

Read-only determined by `FILE_ATTRIBUTE_READONLY`. Go normalizes this to the Unix permission model: `info.Mode()&0200 == 0` means read-only.

## 6. Symlinks and Reparse Points

### Unix

Symbolic links detected via `info.Mode()&os.ModeSymlink`. The `-j` flag controls whether `filepath.WalkDir` follows them.

### Windows

Reparse points (junctions, symlinks, mount points) are not followed by default. The `-j` flag enables following.

## 7. Filesystem Feature Matrix

| Feature | Linux ext4 | Linux btrfs/xfs | macOS APFS | Windows NTFS | Windows ReFS |
|---------|------------|-----------------|------------|--------------|--------------|
| Hardlinks | ✓ | ✓ | ✓ | ✓ (≤1023) | ✓ |
| CoW Clone | ✗ | ✓ (FICLONERANGE) | ✓ (clonefile) | ✗ | ✓ (FSCTL) |
| Inode via stat | ✓ | ✓ | ✓ | ✗ | ✗ |
| Inode via handle | — | — | — | ✓ | ✓ |
| Long paths | N/A | N/A | N/A | ✓ (\\?\\) | ✓ (\\?\\) |
| Symlinks | ✓ | ✓ | ✓ | ✓ | ✓ |

## 8. Path Separators

- Use `filepath.Join` and `filepath.FromSlash`/`filepath.ToSlash` for path operations
- Never hardcode `/` or `\`
- Glob patterns accept both `/` and `\` on Windows
- Output paths use the platform's native separator

## 9. Unicode

Go strings are UTF-8 natively:
- Linux/macOS: filenames are bytes, Go treats as UTF-8
- Windows: `os.Open` accepts UTF-8 and converts to UTF-16 internally (Go 1.16+)
- Paths from `filepath.WalkDir` are UTF-8 on all platforms
