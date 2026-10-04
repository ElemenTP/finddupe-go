# Cross-Platform Considerations

## Overview

finddupe-go targets three platforms: **Linux**, **macOS**, and **Windows**. Platform-specific code is isolated behind build-tagged files (`_unix.go`, `_linux.go`, `_darwin.go`, `_windows.go`, `_other.go`).

## Platform Abstraction Strategy

```
internal/
├── checksum/
│   ├── inode_unix.go      // //go:build unix     → fileIdentity
│   └── inode_windows.go   // //go:build windows  → fileIdentity
├── fswalker/
│   ├── walker_unix.go     // //go:build unix     → getFileIdentity
│   └── walker_windows.go  // //go:build windows  → getFileIdentity
├── extent/
│   ├── query_linux.go     // //go:build linux    → FIEMAP
│   ├── query_darwin.go    // //go:build darwin   → getattrlist ATTR_CMNEXT_CLONEID
│   ├── query_windows.go   // //go:build windows  → FSCTL_GET_RETRIEVAL_POINTERS
│   └── query_other.go     // //go:build !linux && !darwin && !windows
└── action/
    ├── hardlink_unix.go   // //go:build unix
    ├── hardlink_windows.go// //go:build windows
    ├── clone_linux.go     // //go:build linux    → FICLONE
    ├── clone_darwin.go    // //go:build darwin   → clonefile(2)
    ├── clone_windows.go   // //go:build windows  → FSCTL_DUPLICATE_EXTENTS_TO_FILE
    ├── clone_other.go     // //go:build !linux && !darwin && !windows
    ├── replace_unix.go    // //go:build unix     → os.Rename
    └── replace_windows.go // //go:build windows  → MoveFileEx
```

## 1. Inode / File Index Retrieval

### Strategy

Identity retrieval has two entry points with the same `(dev, inode, numLinks)` result:

- `checksum.fileIdentity(f *os.File)` — called in the **parallel worker pool** after `os.Open`.
- `fswalker.getFileIdentity(path, info fs.FileInfo)` — called from the walker; on Unix it uses the already-available stat struct, on Windows it returns zeros.

This split matters for performance: on Windows `GetFileInformationByHandle` requires a handle, and opening files in the walker would serialize all I/O and kill multi-threading.

### Unix (Linux, macOS)

`checksum/inode_unix.go`:
```go
func fileIdentity(f *os.File) (uint64, uint64, uint64) {
    info, _ := f.Stat()
    stat, ok := info.Sys().(*syscall.Stat_t)
    // read stat.Dev, stat.Ino, stat.Nlink
}
```

`fswalker/walker_unix.go` reads the same fields directly from `info.Sys().(*syscall.Stat_t)` inside the walk callback (essentially free).

### Windows

`checksum/inode_windows.go`:
```go
func fileIdentity(f *os.File) (dev, inode, numLinks uint64) {
    var info syscall.ByHandleFileInformation
    syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info)
    dev = uint64(info.VolumeSerialNumber)
    inode = (uint64(info.FileIndexHigh) << 32) | uint64(info.FileIndexLow)
    numLinks = uint64(info.NumberOfLinks)
    return dev, inode, numLinks
}
```

The NTFS file index serves the same role as the Unix inode, and the volume serial number is the `Dev` component. `GetFileInformationByHandle` is called on the already-open handle from `os.Open` — no separate `CreateFile` call.

### Device Matters

`Dev` is part of the identity because inode numbers are only unique per device. Both hardlink detection (`--hardlink`, `--listlink`) and CoW extent comparison compare `(Dev, Inode)` / `Dev`, so files on different volumes are never confused.

### Fallback

If identity retrieval fails, `Dev`, `Inode`, and `NumLinks` are 0. Duplicate detection still works via checksum, but `--hardlink`/`--listlink` are ineffective and CoW detection cannot compare physical extents.

