// Command extentdump prints the physical extent information finddupe uses, as
// the current platform reports it. It exists to validate the platform extent
// APIs on real machines (macOS getattrlist clone IDs, Windows
// FSCTL_GET_RETRIEVAL_POINTERS, Linux FIEMAP) and to diagnose why `find --cow`
// does or does not report sharing.
//
// Usage: extentdump <file> [file...]
package main

import (
	"fmt"
	"os"

	"finddupe/internal/checksum"
	"finddupe/internal/extent"
)

const (
	// minArgs is "extentdump" plus at least one path.
	minArgs = 2

	// exitUsage is the exit code for a usage error.
	exitUsage = 2
)

func main() {
	if len(os.Args) < minArgs {
		fmt.Fprintln(os.Stderr, "usage: extentdump <file> [file...]")
		os.Exit(exitUsage)
	}

	fmt.Printf("extentdump: supported=%v identity=%s\n", extent.Supported(), extent.Identity())

	for _, path := range os.Args[1:] {
		dump(path)
	}
}

// dump prints identity and extents for one file.
func dump(path string) {
	//nolint:gosec // diagnostic CLI: paths are supplied by the user on purpose
	stat, err := os.Stat(path)
	if err != nil {
		fmt.Printf("%s\n  stat error: %v\n", path, err)
		return
	}

	info, idErr := checksum.ComputeFileInfo(path, stat.Size())
	if idErr == nil {
		fmt.Printf("%s (size=%d)\n  identity: dev=%d inode=%d numLinks=%d\n",
			path, stat.Size(), info.Dev, info.Inode, info.NumLinks)
	} else {
		fmt.Printf("%s (size=%d)\n  identity error: %v\n", path, stat.Size(), idErr)
	}

	extents, err := extent.Query(path, stat.Size())
	if err != nil {
		fmt.Printf("  extent query error: %v\n", err)
		return
	}

	fmt.Printf("  extents=%d sharedFlagBytes=%d\n", len(extents), extent.SharedFlagBytes(extents))

	var logicalBytes int64
	for i, e := range extents {
		logicalBytes += int64(e.Length) //nolint:gosec // diagnostic output
		fmt.Printf("  [%d] logical=%d physical=%d length=%d shared=%v encoded=%v opaque=%v\n",
			i, e.Logical, e.Physical, e.Length, e.Shared, e.Encoded, e.Opaque)
	}
	fmt.Printf("  total logical bytes=%d\n", logicalBytes)
}
