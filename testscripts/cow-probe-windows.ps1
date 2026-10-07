<#
.SYNOPSIS
  Exercise finddupe's CoW features on Windows (ReFS / Dev Drive) and dump
  platform extent data.

.DESCRIPTION
  Unpack a cow-test bundle (finddupe.exe, extentdump.exe, this script) on a
  machine with a ReFS volume or Dev Drive, then run:

    powershell -ExecutionPolicy Bypass -File .\cow-probe.ps1 -Dir R:\finddupe-probe

  -Dir must be on the ReFS volume you want to test. Everything is created
  inside it. Output is split into marked sections; please send back the whole
  log.

  NTFS does not support block cloning, so on NTFS the clone steps are expected
  to report "not supported" and the script says so.
#>
param(
    [string]$Dir = ""
)

$ErrorActionPreference = "Continue"

if (-not $Dir) {
    $Dir = Join-Path $env:TEMP ("finddupe-cow-probe-" + [guid]::NewGuid().ToString("N"))
}
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$Dir = (Resolve-Path $Dir).Path

# The probe deletes its own fixed-name subdirectories (independent, hardlink, ...)
# when a section re-runs, so it must never work directly in a directory the user
# pointed it at: those names could already hold real data. Everything goes into a
# fresh private directory instead.
$Dir = Join-Path $Dir ("finddupe-cow-probe-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$Dir = (Resolve-Path $Dir).Path

function Find-Bin([string]$name) {
    foreach ($d in @($PSScriptRoot, (Join-Path $PSScriptRoot "..\bin"))) {
        $p = Join-Path $d $name
        if (Test-Path $p) { return $p }
    }
    return $null
}

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$FD = Find-Bin "finddupe.exe"
if (-not $FD) { $FD = Find-Bin "finddupe-windows-$arch.exe" }
$ED = Find-Bin "extentdump.exe"
if (-not $ED) { $ED = Find-Bin "extentdump-windows-$arch.exe" }
$CD = Find-Bin "compressdump.exe"
if (-not $CD) { $CD = Find-Bin "compressdump-windows-$arch.exe" }

if (-not $FD) { Write-Error "finddupe.exe not found next to the script or in ..\bin"; exit 1 }
if (-not $ED) { Write-Error "extentdump.exe not found next to the script or in ..\bin"; exit 1 }

function Section([string]$title) {
    Write-Host ""
    Write-Host "================================================================"
    Write-Host "== $title"
    Write-Host "================================================================"
}

function Show([string]$exe, [string[]]$arguments) {
    Write-Host ("$ " + $exe + " " + ($arguments -join " "))
    & $exe @arguments 2>&1 | ForEach-Object { Write-Host $_ }
    Write-Host ("(exit=" + $LASTEXITCODE + ")")
}

function New-RandomFile([string]$path, [int]$size) {
    $buf = New-Object byte[] $size
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    $rng.GetBytes($buf)
    [System.IO.File]::WriteAllBytes($path, $buf)
}

function New-SameContentCopy([string]$source, [string]$target) {
    # Write the same bytes again so the two files are definitely independent
    # allocations (Copy-Item may block-clone on some Windows versions).
    [System.IO.File]::WriteAllBytes($target, [System.IO.File]::ReadAllBytes($source))
}

Section "environment"
Write-Host ("date:      " + (Get-Date))
Write-Host ("target:    " + $Dir)
Write-Host ("powershell " + $PSVersionTable.PSVersion)
# Ask about the volume the *path* is on. Split-Path -Qualifier would name the host
# drive for a volume mounted at a folder (C: for C:\mnt\refs), so Get-Volume and
# the filesystem check below would describe the wrong volume.
$volume = Get-Volume -FilePath $Dir -ErrorAction SilentlyContinue
$qualifier = if ($volume -and $volume.DriveLetter) { "$($volume.DriveLetter):" } else { Split-Path -Qualifier $Dir }
Write-Host "--- Get-Volume (for $Dir) ---"
if ($volume) { $volume | Format-List 2>&1 | Out-String | Write-Host } else { Write-Host "(no volume found for $Dir)" }
Write-Host "--- fsutil fsinfo volumeinfo ---"
& fsutil fsinfo volumeinfo "$qualifier" 2>&1 | Write-Host
Write-Host "--- fsutil fsinfo refsinfo (ReFS only) ---"
& fsutil fsinfo refsinfo "$qualifier" 2>&1 | Write-Host

$fsType = if ($volume) { $volume.FileSystem } else { "" }
if ($fsType -and $fsType -ne "ReFS") {
    Write-Warning "volume $qualifier is $fsType, not ReFS; block cloning is expected to be unsupported (NTFS cannot clone, and --prefer-compressed may choose an NTFS-compressed file whose extents cannot be cloned either)"
} else {
    Write-Host "volume is ReFS: block cloning should be available"
}

Section "binaries"
Write-Host "finddupe:   $FD"
& $FD version 2>&1 | Write-Host
Write-Host "extentdump: $ED"
Write-Host "compressdump: $(if ($CD) { $CD } else { 'not found' })"

# ---------------------------------------------------------------------------
# 0. Transparent compression detection
#
# The --prefer-compressed keeper preference reads FILE_ATTRIBUTE_COMPRESSED.
# ReFS (the only volume where block cloning works) does not support NTFS
# per-file compression, so on a ReFS volume every answer being false is the
# expected result; the section is here to confirm that, and to show what the
# probe reports on an NTFS volume.
# ---------------------------------------------------------------------------
$cmp = Join-Path $Dir "compress-detect"
Remove-Item -Recurse -Force $cmp -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $cmp | Out-Null

$compressible = Join-Path $cmp "compressible.bin"
$line = "finddupe compressible detection line`n"
$content = $line * 40000
[System.IO.File]::WriteAllText($compressible, $content)

$incompressible = Join-Path $cmp "incompressible.bin"
New-RandomFile $incompressible (1MB)

# Force NTFS compression on a second copy of the same content, when the volume
# supports it.
$forced = Join-Path $cmp "forced-compressed.bin"
Copy-Item $compressible $forced -Force
& compact.exe /c $forced 2>&1 | Write-Host

Section "transparent compression detection"
$fsType = if ($volume) { $volume.FileSystem } else { "" }
if ($fsType -eq "ReFS") {
    Write-Host "volume is ReFS: NTFS per-file compression is unavailable, all answers should be false"
}
foreach ($f in @($compressible, $incompressible, $forced)) {
    $item = Get-Item $f -ErrorAction SilentlyContinue
    $attrs = if ($item) { $item.Attributes.ToString() } else { "(missing)" }
    if ($CD) { & $CD $f 2>&1 | Write-Host } else { Write-Host "$f : compressdump not found" }
    Write-Host ("  attributes: " + $attrs)
}
Write-Host "--- fsutil file layout (compressible) ---"
& fsutil.exe file layout $compressible 2>&1 | Select-Object -First 8 | Write-Host
Write-Host "--- fsutil file layout (forced-compressed) ---"
& fsutil.exe file layout $forced 2>&1 | Select-Object -First 8 | Write-Host

# ---------------------------------------------------------------------------
# 1. Independent copies
# ---------------------------------------------------------------------------
$ind = Join-Path $Dir "independent"
Remove-Item -Recurse -Force $ind -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $ind | Out-Null
New-RandomFile (Join-Path $ind "a.bin") (1MB)
New-SameContentCopy (Join-Path $ind "a.bin") (Join-Path $ind "b.bin")

Section "extentdump: independent copies"
& $ED (Join-Path $ind "a.bin") (Join-Path $ind "b.bin") 2>&1 | Write-Host

Section "find --cow: independent copies (expect 0% shared)"
Show $FD @("find", "--cow", "--no-progress", $ind)

Section "dedupe --cow: independent copies (expect a clone)"
Show $FD @("dedupe", "--cow", "--no-progress", $ind)

Section "find --cow: after cloning (expect 100% shared)"
Show $FD @("find", "--cow", "--no-progress", $ind)

Section "dedupe --cow: second run (expect no clone, already shared)"
Show $FD @("dedupe", "--cow", "--no-progress", $ind)

Section "extentdump: after cloning"
& $ED (Join-Path $ind "a.bin") (Join-Path $ind "b.bin") 2>&1 | Write-Host

# ---------------------------------------------------------------------------
# 2. Existing hardlink must never be touched
# ---------------------------------------------------------------------------
$hl = Join-Path $Dir "hardlink"
Remove-Item -Recurse -Force $hl -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $hl | Out-Null
New-RandomFile (Join-Path $hl "a.bin") (256KB)
New-Item -ItemType HardLink -Path (Join-Path $hl "b.bin") -Target (Join-Path $hl "a.bin") -Force | Out-Null

Section "hardlink: file identity before"
& fsutil file queryfileid (Join-Path $hl "a.bin") 2>&1 | Write-Host
& fsutil file queryfileid (Join-Path $hl "b.bin") 2>&1 | Write-Host

Section "dedupe --cow: hardlink (expect no clone and no identity change)"
Show $FD @("dedupe", "--cow", "--no-progress", $hl)

Section "hardlink: file identity after (must be identical)"
& fsutil file queryfileid (Join-Path $hl "a.bin") 2>&1 | Write-Host
& fsutil file queryfileid (Join-Path $hl "b.bin") 2>&1 | Write-Host

Section "find --listlink: hardlink group"
Show $FD @("find", "--listlink", "--no-progress", $hl)

# ---------------------------------------------------------------------------
# 3. Compressible content (ReFS has no NTFS-style transparent compression, but
#    this checks that leading zeroes / repeated data do not confuse extents)
# ---------------------------------------------------------------------------
$cmp = Join-Path $Dir "compressible"
Remove-Item -Recurse -Force $cmp -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $cmp | Out-Null
$line = "finddupe compressible probe line`n"
$repeated = $line * 65536
[System.IO.File]::WriteAllText((Join-Path $cmp "src.txt"), $repeated)
New-SameContentCopy (Join-Path $cmp "src.txt") (Join-Path $cmp "copy.txt")

Section "extentdump: compressible content"
& $ED (Join-Path $cmp "src.txt") (Join-Path $cmp "copy.txt") 2>&1 | Write-Host

Section "find --cow: compressible independent copies (expect 0% shared)"
Show $FD @("find", "--cow", "--no-progress", $cmp)

Section "dedupe --cow: compressible independent copies"
Show $FD @("dedupe", "--cow", "--no-progress", $cmp)

Section "find --cow: compressible after cloning"
Show $FD @("find", "--cow", "--no-progress", $cmp)

# The other elimination actions, a read-only victim, and --prefer-compressed. --cow
# is what this probe was written for, but --hardlink and --delete share the same
# decision path (freshness re-check, write protection, metadata) and are what a real
# machine can still surprise us on.
$act = Join-Path $Dir "actions"
New-Item -ItemType Directory -Force -Path $act | Out-Null
New-RandomFile (Join-Path $act "hard-src.bin") 262144
New-SameContentCopy (Join-Path $act "hard-src.bin") (Join-Path $act "hard-dst.bin")
Section "dedupe --hardlink: replace the duplicate with a hardlink"
Show $FD @("dedupe", "--hardlink", "--no-progress", $act)
& fsutil hardlink list (Join-Path $act "hard-src.bin") 2>&1 | Write-Host

New-RandomFile (Join-Path $act "del-src.bin") 262144
New-SameContentCopy (Join-Path $act "del-src.bin") (Join-Path $act "del-dst.bin")
Section "dedupe --delete: remove the duplicate"
Show $FD @("dedupe", "--delete", "--no-progress", $act)
Get-ChildItem $act -Filter "del-*.bin" | Select-Object Name, Length | Format-Table | Out-String | Write-Host
Write-Host "expect: one del-*.bin left, 262144 bytes"

# The keeper policy keeps the smallest path, so the read-only file must sort *last*
# to be the victim this section is about; naming it ro-dst.bin made the read-only
# file the keeper and the section proved nothing.
$ro = Join-Path $Dir "readonly"
New-Item -ItemType Directory -Force -Path $ro | Out-Null
New-RandomFile (Join-Path $ro "ro-keep.bin") 131072
New-SameContentCopy (Join-Path $ro "ro-keep.bin") (Join-Path $ro "ro-victim.bin")
Set-ItemProperty -Path (Join-Path $ro "ro-victim.bin") -Name IsReadOnly -Value $true
Section "dedupe --cow without -r: the read-only victim is skipped"
Show $FD @("dedupe", "--cow", "--no-progress", $ro)
Write-Host "expect: no clone, ro-victim.bin still there and still read-only"
Section "dedupe --cow -r: the read-only victim is replaced"
Show $FD @("dedupe", "--cow", "-r", "--no-progress", $ro)
Get-ChildItem $ro | Select-Object Name, Length, Attributes | Format-Table | Out-String | Write-Host
Write-Host "expect: the read-only attribute is kept on the survivor"

# --prefer-compressed needs identical content and genuinely different compression
# state. compact /c is NTFS-only (ReFS refuses it), so on ReFS both members stay
# plain and the section shows that the flag has nothing to prefer there; a ReFS copy
# may also become a block clone, which makes the pair already shared and the run a
# no-op. Both are expected on that volume.
function New-CompressionPair([string]$dir) {
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $text = ("finddupe prefer-compressed probe line`n" * 32768)
    [System.IO.File]::WriteAllText((Join-Path $dir "zz-compressed.bin"), $text)
    & compact /c (Join-Path $dir "zz-compressed.bin") 2>&1 | Out-Null
    Copy-Item (Join-Path $dir "zz-compressed.bin") (Join-Path $dir "aa-plain.bin") -Force
    # Copying can carry the compressed attribute along, so the plain member is told
    # explicitly not to be compressed (also NTFS-only, and a no-op where the volume
    # cannot compress at all).
    & compact /u (Join-Path $dir "aa-plain.bin") 2>&1 | Out-Null
}

$pc = Join-Path $Dir "prefer-compressed"
New-CompressionPair (Join-Path $pc "with")
New-CompressionPair (Join-Path $pc "without")

Section "compression state before --prefer-compressed (expect aa-plain=false, zz-compressed=true on NTFS)"
if ($CD) { & $CD (Join-Path $pc "with\aa-plain.bin") (Join-Path $pc "with\zz-compressed.bin") 2>&1 | Write-Host } else { Write-Host "compressdump not found" }

Section "dedupe --cow without -C: the plain member is the keeper"
Show $FD @("dedupe", "--cow", "--no-progress", (Join-Path $pc "without"))
if ($CD) { & $CD (Join-Path $pc "without\aa-plain.bin") (Join-Path $pc "without\zz-compressed.bin") 2>&1 | Write-Host } else { Write-Host "compressdump not found" }

Section "dedupe --cow -C: the compressed member is the keeper"
Show $FD @("dedupe", "--cow", "--prefer-compressed", "--no-progress", (Join-Path $pc "with"))
if ($CD) { & $CD (Join-Path $pc "with\aa-plain.bin") (Join-Path $pc "with\zz-compressed.bin") 2>&1 | Write-Host } else { Write-Host "compressdump not found" }
Write-Host "expect: on a volume that can compress, -C keeps the group compressed and"
Write-Host "        without it the plain member wins; where cloning works at all, the keeper"
Write-Host "        named in a failure is still the compressed member with -C. On ReFS the"
Write-Host "        flag changes nothing (both members stay plain), and a ReFS Copy-Item may"
Write-Host "        block-clone the pair, in which case the run is a no-op ('Dupes: 0')."

# A fully sparse file has a size but no allocated clusters. Linux answers "no
# extents" (covered by a unit test); Windows is the open question, because ReFS has
# been seen answering a query with no data at all, which the query reports as
# unavailable rather than as "nothing is shared".
$sparse = Join-Path $Dir "sparse"
New-Item -ItemType Directory -Force -Path $sparse | Out-Null
$holed = Join-Path $sparse "holed.bin"
& fsutil file createnew $holed 1048576 | Out-Null
& fsutil sparse setflag $holed 2>&1 | Out-Null
& fsutil sparse setrange $holed 0 1048576 2>&1 | Out-Null
Section "extentdump: a fully sparse file (no allocated clusters)"
& $ED $holed 2>&1 | Write-Host
Write-Host "expect: no extents and no error; 'unavailable' would also be an honest answer"
Write-Host "        on a volume that describes nothing, and either is worth reporting"

$res = Join-Path $Dir "resident"
New-Item -ItemType Directory -Force -Path $res | Out-Null
[System.IO.File]::WriteAllText((Join-Path $res "small.txt"), "tiny")
Section "extentdump: a sub-cluster file (resident data on NTFS, no extents)"
& $ED (Join-Path $res "small.txt") 2>&1 | Write-Host
Write-Host "expect: no error and extents=0; a resident file has no mapping, which must not"
Write-Host "        be reported as 'this filesystem does not support extents'"

Write-Host ""
Write-Host "================================================================"
Write-Host "== probe finished; target directory kept at: $Dir"
Write-Host "================================================================"
