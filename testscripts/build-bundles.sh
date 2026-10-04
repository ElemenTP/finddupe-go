#!/usr/bin/env bash
#
# build-bundles.sh - cross-compile finddupe + extentdump and assemble
# per-platform CoW probe bundles under bin/cow-test/.
#
# Run from anywhere with a Go toolchain:
#
#   ./testscripts/build-bundles.sh
#
# Each bundle contains:
#   finddupe / finddupe.exe    the CLI under test
#   extentdump / extentdump.exe platform extent diagnostics
#   cow-probe.sh / cow-probe.ps1 the self-test to run on the target machine
#   README.md                  short instructions

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

OUT="bin/cow-test"
rm -rf "$OUT"
mkdir -p "$OUT"

build() {
	local goos="$1" goarch="$2" dir="$3"
	mkdir -p "$dir"
	GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$dir/$4" .
	GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$dir/$5" ./testtools/extentdump
}

# Linux
build linux amd64 "$OUT/linux-amd64" finddupe extentdump
install -m 0755 testscripts/cow-probe.sh "$OUT/linux-amd64/cow-probe.sh"

# macOS
for arch in amd64 arm64; do
	dir="$OUT/darwin-$arch"
	build darwin "$arch" "$dir" finddupe extentdump
	install -m 0755 testscripts/cow-probe.sh "$dir/cow-probe.sh"
done

# Windows
for arch in amd64 arm64; do
	dir="$OUT/windows-$arch"
	build windows "$arch" "$dir" finddupe.exe extentdump.exe
	install -m 0644 testscripts/cow-probe-windows.ps1 "$dir/cow-probe.ps1"
done

cp testscripts/README.md "$OUT/README.md"
for dir in "$OUT"/*/; do
	cp testscripts/README.md "$dir/README.md"
done

for dir in "$OUT"/*/; do
	name="$(basename "$dir")"
	if command -v zip >/dev/null 2>&1; then
		(cd "$OUT" && zip -qr "$name.zip" "$name")
	else
		tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
	fi
done

echo
echo "bundles in $OUT:"
ls -la "$OUT"