## 2. Hardlink Creation

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
- **NTFS hardlink limit**: 1023 links per file. Checked against `NumLinks` before linking (`ResultHardlinkLimit`).
- **Administrator privileges**: May be required (caller's responsibility).
- **Cross-drive**: Impossible. Hardlinks must be on the same volume.

## 3. CoW (Copy-on-Write) Clone

CoW elimination is implemented for each platform. `cloneReplace` in `internal/action/cow.go` clones the keeper into a temporary file next to the victim, preserves the victim's mode/mtime, and atomically replaces it. Unsupported filesystems return `ErrCoWNotSupported` and the victim is left untouched.

### Linux — `FICLONE`

`clone_linux.go` opens the clone destination with `O_CREATE|O_EXCL` and calls `unix.IoctlFileClone` (`FICLONE`) on the destination handle with the source handle. Supported on **btrfs** and **XFS** (reflink). Unsupported errors (`EOPNOTSUPP`, `ENOTTY`, `EINVAL`, `EXDEV`, `ENOSYS`) are joined with `ErrCoWNotSupported`.

### macOS — `clonefile(2)`

`clone_darwin.go` calls `unix.Clonefile(src, dst, 0)`. Supported on **APFS**. `clonefile` requires the destination not to exist, which `cloneReplace` guarantees by removing the temporary file first.

### Windows — `FSCTL_DUPLICATE_EXTENTS_TO_FILE`

`clone_windows.go` implements block cloning on **ReFS** (including Dev Drive):
1. Determine the volume cluster size with `GetDiskFreeSpaceW` (cached per volume root).
2. Create the destination and preallocate it with `SetFileInformationByHandle(FileAllocationInfo)`.
3. Duplicate whole clusters with `DeviceIoControl(FSCTL_DUPLICATE_EXTENTS_TO_FILE)`.
4. Copy the unaligned tail with a normal read/write.

NTFS returns `ERROR_INVALID_FUNCTION`; unsupported errors are joined with `ErrCoWNotSupported`.

### Atomic Replace

- **Unix** (`replace_unix.go`): `os.Rename(tmp, dst)` — atomic within a filesystem.
- **Windows** (`replace_windows.go`): `MoveFileEx(from, to, MOVEFILE_REPLACE_EXISTING)`.

## 4. Extent Querying (CoW Detection)

`find --cow` uses `internal/extent` to measure how much of each member of an
identical-content group is already shared. `Query(path) ([]Extent, error)` is
implemented per platform; `Supported()` reports whether the platform has an
implementation, and `ErrUnsupported` is returned when the filesystem cannot
report extents.

### Linux — FIEMAP

`query_linux.go` issues the `FS_IOC_FIEMAP` ioctl (`0xC020660B`) in batches, with `FIEMAP_FLAG_SYNC` so freshly written files are written back before mapping (otherwise dirty files report zero-length extents). Extents flagged `FIEMAP_EXTENT_ENCODED` or `FIEMAP_EXTENT_UNKNOWN` are marked `Encoded` and excluded from physical comparison; the `FIEMAP_EXTENT_SHARED` flag sets `Shared`.

### macOS — `fcntl(F_LOG2PHYS_EXT)` via libSystem

`query_darwin.go` enumerates extents with `fcntl(fd, F_LOG2PHYS_EXT, &l2p)`,
called through the **libSystem wrapper** (`unix.FcntlInt`, no raw syscall, no
cgo): `l2p_devoffset` is the input logical offset and the output device byte
offset, `l2p_contigbytes` is the input query length and the output length of the
contiguous run. The loop advances by that run length, which turns APFS into a
Linux-FIEMAP equivalent: a 1 MiB file typically resolves in one or two calls.

The struct must be reproduced byte-exactly. `<sys/fcntl.h>` declares it under
`#pragma pack(4)`, so `l2p_contigbytes` is at offset **4** and `l2p_devoffset` at
offset **12** (`sizeof = 20`):

```c
#pragma pack(4)
struct log2phys {
	unsigned int l2p_flags;        /* offset 0  */
	off_t        l2p_contigbytes;  /* offset 4  */
	off_t        l2p_devoffset;    /* offset 12 */
};
```

A Go struct with natural alignment puts the fields at 8 and 16 and then reads the
kernel's output from the wrong bytes — that alignment bug (contigbytes read as
garbage or 0, so the walk stopped immediately) is what made an earlier attempt
conclude, wrongly, that `F_LOG2PHYS_EXT` was unusable on macOS.

