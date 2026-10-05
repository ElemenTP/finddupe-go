#!/usr/bin/env bash
#
# cow-probe.sh - exercise finddupe's CoW features and dump platform extent data.
#
# Run this on macOS (APFS) or Linux (btrfs/XFS) from a directory that contains
# the matching finddupe and extentdump binaries (e.g. an unpacked cow-test
# bundle), or with them available in ../bin.
#
#   ./cow-probe.sh [target-directory]
#
# The target directory must be on the filesystem you want to test (APFS volume
# / btrfs subvolume). Everything it creates stays inside that directory.
#
# Output is split into clearly marked sections; please send back the whole log.

set -u

DIR="${1:-}"
if [ -z "$DIR" ]; then
	DIR="$(mktemp -d "${TMPDIR:-/tmp}/finddupe-cow-probe.XXXXXX")"
else
	mkdir -p "$DIR" || exit 1
	DIR="$(cd "$DIR" && pwd)"

	# The probe deletes its own fixed-name subdirectories (independent/, hardlink/,
	# ...) when a section re-runs, so it must never work directly in a directory
	# the user pointed it at: those names could already hold real data. Everything
	# goes into a fresh private directory instead.
	WORK="$(mktemp -d "$DIR/finddupe-cow-probe.XXXXXX")" || {
		echo "cannot create a work directory in $DIR" >&2
		exit 1
	}
	DIR="$WORK"
	echo "Probe work directory: $DIR"
fi

HERE="$(cd "$(dirname "$0")" && pwd)"
OS="$(uname -s)"
ARCH="$(uname -m)"

find_bin() {
	for candidate in "$HERE/$1" "$HERE/$2" "$HERE/../bin/$2"; do
		if [ -x "$candidate" ]; then
			echo "$candidate"
			return 0
		fi
	done
	return 1
}

FIEMAP=""
case "$OS-$ARCH" in
Darwin-arm64) FD="$(find_bin finddupe finddupe-darwin-arm64)"; ED="$(find_bin extentdump extentdump-darwin-arm64)"; CD="$(find_bin compressdump compressdump-darwin-arm64 || true)"; FIEMAP="$(find_bin darwinfiemap darwinfiemap-darwin-arm64 || true)" ;;
Darwin-x86_64) FD="$(find_bin finddupe finddupe-darwin-amd64)"; ED="$(find_bin extentdump extentdump-darwin-amd64)"; CD="$(find_bin compressdump compressdump-darwin-amd64 || true)"; FIEMAP="$(find_bin darwinfiemap darwinfiemap-darwin-amd64 || true)" ;;
Linux-x86_64) FD="$(find_bin finddupe finddupe-linux-amd64)"; ED="$(find_bin extentdump extentdump-linux-amd64)"; CD="$(find_bin compressdump compressdump-linux-amd64 || true)" ;;
*)
	echo "unsupported platform $OS-$ARCH" >&2
	exit 1
	;;
esac

if [ -z "${FD:-}" ] || [ ! -x "$FD" ]; then
	echo "finddupe binary not found next to the script or in ../bin" >&2
	exit 1
fi
if [ -z "${ED:-}" ] || [ ! -x "$ED" ]; then
	echo "extentdump binary not found next to the script or in ../bin" >&2
	exit 1
fi

section() {
	echo
	echo "================================================================"
	echo "== $*"
	echo "================================================================"
}

show() {
	echo "\$ $*"
	"$@" 2>&1
	echo "(exit=$?)"
}

# clone_one SRC DST - make a CoW clone using the platform's own tooling.
clone_one() {
	if [ "$OS" = "Darwin" ]; then
		cp -c "$1" "$2"
	else
		cp --reflink=always "$1" "$2"
	fi
}

# random_file PATH SIZE - incompressible content.
random_file() {
	dd if=/dev/urandom of="$1" bs=1024 count=$(( $2 / 1024 )) 2>/dev/null
}

