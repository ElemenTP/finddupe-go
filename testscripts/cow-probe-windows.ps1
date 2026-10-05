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
$qualifier = Split-Path -Qualifier $Dir
$letter = $qualifier.TrimEnd(':')
Write-Host "--- Get-Volume ---"
# A volume mounted at a folder has no drive letter: the probe is diagnostic, so
# report what is available instead of failing.
$volume = $null
if ($letter) { $volume = Get-Volume -DriveLetter $letter -ErrorAction SilentlyContinue }
if ($volume) { $volume | Format-List 2>&1 | Out-String | Write-Host } else { Write-Host "(no drive letter for $qualifier)" }
Write-Host "--- fsutil fsinfo volumeinfo ---"
& fsutil fsinfo volumeinfo "$qualifier" 2>&1 | Write-Host
Write-Host "--- fsutil fsinfo refsinfo (ReFS only) ---"
& fsutil fsinfo refsinfo "$qualifier" 2>&1 | Write-Host

$fsType = if ($volume) { $volume.FileSystem } else { "" }
if ($fsType -and $fsType -ne "ReFS") {
    Write-Warning "volume $qualifier is $fsType, not ReFS; block cloning is expected to be unsupported"
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

Write-Host ""
Write-Host "================================================================"
Write-Host "== probe finished; target directory kept at: $Dir"
Write-Host "================================================================"
