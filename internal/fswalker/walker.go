// Package fswalker walks the filesystem, matching glob patterns and producing file metadata.
package fswalker

import (
	"context"
	"fmt"
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

// NoMatchError reports a pattern that matched no files at all, so the caller can
// fail the run instead of reporting success for a typo, an empty directory or a
// glob that no longer hits. It travels the per-file error channel because the
// walker is asynchronous, and is distinguished from a read error by type.
type NoMatchError struct {
	// Pattern is the pattern as the user wrote it.
	Pattern string
}

// Error implements error.
func (e *NoMatchError) Error() string {
	return fmt.Sprintf("no files matched %q", e.Pattern)
}

// sendResult delivers a result unless the walk has been cancelled, and reports
// whether it was delivered. A walker goroutine must never block on a send after
// its consumer has gone away: it would stay parked forever, because nothing will
// ever read from the channel again.
func sendResult(ctx context.Context, ch chan<- Result, result Result) bool {
	select {
	case ch <- result:
		return true
	case <-ctx.Done():
		return false
	}
}

// walkState is the state shared by the callbacks of one pattern: the set of
// already-visited directories (symlink loop prevention, shared across the
// patterns of one walk) and whether this pattern matched anything.
type walkState struct {
	seen    map[string]bool
	matched int
	failed  bool
}

// ZeroLenCounter is the interface for tracking skipped zero-length files.
type ZeroLenCounter interface {
	AddZeroLen(delta int64)
}

// WalkOptions controls walker behavior.
type WalkOptions struct {
	// FollowSymlinks causes the walker to follow symbolic links and reparse
	// points found while walking. Without it they are skipped. When set, a link
	// is resolved and classified by its target: links to directories are walked
	// (with loop prevention) and links to regular files are reported with the
	// target's size and identity under the link's path. A pattern that names a
	// link directly always resolves it, with or without this option.
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
	// seen tracks resolved symlink targets to prevent infinite loops
	// when FollowSymlinks is enabled. Key is the canonical path.
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		if ctx.Err() != nil {
			return
		}
		w.walkPattern(ctx, pattern, opts, ch, seen)
	}
}

// walkPattern walks a single pattern. A pattern that names an existing path is
// scanned as written (recursively when it is a directory); otherwise it is
// treated as a glob. A pattern that matches no usable file produces a
// [NoMatchError] so a typo cannot pass for a successful run.
func (w *Walker) walkPattern(
	ctx context.Context, pattern string, opts WalkOptions, ch chan<- Result, seen map[string]bool,
) {
	// Convert to absolute path for consistent dedup and output.
	absPattern, err := filepath.Abs(pattern)
	if err != nil {
		absPattern = pattern
	}

	st := &walkState{seen: seen}

	switch _, statErr := os.Stat(absPattern); {
	case statErr == nil:
		// The pattern names an existing path, so it is taken literally: a path
		// may contain glob characters ("/data/[2020] photos", "report[1].txt")
		// and splitting it on them would scan the wrong tree or nothing at all.
		w.walkLiteral(ctx, absPattern, opts, ch, st)
	case !os.IsNotExist(statErr):
		st.failed = true
		sendResult(ctx, ch, Result{Err: statErr})
	default:
		w.walkGlob(ctx, absPattern, opts, ch, st)
	}

	if st.matched == 0 && !st.failed {
		w.reportNoMatch(ctx, pattern, ch)
	}
}

// walkLiteral walks a path that exists: a file is processed directly, a
// directory is scanned recursively.
func (w *Walker) walkLiteral(ctx context.Context, path string, opts WalkOptions, ch chan<- Result, st *walkState) {
	info, err := os.Stat(path)
	if err != nil {
		st.failed = true
		sendResult(ctx, ch, Result{Err: err})
		return
	}

	if !info.IsDir() {
		w.processFile(ctx, path, opts, ch, st)
		return
	}

	// filepath.WalkDir does NOT follow symlinks by default — it uses os.Lstat
	// internally. Symlink following is handled inside createWalkFn.
	if walkErr := filepath.WalkDir(path, w.createWalkFn(ctx, path, "**", opts, ch, st)); walkErr != nil {
		st.failed = true
		sendResult(ctx, ch, Result{Err: walkErr})
	}
}