APFS returns `ENOTSUP` for decmpfs-compressed files: the whole file lives in a
compressed container and the kernel refuses to map it. Those files fall back to
`getattrlist(path, ...)` with `ATTR_CMNEXT_CLONEID` (bit `0x100`), turned into one
synthetic `Extent` whose `Physical` field carries the **APFS clone ID**, marked
`Opaque`: all files of one clone family report the same value, independent files
report different ones. Opaque extents are compared by exact key only and never
range-intersected, because unrelated clone IDs are numerically adjacent. That is
family-level only (100% or 0% shared, never partial), which the docs and
`Identity()` state explicitly:

```
physical offset (F_LOG2PHYS_EXT), APFS clone id for compressed files
```

Failures (`EINVAL`, `ENOTSUP`, `EOPNOTSUPP`, `ENOTTY`, a missing clone-ID
attribute, or a zero ID) become `ErrUnsupported`, so a volume that can do neither
reports "extent information unavailable" rather than a misleading 0%.

Verified on a Darwin 27.0.0 / macOS 27 ARM64 APFS data volume:

- an uncompressed 1 MiB file resolves to two extents (a 4 KiB run plus a
  1044480-byte run); a `cp -c` clone returns the *same* device offsets and run
  lengths, an independently written copy returns different ones;
- `find --cow` therefore reports real percentages for uncompressed files, and a
  clone whose first 256 KiB were rewritten with identical bytes (a COW split) is
  reported as partially shared rather than 100%;
- after `dedupe --cow` the victim maps to the original's extents, `find --cow`
  reports 100%, and a second `dedupe --cow` is a no-op (`Dupes: 0`);
- `afsctool -c`-compressed files (and `cp -c` clones of them) return `ENOTSUP`
  from `F_LOG2PHYS_EXT`, take the clone-ID fallback, and still report 100%/0%;
  the clone is byte-identical;
- `clonefile(2)` preserves *the source's* compression. `dedupe --cow` clones each
  victim from the group keeper, so when the keeper is uncompressed (which file is
  first depends on directory order) compressed members become uncompressed clones
  of equal content. The dedupe result is correct either way, but the compression
  saving is lost in that case;
- APFS native compression is exposed through the `SF_COMPRESSED` flag
  (`ls -lO` prints `compressed`), not as a readable `com.apple.decmpfs` xattr.

`getattrlist` itself is still invoked through the raw syscall trap because
`golang.org/x/sys/unix` (through v0.48.0) exports no libSystem wrapper for it —
it provides `Setattrlist` and the deprecated `SYS_*` numbers only — and cgo would
break the `CGO_ENABLED=0` release and cross-compiled bundles. It is now only the
compressed-file fallback, is isolated in one function, and degrades to
`ErrUnsupported` on any failure. If x/sys adds `Getattrlist`, that single call
site can switch to it.

### Windows — `FSCTL_GET_RETRIEVAL_POINTERS`

`query_windows.go` calls `DeviceIoControl(FSCTL_GET_RETRIEVAL_POINTERS)` with a `STARTING_VCN_INPUT_BUFFER`, growing the buffer on `ERROR_MORE_DATA`. The returned VCN→LCN pairs are converted to byte offsets using the volume cluster size from `GetDiskFreeSpaceW` (cached), and `Logical` is the VCN-derived byte offset. ReFS block clones make two files reference the same LCNs, so overlapping mappings reveal shared extents.

Verified on a ReFS 3.14 Dev Drive (Windows 11, 4 KiB cluster): independent copies
map to different LCNs and report 0%; after `dedupe --cow` both files map to the
same runs (`28074434560+4096`, `28053598208+1044480`), `find --cow` reports 100%,
and a second `dedupe --cow` is a no-op. An existing hardlink is left untouched
and appears in `find --listlink`. ReFS has no NTFS-style transparent compression,
and files smaller than one cluster are reported as unsupported rather than
silently copied.

