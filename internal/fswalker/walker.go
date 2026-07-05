// Package fswalker walks the filesystem, matching glob patterns and producing file metadata.
package fswalker

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"finddupe/internal/dupe"
)

// Result holds either a successfully processed file or an error.
type Result struct {
	Info dupe.FileInfo
	Err  error
}

// ZeroLenCounter is the interface for tracking skipped zero-length files.
type ZeroLenCounter interface {
	AddZeroLen(delta int64)
}

// WalkOptions controls walker behavior.
type WalkOptions struct {
	// FollowSymlinks causes the walker to follow symbolic links and reparse points.
	FollowSymlinks bool

	// IncludeZeroLen includes zero-length files (skipped by default).
	IncludeZeroLen bool

	// ZeroLen is an optional counter for skipped zero-length files.
	ZeroLen ZeroLenCounter
}

// Walker walks the filesystem and produces FileInfo records.
type Walker struct{}

// New creates a new Walker.
func New() *Walker {
	return &Walker{}
}

// Walk walks the filesystem matching the given patterns and sends results on the returned channel.
// The channel is closed when the walk completes or ctx is cancelled.
func (w *Walker) Walk(ctx context.Context, patterns []string, opts WalkOptions) <-chan Result {
	ch := make(chan Result)
	go func() {
		defer close(ch)
		w.walkPatterns(ctx, patterns, opts, ch)
	}()
	return ch
}

// walkPatterns iterates over all patterns and walks each one.
func (w *Walker) walkPatterns(ctx context.Context, patterns []string, opts WalkOptions, ch chan<- Result) {
	seen := make(map[string]bool) // dedup by device+inode+path

	for _, pattern := range patterns {
		if ctx.Err() != nil {
			return
		}
		w.walkPattern(ctx, pattern, opts, ch, seen)
	}
}

// walkPattern walks a single pattern.
// If the pattern is a directory without wildcards, it appends ** to scan recursively.
func (w *Walker) walkPattern(
	ctx context.Context, pattern string, opts WalkOptions, ch chan<- Result, seen map[string]bool,
) {
	// Convert to absolute path for consistent dedup and output.
	absPattern, err := filepath.Abs(pattern)
	if err != nil {
		absPattern = pattern
	}

	// Determine base directory and pattern for walking.
	baseDir, matchPattern := splitPattern(absPattern)

	// Check if baseDir exists as a directory.
	info, err := os.Stat(baseDir)
	if err != nil {
		if !os.IsNotExist(err) {
			ch <- Result{Err: err}
		}
		return
	}

	if !info.IsDir() {
		// Single file pattern.
		w.processFile(ctx, baseDir, opts, ch, seen)
		return
	}

	// Walk the directory tree.
	walkFn := w.createWalkFn(ctx, baseDir, matchPattern, opts, ch, seen)

	if opts.FollowSymlinks {
		err = filepath.WalkDir(baseDir, walkFn)
	} else {
		err = w.walkWithoutSymlinks(baseDir, walkFn)
	}
	if err != nil {
		select {
		case <-ctx.Done():
		case ch <- Result{Err: err}:
		}
	}
}

// splitPattern splits a path+pattern into a base directory and a match component.
// For example, "/data/**/*.txt" → ("/data", "**/*.txt")
// For "/data" (no wildcard) → ("/data", "**").
func splitPattern(pattern string) (baseDir, match string) {
	// Find the first wildcard character.
	wildIdx := strings.IndexAny(pattern, "*?[")
	if wildIdx < 0 {
		// No wildcard — treat as directory, scan everything.
		return pattern, "**"
	}

	// Find the last separator before the wildcard.
	sepIdx := strings.LastIndexAny(pattern[:wildIdx], "/\\")
	if sepIdx < 0 {
		// Wildcard at the start (e.g., "*.txt" or "**/*.txt").
		return ".", pattern
	}

	return pattern[:sepIdx], pattern[sepIdx+1:]
}

