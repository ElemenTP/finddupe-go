// Command compressdump prints, for every path argument, whether the transparent
// compression detection behind `dedupe --cow --prefer-compressed` reports the
// file as compressed. The CoW probe bundles run it on a real machine, so the
// answer comes from the same code the tool uses rather than from a guess about
// what the platform reports.
package main

import (
	"fmt"
	"os"

	"finddupe/internal/compress"
	"finddupe/internal/dupe"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <path> [path...]\n", os.Args[0])
		os.Exit(2)
	}

	status := 0
	for _, path := range os.Args[1:] {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Printf("%s: cannot stat: %v\n", path, err)
			status = 1
			continue
		}

		fi := dupe.FileInfo{Path: path, Size: info.Size()}
		fmt.Printf("%s: compressed=%v (size=%d bytes)\n", path, compress.IsCompressed(fi), info.Size())
	}
	os.Exit(status)
}