### Other Platforms

`query_other.go` reports `Supported() == false` and `Query` always returns `ErrUnsupported`.

### Comparing Extents

`find --cow` runs one `CoWDetect` execution per identical-content group and
computes, for every member, how many bytes are already shared (`FileShared`).

`extent.Equal(a, b)` is the "already sharing, skip the work" fast path used by
`dedupe --cow`: it is true only when both lists are non-empty, have the same
length in the same logical order, every extent has equal
`Logical`/`Physical`/`Length`, and no `Physical` is 0. `Encoded` (compressed)
extents are compared as well, because an exact start+length match remains a
sound identity signal under compression — only the range-overlap arithmetic is
unreliable there. It stays conservative: anything uncertain compares unequal and
the caller clones anyway, which is safe because content equality was already
established.

For the group ratios there are two signals:

1. `extent.SharedFlagBytes(e)` sums the lengths of extents the filesystem marked
   `Shared` (Linux `FIEMAP_EXTENT_SHARED`). It is a per-file signal: it says the
   extent is shared with *someone*, not with whom. It is used for the whole group
   as soon as any member's extents carry the flag.
2. `extent.SharedWithOthers(own, others)` intersects `own`'s physical ranges
   with the union of the other members' ranges. Allocated extents of different
   files never overlap unless the blocks are shared, so this counts a shared run
   even when the filesystem splits it at different boundaries in each file (an
   APFS clone whose first blocks were rewritten keeps sharing its untouched tail,
   reported as an extent starting mid-way through the original's run).
   Encoded (compressed) and Opaque (clone-ID) extents fall back to an exact
   key match, because their identities are not byte ranges.
   It is used where no shared flag exists, and only between members on the same
   device. On macOS it is the device offset from `F_LOG2PHYS_EXT` for
   uncompressed files (partial ratios included) and the APFS clone ID for
   decmpfs-compressed files (family-level: 100% or 0%).

`extent.SharedBytes(a, b)` (pairwise overlap) is kept as a library helper:

1. It sums the overlap of **non-encoded physical** ranges (the primary signal).
2. If that yields nothing, it falls back to comparing the **logical** ranges of extents the filesystem marked `Shared` — needed for compressed btrfs, where physical offsets are not comparable.

### Compressed-btrfs Caveat

On btrfs with compression, extents are reported as `Encoded`: their physical offsets and logical lengths cannot be compared directly, so the physical-identity path (`SharedWithOthers`) skips them. In-group detection then depends on the filesystem's `FIEMAP_EXTENT_SHARED` hint (`SharedFlagBytes`); if the kernel does not set that hint, two compressed clones may not be reported as sharing. The pairwise `SharedBytes` helper still offers its shared-logical-range fallback.

### Compression State and Clone Sources

A CoW clone inherits the **source's** extent layout, and compression lives in the
extent layout: on btrfs extents are compressed only if the source's are (encoding
is decided when the data is written), and an APFS decmpfs-compressed file's data
lives in a compressed container. Cloning an uncompressed source over a compressed
member therefore yields a byte-identical but **uncompressed** clone.

`dedupe --cow` clones every member from the group keeper, and the keeper is the
first file of the group in walk order. When that keeper is uncompressed, a
compressed member comes out uncompressed: content and sharing are correct, the
compression saving is lost. Re-compressing afterwards is not a fix — it allocates
new extents and breaks the sharing dedupe just created.

The tool deliberately does not try to preserve compression by preferring a
compressed source. Compression state is a per-file property that content equality
does not capture, and the detector emits eliminations pair-wise as soon as a file
is hashed, so a compressed member discovered later could only be honoured by
retroactively re-cloning members that were already processed. Doing that properly
needs elimination deferred until a whole hash bucket is complete, plus matching
accounting changes, and it would also change which path survives in
`dedupe --delete` / `--hardlink`. The practical guidance instead:

