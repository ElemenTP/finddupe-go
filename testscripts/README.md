# CoW / extent platform probes

`find --cow` and `dedupe --cow` depend on platform extent APIs:

| Platform | Filesystem | Clone | Extent / sharing signal |
|---|---|---|---|
| Linux | btrfs, XFS | `FICLONE` ioctl | `FS_IOC_FIEMAP` + `FIEMAP_EXTENT_SHARED` |
| macOS | APFS | `clonefile(2)` | `getattrlist(ATTR_CMNEXT_CLONEID)` (APFS clone ID) |
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
- each contains `finddupe`, `extentdump`, the probe script, and this README

Copy the bundle matching your machine to that machine. The probe only creates
files inside the directory you give it.

## macOS (APFS)

```bash
cd darwin-arm64
chmod +x cow-probe.sh finddupe extentdump
./cow-probe.sh ~/finddupe-cow-probe
```

Use a directory on an APFS volume (`diskutil info / | grep "File System"`).
The script exercises:

1. independent copies, `dedupe --cow`, then `find --cow` before/after;
2. a pre-existing `cp -c` clone;
3. an existing hardlink (must never be replaced by a clone);
4. compressible files created with `ditto --hfsCompression` when available,
   including `ls -lO` to show the `compressed` flag.

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
- macOS uses the APFS clone ID from `getattrlist(ATTR_CMNEXT_CLONEID)`: all files
  of one clone family share it, but APFS gives no per-byte granularity, so
  `find --cow` reports 100% or 0% rather than a partial ratio.
- Windows `FSCTL_GET_RETRIEVAL_POINTERS` reports cluster runs; the probe checks
  whether clones actually share them and whether NTFS-vs-ReFS behaves as
  expected.
- A hardlink shares the whole inode, so `find --cow` collapses hardlinked
  aliases to one group member; use `find --listlink` to list hardlink groups.
- Block cloning on Windows works in whole clusters, so a file smaller than one
  cluster cannot be cloned; finddupe reports it as unsupported rather than
  silently writing a full copy.