// createWalkFn creates a [fs.WalkDirFunc] that filters by the match pattern.
func (w *Walker) createWalkFn(
	ctx context.Context, baseDir, matchPattern string, opts WalkOptions, ch chan<- Result, seen map[string]bool,
) fs.WalkDirFunc {
	// Determine if the pattern is recursive (contains **).
	recursive := strings.Contains(matchPattern, "**")

	return func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if !opts.FollowSymlinks && os.IsPermission(err) {
				return filepath.SkipDir
			}
			ch <- Result{Err: err}
			return nil
		}

		// Check context cancellation.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// For non-recursive patterns, skip subdirectories but not the root.
		if d.IsDir() {
			if !recursive && path != baseDir {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			ch <- Result{Err: err}
			return nil
		}

		// Match against the pattern.
		if !matchPath(matchPattern, path) {
			return nil
		}

		// Skip zero-length files unless IncludeZeroLen is set.
		if info.Size() == 0 && !opts.IncludeZeroLen {
			if opts.ZeroLen != nil {
				opts.ZeroLen.AddZeroLen(1)
			}
			return nil
		}

		w.processFileEntry(ctx, path, info, ch, seen)
		return nil
	}
}

// walkWithoutSymlinks walks a directory tree without following symlinks.
func (w *Walker) walkWithoutSymlinks(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return fn(path, d, err)
		}
		// If it's a symlink directory, don't descend into it.
		if d.IsDir() && d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return fn(path, d, err)
	})
}

// processFileEntry creates a FileInfo and sends it on the channel.
func (w *Walker) processFileEntry(ctx context.Context, path string, info os.FileInfo, ch chan<- Result, seen map[string]bool) {
	select {
	case <-ctx.Done():
		return
	default:
	}

	fi := dupe.FileInfo{
		Path: path,
		Size: info.Size(),
	}

	// Get inode and link count (platform-specific).
	fi.Inode, fi.NumLinks = getInode(path, info)

	// Send the result.
	select {
	case <-ctx.Done():
	case ch <- Result{Info: fi}:
	}
}

// processFile handles a single file (non-directory) pattern.
func (w *Walker) processFile(ctx context.Context, path string, opts WalkOptions, ch chan<- Result, seen map[string]bool) {
	info, err := os.Stat(path)
	if err != nil {
		ch <- Result{Err: err}
		return
	}

	if info.Size() == 0 && !opts.IncludeZeroLen {
		if opts.ZeroLen != nil {
			opts.ZeroLen.AddZeroLen(1)
		}
		return
	}

	w.processFileEntry(ctx, path, info, ch, seen)
}

// matchPath checks if a path matches a pattern with ** support.
// ** matches zero or more directory components.
// The path should be relative to the base directory, or absolute if the pattern is relative.
func matchPath(pattern, name string) bool {
	// Fast path: exact match.
	if pattern == name {
		return true
	}

	// Fast path: pattern is just "**" — matches everything.
	if pattern == "**" {
		return true
	}

	// Normalize to forward slashes for consistent matching.
	pattern = filepath.ToSlash(pattern)
	name = filepath.ToSlash(name)

	// For non-** patterns, use filepath.Match against the basename only
	// (flat match, not recursive).
	if !strings.Contains(pattern, "**") {
		matched, err := filepath.Match(pattern, filepath.Base(name))
		if err != nil {
			return false
		}
		return matched
	}

	// ** pattern: split into components and match recursively.
	return matchComponents(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// matchComponents recursively matches pattern components against path components.
// ** in the pattern matches zero or more path components.
func matchComponents(patParts, nameParts []string) bool {
	if len(patParts) == 0 {
		return len(nameParts) == 0
	}

	if patParts[0] == "**" {
		// ** matches zero or more path components.
		// Try matching ** against 0, 1, 2, ... name parts.
		for i := 0; i <= len(nameParts); i++ {
			if matchComponents(patParts[1:], nameParts[i:]) {
				return true
			}
		}
		return false
	}

	if len(nameParts) == 0 {
		return false
	}

	matched, err := filepath.Match(patParts[0], nameParts[0])
	if err != nil || !matched {
		return false
	}

	return matchComponents(patParts[1:], nameParts[1:])
}