# independent_copy SRC DST - make a real (non-shared) copy. Linux cp defaults
# to reflink when the filesystem supports it, so it must be disabled.
independent_copy() {
	if [ "$OS" = "Darwin" ]; then
		cp "$1" "$2"
	else
		cp --reflink=never "$1" "$2"
	fi
}

section "environment"
echo "date: $(date)"
echo "uname: $(uname -a)"
echo "target dir: $DIR"
if [ "$OS" = "Darwin" ]; then
	mount | grep -E ' on /' | head -5
	df -h "$DIR" 2>&1 | head -3
else
	findmnt -no SOURCE,FSTYPE,OPTIONS --target "$DIR" 2>&1 || df -T "$DIR"
fi

section "binaries"
echo "finddupe:   $FD"
"$FD" version 2>&1
echo "extentdump: $ED"

# ---------------------------------------------------------------------------
# 1. Independent copies (same content, separate storage)
# ---------------------------------------------------------------------------
IND="$DIR/independent"
rm -rf "$IND"
mkdir -p "$IND"
random_file "$IND/a.bin" 1048576
independent_copy "$IND/a.bin" "$IND/b.bin"

section "extentdump: independent copies"
"$ED" "$IND/a.bin" "$IND/b.bin"

section "find --cow: independent copies (expect 0% shared)"
show "$FD" find --cow --no-progress "$IND"

section "dedupe --cow: independent copies (expect a clone)"
show "$FD" dedupe --cow --no-progress "$IND"

section "find --cow: after cloning (expect 100% shared)"
show "$FD" find --cow --no-progress "$IND"

section "dedupe --cow: second run (expect no clone, already shared)"
show "$FD" dedupe --cow --no-progress "$IND"

section "extentdump: after cloning"
"$ED" "$IND/a.bin" "$IND/b.bin"

# ---------------------------------------------------------------------------
# 2. Pre-existing clone
# ---------------------------------------------------------------------------
CLO="$DIR/preclone"
rm -rf "$CLO"
mkdir -p "$CLO"
random_file "$CLO/orig.bin" 1048576
if clone_one "$CLO/orig.bin" "$CLO/clone.bin"; then
	section "extentdump: pre-existing clone"
	"$ED" "$CLO/orig.bin" "$CLO/clone.bin"

	section "find --cow: pre-existing clone (expect 100% shared)"
	show "$FD" find --cow --no-progress "$CLO"

	section "dedupe --cow: pre-existing clone (expect no clone)"
	show "$FD" dedupe --cow --no-progress "$CLO"
else
	echo "could not create a clone with the platform tooling (skipping section 2)"
fi

# ---------------------------------------------------------------------------
# 3. Existing hardlink must never be touched
# ---------------------------------------------------------------------------
HL="$DIR/hardlink"
rm -rf "$HL"
mkdir -p "$HL"
random_file "$HL/a.bin" 262144
ln "$HL/a.bin" "$HL/b.bin"

section "hardlink: inodes before"
ls -li "$HL" 2>&1

section "dedupe --cow: hardlink (expect no clone and no inode change)"
show "$FD" dedupe --cow --no-progress "$HL"

section "hardlink: inodes after (must be identical)"
ls -li "$HL" 2>&1

section "find --listlink: hardlink group"
show "$FD" find --listlink --no-progress "$HL"

# ---------------------------------------------------------------------------
# 4. Compressed content (APFS decmpfs, via afsctool when available)
# ---------------------------------------------------------------------------
CMP="$DIR/compressible"
rm -rf "$CMP"
mkdir -p "$CMP"
yes "finddupe compressible probe line" 2>/dev/null | head -c 2097152 >"$CMP/src.bin" || true