// walkGlob walks a pattern that does not name an existing path: the pattern is
// split into the longest existing base directory and the match component.
func (w *Walker) walkGlob(ctx context.Context, pattern string, opts WalkOptions, ch chan<- Result, st *walkState) {
	baseDir, matchPattern := splitPattern(pattern)

	info, err := os.Stat(baseDir)
	if err != nil {
		if !os.IsNotExist(err) {
			st.failed = true
			sendResult(ctx, ch, Result{Err: err})
		}
		return // nothing matched; the caller reports it
	}

	if !info.IsDir() {
		w.processFile(ctx, baseDir, opts, ch, st)
		return
	}

	if walkErr := filepath.WalkDir(baseDir, w.createWalkFn(ctx, baseDir, matchPattern, opts, ch, st)); walkErr != nil {
		st.failed = true
		sendResult(ctx, ch, Result{Err: walkErr})
	}
}

// reportNoMatch reports that pattern matched nothing, unless the walk was
// cancelled.
func (w *Walker) reportNoMatch(ctx context.Context, pattern string, ch chan<- Result) {
	sendResult(ctx, ch, Result{Err: &NoMatchError{Pattern: pattern}})
}

// splitPattern splits a path+pattern into a base directory and a match component.
// For example, "/data/**/*.txt" → ("/data", "**/*.txt")
// For "/data" (no wildcard) → ("/data", "**").
func splitPattern(pattern string) (string, string) {
	if baseDir, match := splitAtExistingDir(pattern); baseDir != "" {
		return baseDir, match
	}

	// No part of the pattern exists: fall back to the first metacharacter so the
	// walker can still report "no files matched" for the intended tree.
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

// splitAtExistingDir returns the last separator whose following component
// contains a glob metacharacter and whose prefix exists as a directory, together
// with the match component after it. That keeps a metacharacter inside a
// directory name ("/data/[2020] photos/*.txt") from being mistaken for the start
// of the pattern, while still splitting "/data/**/*.txt" at "/data" (the
// "/data/**" prefix does not exist). It returns empty strings when no separator
// qualifies.
func splitAtExistingDir(pattern string) (string, string) {
	var baseDir, match string

	for i := range len(pattern) {
		if pattern[i] != '/' && pattern[i] != '\\' {
			continue
		}

		rest := pattern[i+1:]
		if rest == "" {
			continue
		}
		component := rest
		if end := strings.IndexAny(rest, "/\\"); end >= 0 {
			component = rest[:end]
		}
		if !strings.ContainsAny(component, "*?[") {
			continue
		}

		if info, err := os.Stat(pattern[:i]); err == nil && info.IsDir() {
			baseDir, match = pattern[:i], rest
		}
	}

	return baseDir, match
}

// createWalkFn creates a [fs.WalkDirFunc] that filters by the match pattern.
//
// Only regular files are reported: devices, sockets and FIFOs have no content
// to compare (and opening a FIFO blocks until a writer appears). Symbolic links
// and Windows reparse points are skipped unless opts.FollowSymlinks is set, in
// which case the entry is resolved and classified by what it points at — a
// directory is walked through the loop-preventing seen set, a regular file is
// reported with the target's metadata.
//
//nolint:gocognit // the walk callback handles many filesystem edge cases in one place
func (w *Walker) createWalkFn(
	ctx context.Context, baseDir, matchPattern string, opts WalkOptions, ch chan<- Result, st *walkState,
) fs.WalkDirFunc {
	// Determine if the pattern is recursive (contains **).
	recursive := strings.Contains(matchPattern, "**")

	return func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// On permission errors, skip the directory to avoid a flood of
			// "permission denied" errors for every file inside it.
			if os.IsPermission(err) {
				return filepath.SkipDir
			}
			st.failed = true
			sendResult(ctx, ch, Result{Err: err})
			return nil
		}

		// Check context cancellation.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// A directory symlink (a Windows junction reports ModeDir|ModeSymlink)
		// must never be descended into by WalkDir: its target is walked
		// explicitly below, so skipDir keeps the two from overlapping.
		isSymlink := d.Type()&os.ModeSymlink != 0
		skipDir := func() error {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Plain directories are walked by WalkDir itself. Recording them in
		// seen keeps a symlink pointing at a directory that is already (or
		// later) part of the tree from being walked twice. The root of the
		// current walk is exempt: it is the entry the walk starts from and may
		// have been registered by the symlink that led here.
		if d.IsDir() && !isSymlink {
			if path != baseDir && st.seen[path] {
				return filepath.SkipDir
			}
			st.seen[path] = true

			// For non-recursive patterns, skip subdirectories but not the root.
			if !recursive && path != baseDir {
				return filepath.SkipDir
			}
			return nil
		}

		if isSymlink && !opts.FollowSymlinks {
			return skipDir()
		}

		info, resolved, infoErr := w.entryInfo(path, d, opts)
		if infoErr != nil {
			st.failed = true
			sendResult(ctx, ch, Result{Err: infoErr})
			return nil //nolint:nilerr // per-file errors are reported on the channel
		}
		if info == nil {
			return skipDir() // not a regular file, or an unreadable link
		}

		if info.IsDir() {
			if !recursive && path != baseDir {
				return skipDir()
			}
			w.walkSymlinkTarget(ctx, resolved, matchPattern, opts, ch, st)
			return skipDir()
		}

		// Match against the pattern.
		if !matchPath(matchPattern, path) {
			return nil
		}

		// The pattern matched something usable, even if the file is later
		// skipped as empty: that is not a "no files matched" situation.
		st.matched++

		// Skip zero-length files unless IncludeZeroLen is set.
		if info.Size() == 0 && !opts.IncludeZeroLen {
			if opts.ZeroLen != nil {
				opts.ZeroLen.AddZeroLen(1)
			}
			return nil
		}

		w.processFileEntry(ctx, path, info, ch)
		return nil
	}
}

