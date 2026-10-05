# CoW / extent platform probes

`find --cow` and `dedupe --cow` depend on platform extent APIs:

| Platform | Filesystem | Clone | Extent / sharing signal |
|---|---|---|---|
| Linux | btrfs, XFS | `FICLONE` ioctl | `FS_IOC_FIEMAP` + `FIEMAP_EXTENT_SHARED` |
| macOS | APFS | `clonefile(2)` | `fcntl(F_LOG2PHYS_EXT)`; `getattrlist` clone ID for compressed files |
| Windows | ReFS / Dev Drive | `FSCTL_DUPLICATE_EXTENTS_TO_FILE` | `FSCTL_GET_RETRIEVAL_POINTERS` |

Linux is covered by the automated test suite. macOS and Windows need a real
machine, so this directory contains probe scripts you run by hand and report
back.

## Build the bundles

```bash
./testscripts/build-bundles.sh
```

This writes `bin/cow-test/<platform>/` (and `.zip` if `zip` is installed):

- `linux-amd64`, `darwin-amd64`, `darwin-arm64`, `windows-amd64`, `windows-arm64`
- each contains `finddupe`, `extentdump`, `compressdump`, the probe script, and
  this README

Copy the bundle matching your machine to that machine. The probe creates its
files in a fresh `finddupe-cow-probe.<random>` subdirectory of the directory you
give it (and never deletes anything outside that subdirectory), so it is safe to
point at any writable location.

## macOS (APFS)

```bash
cd darwin-arm64
chmod +x cow-probe.sh finddupe extentdump compressdump
./cow-probe.sh ~/finddupe-cow-probe
```

Use a directory on an APFS volume (`diskutil info / | grep "File System"`).
The script exercises:

1. independent copies, `dedupe --cow`, then `find --cow` before/after;
2. a pre-existing `cp -c` clone;
3. an existing hardlink (must never be replaced by a clone);
4. compressed files (APFS decmpfs) and their clones;
5. a `darwinfiemap` investigation of whether `fcntl(F_LOG2PHYS_EXT)` can
   enumerate physical extents (four input conventions, plus file comparison).

APFS does not compress files automatically, so step 4 needs a compressor. If
`afsctool` is installed (`brew install afsctool`) the script runs
`afsctool -c` on two independently written copies, verifies the `compressed`
flag via `ls -lO`, and prints `afsctool -v`. `ditto --hfsCompression`
is used as a fallback, otherwise the files stay uncompressed. The section then
clones one of them with `cp -c`, reports `find --cow`, runs `dedupe --cow`
twice, and `cmp`s a cloned file against the source to prove content is intact.

## Windows (ReFS or Dev Drive)

```powershell
cd windows-amd64
powershell -ExecutionPolicy Bypass -File .\cow-probe.ps1 -Dir R:\finddupe-probe
```

`-Dir` must be on a ReFS volume or Dev Drive. The script prints
`Get-Volume` / `fsutil fsinfo volumeinfo` / `fsutil fsinfo refsinfo`, then
exercises independent copies, cloning, a hardlink, and compressible content.
NTFS does not support block cloning, so on NTFS the clone steps are expected
to report "not supported".

## What to send back

The whole script output (it is split into `== section ==` markers), plus:

- macOS: `diskutil info <volume>` and whether the volume is APFS;
- Windows: the `Get-Volume` / `fsutil` output the script already prints;
- whether you ran it on a plain volume, a btrfs subvolume, an APFS container,
  a ReFS volume, or a Dev Drive.

## What the numbers mean

- `extentdump` prints exactly what finddupe sees per file: `logical`,
  `physical`, `length`, and the `shared`/`encoded` flags, plus the shared-flag
  byte total.