# compress_file PATH - apply filesystem compression; echoes how it was done.
compress_file() {
	if [ "$OS" != "Darwin" ]; then
		return 1
	fi
	if command -v afsctool >/dev/null 2>&1; then
		if afsctool -c "$1" >/dev/null 2>&1; then
			echo "afsctool -c"
			return 0
		fi
		if afsctool -c -T zlib "$1" >/dev/null 2>&1; then
			echo "afsctool -c -T zlib"
			return 0
		fi
	fi
	if command -v ditto >/dev/null 2>&1; then
		if ditto --hfsCompression "$1" "$1.compressed" >/dev/null 2>&1; then
			mv "$1.compressed" "$1"
			echo "ditto --hfsCompression"
			return 0
		fi
	fi
	return 1
}

# is_compressed PATH - true when the filesystem reports the file as compressed.
# APFS native compression is exposed through the SF_COMPRESSED flag (`ls -lO`
# prints "compressed"), not as a readable com.apple.decmpfs xattr.
is_compressed() {
	# Only the flags column is inspected: a file whose *name* contains
	# "compressed" must not be mistaken for a compressed file.
	ls -lO "$1" 2>/dev/null | awk 'NR == 1 { next } { for (i = 5; i <= NF; i++) if ($i == "compressed") exit 0 } exit 1'
}

independent_copy "$CMP/src.bin" "$CMP/compA.bin"
independent_copy "$CMP/src.bin" "$CMP/compB.bin"

if [ "$OS" = "Darwin" ]; then
	HOW_A="$(compress_file "$CMP/compA.bin" || true)"
	HOW_B="$(compress_file "$CMP/compB.bin" || true)"
	echo "--- compression: compA=[$HOW_A] compB=[$HOW_B] ---"
	if is_compressed "$CMP/compA.bin"; then
		echo "compA.bin is compressed: yes"
	else
		echo "compA.bin is compressed: no"
	fi
	if command -v afsctool >/dev/null 2>&1; then
		echo "--- afsctool -v ---"
		afsctool -v "$CMP" 2>&1 | head -12
	fi
	echo "--- ls -lO (look for 'compressed') ---"
	ls -lO "$CMP" 2>&1
else
	# Linux: the mount option decides compression per write, and btrfs exposes
	# it as the FIEMAP ENCODED flag. Build one compressible file (compressed by
	# the mount option) and one incompressible file (stored plain), so the two
	# answers can be compared.
	mkdir -p "$CMP/detect"
	yes "finddupe compressible detection line" 2>/dev/null |
		head -c 1048576 >"$CMP/detect/compressible.bin" || true
	dd if=/dev/urandom of="$CMP/detect/incompressible.bin" bs=1048576 count=1 2>/dev/null

	# btrfs can be told to store a file plain: create it, drop compression, then
	# write the same compressible content through the notrunc path.
	cp "$CMP/detect/compressible.bin" "$CMP/detect/forced-plain.bin"
	if command -v btrfs >/dev/null 2>&1 && btrfs property set "$CMP/detect/forced-plain.bin" compression no 2>/dev/null; then
		dd if="$CMP/detect/compressible.bin" of="$CMP/detect/forced-plain.bin" bs=1M conv=notrunc 2>/dev/null || true
	fi

	echo "--- transparent compression detection ---"
	if [ -n "$CD" ]; then
		for f in compressible incompressible forced-plain; do
			"$CD" "$CMP/detect/$f.bin" 2>&1 || true
		done
	else
		echo "compressdump not found; skipping the library answer"
	fi
	echo "--- raw FIEMAP flags (encoded=true means the extent is stored encoded) ---"
	for f in compressible incompressible forced-plain; do
		echo "[$f.bin]"
		"$ED" "$CMP/detect/$f.bin" 2>&1 | grep -E '^  \[0\]|^  \[1\]|extents=' || true
	done
	echo "--- expected: compressible.bin compressed=true, incompressible.bin false ---"
fi

clone_one "$CMP/compA.bin" "$CMP/compClone.bin" 2>/dev/null || independent_copy "$CMP/compA.bin" "$CMP/compClone.bin"

section "extentdump: compressed files (independent + clone)"
"$ED" "$CMP/compA.bin" "$CMP/compB.bin" "$CMP/compClone.bin" 2>&1