- compress the members of a group before running `dedupe --cow`, so that the
  keeper is compressed too, and verify with `afsctool -v` (APFS) or
  `filefrag -v` (btrfs);
- or accept the uncompressed result — `find --cow` reports sharing, not
  compression.

### Same-Device Rule

Physical identities are only comparable within one device (an APFS clone ID is
per-volume as well). `extent.SharedWithOthers` is therefore only given the
members whose non-zero `Dev` matches the file being measured; a member on a
different volume contributes nothing. The `FIEMAP_EXTENT_SHARED` flag path does
not need this check, because the kernel already knows whether the extent is
shared.

### Validating Extent APIs on Real Machines

Linux is exercised by the automated test suite. macOS (`F_LOG2PHYS_EXT`)
and Windows (`FSCTL_GET_RETRIEVAL_POINTERS`) need a real APFS/ReFS machine, so two
diagnostic tools exist:

- `testtools/extentdump` is a small CLI (`extentdump <file>...`) that prints, per
  file, `dev`/`inode`/`numLinks` (via `checksum.ComputeFileInfo`) and every
  extent's `logical`/`physical`/`length`/`shared`/`encoded`, plus
  `sharedFlagBytes`. It shows exactly what finddupe sees on that platform.
- `testscripts/build-bundles.sh` cross-compiles `finddupe` and `extentdump` for
  linux-amd64, darwin-amd64/arm64, and windows-amd64/arm64 into
  `bin/cow-test/<platform>/`, together with a probe script and a README. The same
  bundles can be produced with `make cow-test-bundles`. `bin/` is gitignored, so
  the bundles are build output, not source.
- `testscripts/cow-probe.sh` (macOS/Linux bash) and
  `testscripts/cow-probe-windows.ps1` (Windows PowerShell) run the end-to-end
  scenarios on the target machine: independent copies, `dedupe --cow` and a
  second run, a pre-existing clone, an existing hardlink (which must stay
  untouched), and compressible content. Each prints clearly marked sections for
  the user to send back.

See `testscripts/README.md` for instructions and what the numbers mean.

## 5. Read-Only Files

### Unix

Read-only is determined by file permission bits (`info.Mode().Perm()&0200 == 0`). To make writable, `os.Chmod(path, mode|0200)`.

### Windows

Read-only is determined by `FILE_ATTRIBUTE_READONLY`; Go normalizes this to the Unix permission model, so the same `&0200 == 0` check works.

## 6. Symlinks and Reparse Points

### Unix

Symbolic links are detected via `d.Type()&os.ModeSymlink`. The `-j` flag controls whether the walker follows them (with a `seen` set to prevent loops).

### Windows

Reparse points (junctions, symlinks, mount points) are not followed by default. The `-j` flag enables following.

## 7. Filesystem Feature Matrix

| Feature | Linux ext4 | Linux btrfs/xfs | macOS APFS | Windows NTFS | Windows ReFS |
|---------|------------|-----------------|------------|--------------|--------------|
| Hardlinks | ✓ | ✓ | ✓ | ✓ (≤1023) | ✓ |
| CoW clone (write) | ✗ | ✓ (FICLONE) | ✓ (clonefile) | ✗ | ✓ (FSCTL_DUPLICATE_EXTENTS_TO_FILE) |
| Extent query (`--cow`) | ✓ (FIEMAP) | ✓ (FIEMAP) | ✓ (F_LOG2PHYS_EXT; clone ID for compressed files) | ✓ (FSCTL_GET_RETRIEVAL_POINTERS) | ✓ (FSCTL_GET_RETRIEVAL_POINTERS) |
| Inode via stat | ✓ | ✓ | ✓ | ✗ | ✗ |
| Inode via handle | — | — | — | ✓ | ✓ |
| Long paths | N/A | N/A | N/A | ✓ (\\?\\) | ✓ (\\?\\) |
| Symlinks | ✓ | ✓ | ✓ | ✓ | ✓ |

Extent querying is best-effort: on a filesystem without support the query returns `ErrUnsupported`, `find --cow` still lists the group members but without per-file ratios, and the files are still counted as duplicates.

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