- `compressdump` prints what `dedupe --cow --prefer-compressed` decides per
  file: `compressed=true/false`, using the same code path the tool uses
  (Linux: the FIEMAP `encoded` flag; macOS: `UF_COMPRESSED`; Windows:
  `FILE_ATTRIBUTE_COMPRESSED`). The probe's "transparent compression detection"
  section writes a compressible file, an incompressible one, and (where the
  platform allows) a deliberately uncompressed copy of the compressible one, so
  the three answers can be compared: the first should be `true`, the others
  `false`. On ReFS every answer is expected to be `false`, because ReFS does not
  implement NTFS per-file compression (`compact /c` reports "The request is not
  supported"), which also means `--prefer-compressed` never changes anything there.

### What a healthy macOS/APFS run looks like

Verified on Darwin 27 / macOS 27 ARM64:

- `extentdump` prints real extents (a 4 KiB run plus a 1044480-byte run for a
  1 MiB file); a `cp -c` clone shows the same device offsets, an independent copy
  different ones;
- `dedupe --cow` prints `CoW cloned: …`, `find --cow` then reports `100.0%`, and a
  second `dedupe --cow` prints nothing (`Dupes: 0 B in 0 files`);
- a clone whose first 256 KiB were rewritten reports a **partial** ratio (75.0%);
- compressed files (`afsctool -c`) fall back to the APFS clone ID; `extentdump`
  shows `opaque=true`, and a `cp -c` clone of one reports 100% shared;
- `cmp src.bin compClone.bin: identical` — a mismatch here means a clone no longer
  holds its source's bytes, and `1 files of zero length were skipped` on the next
  `find` is the same symptom (the compressed payload was lost while the file was
  marked uncompressed); report the log, it is a data-loss bug.

### What a healthy Windows/ReFS run looks like

Verified on Windows 11 with a ReFS 3.14 Dev Drive (4096-byte clusters):

- `extentdump` prints real extents (a small leading run plus one large run), and
  `total logical bytes` equals the file size;
- `dedupe --cow` prints `CoW cloned: '<victim>'` and `1 files replaced with CoW
  clones`;
- the `find --cow` that follows prints `shared: 100.0%` for every member;
- a second `dedupe --cow` prints nothing and reports `Dupes: 0 B in 0 files`,
  because the pair already shares its extents;
- the hardlink section leaves both file IDs unchanged.

If `extentdump` prints `extents=0` with no error, or `dedupe --cow` reports
`cluster size of …`, send the whole log: both point at the volume refusing to
describe itself, which is what the three-way cluster-size probe and the
"unavailable" reporting are there to make visible instead of silently reporting
"nothing shared".
- `find --cow` groups byte-identical files that are not hardlinks of each
  other and prints, per file, how many bytes are already shared with the rest
  of the group:
  - on Linux the `FIEMAP_EXTENT_SHARED` flag is used when the filesystem sets
    it, otherwise the physical start address is compared in-group;
  - on macOS/Windows there is no shared flag, so physical start identity is
    compared in-group.
- `dedupe --cow` skips a pair that is the same physical file, or whose extent
  layout is provably identical, and clones everything else. Content equality
  is established first, so an unnecessary clone is safe; the skip is only an
  optimization.

## Known caveats

- Compressed extents (`encoded=true`) are excluded from physical comparison:
  the reported logical length overstates the compressed physical allocation.
- macOS maps uncompressed files with `fcntl(F_LOG2PHYS_EXT)` through libSystem,
  giving real per-extent device offsets (partial ratios included). decmpfs
  compressed files answer `ENOTSUP`, so they fall back to the APFS clone ID from
  `getattrlist(ATTR_CMNEXT_CLONEID)`, which is family-level (100% or 0%).
- Windows `FSCTL_GET_RETRIEVAL_POINTERS` reports cluster runs; the probe checks
  whether clones actually share them and whether NTFS-vs-ReFS behaves as
  expected.
- A hardlink shares the whole inode, so `find --cow` collapses hardlinked
  aliases to one group member; use `find --listlink` to list hardlink groups.
- Block cloning on Windows works in whole clusters, so a file smaller than one
  cluster cannot be cloned; finddupe reports it as unsupported rather than
  silently writing a full copy.