section "find --cow: compressed files (only the cp -c clone should be shared)"
show "$FD" find --cow --no-progress "$CMP"

section "dedupe --cow: compressible files (clones the independent copies)"
show "$FD" dedupe --cow --no-progress "$CMP"

section "find --cow: compressible after dedupe (expect all in the clone family)"
show "$FD" find --cow --no-progress "$CMP"

section "dedupe --cow: compressible second run (expect no clone)"
show "$FD" dedupe --cow --no-progress "$CMP"

# ---------------------------------------------------------------------------
# 4b. Partially shared clone (identical content, only part of the extents shared)
# ---------------------------------------------------------------------------
PART="$DIR/partial"
rm -rf "$PART"
mkdir -p "$PART"
random_file "$PART/a.bin" 1048576
clone_one "$PART/a.bin" "$PART/b.bin" 2>/dev/null || independent_copy "$PART/a.bin" "$PART/b.bin"

section "extentdump: partial - clone before the rewrite (expect identical extents)"
"$ED" "$PART/a.bin" "$PART/b.bin"

section "find --cow: partial - clone before the rewrite (expect 100%)"
show "$FD" find --cow --no-progress "$PART"

# Rewrite the first 256 KiB of the clone with the very same bytes: the content
# stays identical, but the filesystem has to stop sharing that range.
dd if="$PART/a.bin" of="$PART/b.bin" bs=4096 count=64 conv=notrunc 2>/dev/null

section "extentdump: partial - after rewriting 256 KiB of the clone with identical bytes"
"$ED" "$PART/a.bin" "$PART/b.bin"

section "find --cow: partially shared clone (a partial ratio if the rest stayed shared)"
echo "(the per-file FIEMAP Shared flag may still report the untouched file as 100%)"
show "$FD" find --cow --no-progress "$PART"

section "dedupe --cow: partially shared clone (expect the extents to be unified)"
show "$FD" dedupe --cow --no-progress "$PART"

section "find --cow: partial after dedupe (expect 100% again)"
show "$FD" find --cow --no-progress "$PART"

section "compressed content check (cloned file must equal the source)"
if command -v cmp >/dev/null 2>&1; then
	if cmp -s "$CMP/src.bin" "$CMP/compClone.bin"; then
		echo "cmp src.bin compClone.bin: identical"
	else
		echo "cmp src.bin compClone.bin: DIFFERENT"
	fi
fi
if [ "$OS" = "Darwin" ] && command -v afsctool >/dev/null 2>&1; then
	echo "--- afsctool -v after cloning ---"
	afsctool -v "$CMP" 2>&1 | head -12
fi

# ---------------------------------------------------------------------------
# 5. FIEMAP-equivalent investigation (macOS only)
# ---------------------------------------------------------------------------
if [ "$OS" = "Darwin" ] && [ -n "${FIEMAP:-}" ] && [ -x "${FIEMAP:-}" ]; then
	FI="$DIR/fiemap"
	rm -rf "$FI"
	mkdir -p "$FI"
	random_file "$FI/plain.bin" 1048576
	independent_copy "$FI/plain.bin" "$FI/copy.bin"
	clone_one "$FI/plain.bin" "$FI/clone.bin" 2>/dev/null || true
	yes "finddupe fiemap probe line" 2>/dev/null | head -c 2097152 >"$FI/text.bin" || true
	compress_file "$FI/text.bin" >/dev/null 2>&1 || true
	clone_one "$FI/text.bin" "$FI/text-clone.bin" 2>/dev/null || true

	section "darwinfiemap: plain vs independent copy vs clone"
	show "$FIEMAP" "$FI/plain.bin" "$FI/copy.bin" "$FI/clone.bin"

	section "darwinfiemap: compressed file vs its clone"
	show "$FIEMAP" "$FI/text.bin" "$FI/text-clone.bin"
fi

echo
echo "================================================================"
echo "== probe finished; target directory kept at: $DIR"
echo "================================================================"