// entryInfo returns the metadata of the file an entry refers to. Symlinks are
// resolved only when opts.FollowSymlinks is set. A nil FileInfo with a nil
// error means "skip this entry": a symlink that is not followed, a broken link,
// or something that is not a regular file or directory. resolved is the
// canonical path of a followed symlink and is only used for directories.
func (w *Walker) entryInfo(path string, d os.DirEntry, opts WalkOptions) (os.FileInfo, string, error) {
	if d.Type()&os.ModeSymlink == 0 {
		info, err := d.Info()
		if err != nil {
			return nil, "", err
		}
		if !info.Mode().IsRegular() {
			return nil, "", nil
		}
		return info, "", nil
	}

	if !opts.FollowSymlinks {
		return nil, "", nil
	}

	// DirEntry.Info is an lstat, so a link's own metadata (its target string as
	// the "size") must never be used: resolve the link and stat the target.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil // broken link
		}
		return nil, "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil // broken link
		}
		return nil, "", err
	}
	if info.IsDir() || info.Mode().IsRegular() {
		return info, resolved, nil
	}
	return nil, "", nil
}

// walkSymlinkTarget recursively walks the resolved target of a directory
// symlink. seen is keyed by resolved path and prevents symlink loops; the
// caller passes the already-resolved target.
func (w *Walker) walkSymlinkTarget(
	ctx context.Context, target, matchPattern string,
	opts WalkOptions, ch chan<- Result, st *walkState,
) {
	// Prevent infinite loops: if we've already visited this target, skip it.
	if st.seen[target] {
		return
	}
	st.seen[target] = true

	// Walk the resolved target directory.
	walkFn := w.createWalkFn(ctx, target, matchPattern, opts, ch, st)
	if walkErr := filepath.WalkDir(target, walkFn); walkErr != nil {
		st.failed = true
		sendResult(ctx, ch, Result{Err: walkErr})
	}
}

// processFileEntry creates a FileInfo and sends it on the channel.
func (w *Walker) processFileEntry(
	ctx context.Context,
	path string,
	info os.FileInfo,
	ch chan<- Result,
) {
	select {
	case <-ctx.Done():
		return
	default:
	}

	fi := dupe.FileInfo{
		Path:    path,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}

	// Get device/inode identity and link count (platform-specific).
	fi.Dev, fi.Inode, fi.NumLinks = getFileIdentity(path, info)

	// Send the result.
	sendResult(ctx, ch, Result{Info: fi})
}

// processFile handles a single file (non-directory) pattern. The path is
// stat'ed with symlinks followed: an argument naming a link means its target.
// Explicitly named non-regular files (a device, a FIFO) are ignored rather than
// opened.
func (w *Walker) processFile(
	ctx context.Context,
	path string,
	opts WalkOptions,
	ch chan<- Result,
	st *walkState,
) {
	info, err := os.Stat(path)
	if err != nil {
		st.failed = true
		sendResult(ctx, ch, Result{Err: err})
		return
	}

	if !info.Mode().IsRegular() {
		return
	}
	st.matched++

	if info.Size() == 0 && !opts.IncludeZeroLen {
		if opts.ZeroLen != nil {
			opts.ZeroLen.AddZeroLen(1)
		}
		return
	}

	w.processFileEntry(ctx, path, info, ch)
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
