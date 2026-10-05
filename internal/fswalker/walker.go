// Package fswalker walks the filesystem, matching glob patterns and producing file metadata.
package fswalker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"finddupe/internal/dupe"
	"finddupe/internal/fileid"
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
// already-visited directories (symlink loop prevention within this pattern) and
// whether the pattern matched anything.
type walkState struct {
	seen    map[dirKey]bool
	matched int
	failed  bool
}

// dirKey identifies a directory the walk has entered. Physical identity is
// preferred, because the walker sees the same directory under two spellings: the
// path WalkDir builds from the walk root, and the canonical path a symlink
// resolved to. When the walk root itself is spelled through a symlink (or the
// prefix is one, as /var is on macOS) those spellings differ, and a path-keyed
// set walked the subtree twice. Platforms whose FileInfo exposes no identity fall
// back to the path spelling.
type dirKey struct {
	dev   uint64
	inode uint64
	path  string
}

// dirKeyOf builds the key for a directory that was just stat'ed.
func dirKeyOf(path string, info os.FileInfo) dirKey {
	if info != nil {
		if dev, inode, _, ok := fileid.FromFileInfo(info); ok {
			return dirKey{dev: dev, inode: inode}
		}
	}
	return dirKey{path: path}
}

// enter records a directory and reports whether it had already been visited.
func (st *walkState) enter(path string, info os.FileInfo) bool {
	key := dirKeyOf(path, info)
	if st.seen[key] {
		return true
	}
	if st.seen == nil {
		st.seen = make(map[dirKey]bool)
	}
	st.seen[key] = true
	return false
}

// skipVisitedDir records a plain directory for the loop check and reports whether
// the walk must skip it because the same directory was already entered (under
// either spelling). Nothing is recorded unless symlinks are followed: without -j
// no directory symlink is ever walked, so the set could only waste memory.
func skipVisitedDir(baseDir, path string, d os.DirEntry, opts WalkOptions, st *walkState) bool {
	if !opts.FollowSymlinks {
		return false
	}

	// The stat can fail; the directory is then identified by its path spelling.
	info, infoErr := d.Info()
	if infoErr != nil {
		info = nil
	}

	// The root of the current walk is never skipped: it is the entry the walk
	// starts from and may have been registered by the symlink that led here.
	// Recording it is what makes a symlink pointing back at it terminate.
	return st.enter(path, info) && path != baseDir
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
	for _, pattern := range patterns {
		if ctx.Err() != nil {
			return
		}
		w.walkPattern(ctx, pattern, opts, ch)
	}
}

// walkPattern walks a single pattern. A pattern that names an existing path is
// scanned as written (recursively when it is a directory); otherwise it is
// treated as a glob. A pattern that matches no usable file produces a
// [NoMatchError] so a typo cannot pass for a successful run.
func (w *Walker) walkPattern(
	ctx context.Context, pattern string, opts WalkOptions, ch chan<- Result,
) {
	// Convert to absolute path for consistent dedup and output.
	absPattern, err := filepath.Abs(pattern)
	if err != nil {
		absPattern = pattern
	}

	// seen is deliberately scoped to one pattern: sharing it across patterns
	// makes a later pattern skip every directory an earlier one recorded, so
	// "/data/**/*.txt /data/**/*.jpg" would silently lose whole subtrees and
	// report "no files matched" for a pattern that does match. Duplicate files
	// reached by several patterns are deduplicated downstream
	// (scanChecksums.seenPaths, Detector.seenPaths).
	st := &walkState{}

	if _, statErr := os.Stat(absPattern); statErr == nil {
		// The pattern names an existing path, so it is taken literally: a path
		// may contain glob characters ("/data/[2020] photos", "report[1].txt")
		// and splitting it on them would scan the wrong tree or nothing at all.
		w.walkLiteral(ctx, absPattern, opts, ch, st)
	} else {
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
		// A prefix that cannot be resolved at all (missing directory, a path
		// component that is not a directory, a broken link) means the pattern
		// matches nothing; the caller reports that. Anything else (permissions,
		// I/O) is a real error and is reported with the pattern it came from.
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return
		}
		st.failed = true
		sendResult(ctx, ch, Result{Err: fmt.Errorf("pattern %q: %w", pattern, err)})
		return
	}

	if !info.IsDir() {
		// The literal prefix exists but is a regular file, so the remaining
		// components (e.g. "notes.txt/*.jpg") cannot match anything.
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
		return driveRoot(baseDir), match
	}

	// No part of the pattern exists: fall back to the first metacharacter so the
	// walker can still report "no files matched" for the intended tree.
	wildIdx := strings.IndexAny(pattern, "*?[")
	if wildIdx < 0 {
		// No wildcard — treat as directory, scan everything.
		return pattern, "**"
	}

	// Find the last separator before the wildcard. walkGlob is only ever called
	// with an absolute pattern, so there always is one (an absolute path has a
	// separator before its first metacharacter); the guard keeps a relative
	// pattern from slicing out of range if that ever changes.
	sepIdx := strings.LastIndexAny(pattern[:wildIdx], "/\\")
	if sepIdx < 0 {
		return ".", pattern
	}
	if sepIdx == 0 {
		// The wildcard sits directly below the volume root ("/*.conf"): the prefix
		// before the separator is the empty string, which no stat call can resolve,
		// and "nothing matched" was the result. The separator itself is the base
		// directory.
		return pattern[:1], pattern[1:]
	}

	return driveRoot(pattern[:sepIdx]), pattern[sepIdx+1:]
}

// driveRoot gives a bare Windows drive letter its separator back. "C:" on its own
// names that drive's *current* directory, not its root, so walking it would scan
// the wrong tree; "C:\\" (or "C:/", as written) is the root.
func driveRoot(baseDir string) string {
	if len(baseDir) == 2 && baseDir[1] == ':' {
		return baseDir + string(filepath.Separator)
	}
	return baseDir
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
	pattern := compilePattern(matchPattern, baseDir)

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
		depth := pathDepth(baseDir, path)

		// Plain directories are walked by WalkDir itself; recording them keeps a
		// symlink pointing at a directory that is already (or later) part of the
		// tree from being walked twice (see skipVisitedDir).
		if d.IsDir() && !isSymlink {
			if skipVisitedDir(baseDir, path, d, opts, st) {
				return filepath.SkipDir
			}

			// Do not descend deeper than the pattern can match: "*.txt" needs one
			// level below the base directory, "*/x.txt" two, "**" any number.
			if !pattern.descends(depth) {
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
			if !pattern.descends(depth) {
				return skipDir()
			}
			w.walkSymlinkTarget(ctx, resolved, matchPattern, opts, ch, st)
			return skipDir()
		}

		// The entry is matched where it sits in the walk; a followed link is then
		// reported under its resolved target, which is the file the action layer
		// must operate on (os.Link on a symlink path would link the symlink, and
		// removing it would delete the link instead of the duplicate).
		if !pattern.matches(path, depth) {
			return nil
		}
		if resolved != "" {
			path = resolved
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

// symlinkTarget returns the canonical target of path when path is itself a
// symbolic link, and "" when it is not.
func symlinkTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", nil
	}
	return filepath.EvalSymlinks(path)
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
// symlink; the caller passes the already-resolved target. The target is recorded
// in the same set as the plain directories, so a target that is already part of
// the tree (under either spelling) is not walked twice and a loop terminates.
func (w *Walker) walkSymlinkTarget(
	ctx context.Context, target, matchPattern string,
	opts WalkOptions, ch chan<- Result, st *walkState,
) {
	// The stat is needed for the identity; a target that cannot be stat'ed is
	// walked anyway and reports its own errors.
	info, _ := os.Stat(target)
	if st.enter(target, info) {
		return
	}

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
	if dev, inode, links, ok := fileid.FromFileInfo(info); ok {
		fi.Dev, fi.Inode, fi.NumLinks = dev, inode, links
	}

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

	// An explicit argument that is itself a link is scanned as its target, so the
	// action layer operates on the file whose content was verified rather than on
	// the link (os.Link would link the symlink; os.Remove would delete the link).
	if target, linkErr := symlinkTarget(path); linkErr != nil {
		st.failed = true
		sendResult(ctx, ch, Result{Err: linkErr})
		return
	} else if target != "" {
		path = target
	}

	if info.Size() == 0 && !opts.IncludeZeroLen {
		if opts.ZeroLen != nil {
			opts.ZeroLen.AddZeroLen(1)
		}
		return
	}

	w.processFileEntry(ctx, path, info, ch)
}

// compiledPattern is a match pattern prepared once per walk: compiling up front
// keeps the per-file cost to the matching itself instead of re-splitting and
// re-parsing the pattern for every candidate.
type compiledPattern struct {
	// parts is the component-wise pattern, with runs of "**" collapsed.
	parts []string

	// depth is len(parts): for a pattern without "**" a file must sit exactly
	// that many components below the base directory.
	depth int

	// recursive is true when the pattern contains "**", which matches any
	// number of components.
	recursive bool

	// all is true for the pattern "**": every regular file matches.
	all bool

	// baseDir is the walk root the pattern is relative to.
	baseDir string
}

// compilePattern prepares pattern for repeated matching below baseDir.
func compilePattern(pattern, baseDir string) compiledPattern {
	if pattern == "**" {
		return compiledPattern{all: true, recursive: true, baseDir: baseDir}
	}

	// Normalise separators once: the component matcher works on "/".
	slash := filepath.ToSlash(pattern)
	rawParts := strings.Split(slash, "/")

	parts := make([]string, 0, len(rawParts))
	for i, part := range rawParts {
		if part == "**" && i > 0 && parts[len(parts)-1] == "**" {
			continue // "**/**" is equivalent to "**"
		}
		parts = append(parts, part)
	}

	return compiledPattern{
		parts:     parts,
		depth:     len(parts),
		recursive: strings.Contains(slash, "**"),
		baseDir:   baseDir,
	}
}

// descends reports whether a directory this many components below baseDir can
// still contain a match, so WalkDir knows whether to enter it.
func (p compiledPattern) descends(depth int) bool {
	if p.all || p.recursive {
		return true
	}
	return depth < p.depth
}

// matches reports whether the file at path, depth components below the walk
// base directory, matches the pattern.
func (p compiledPattern) matches(path string, depth int) bool {
	if p.all {
		return true
	}
	if !p.recursive && depth != p.depth {
		return false
	}
	if !p.recursive && p.depth == 1 {
		// Single-component pattern: match the basename without splitting.
		base := path[strings.LastIndexAny(path, "/\\")+1:]
		return matchComponent(p.parts[0], base)
	}
	return matchComponents(p.parts, relativeParts(p.baseDir, path))
}

// relativeParts returns the components of path below baseDir. WalkDir always
// builds path from baseDir, so a plain prefix cut is exact here.
func relativeParts(baseDir, path string) []string {
	rest := path
	if len(path) > len(baseDir) {
		rest = path[len(baseDir):]
	}
	return strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' })
}

// pathDepth returns how many components path sits below baseDir (0 for baseDir
// itself).
// separators holds the characters that separate path components exactly once:
// Unix and Windows both accept "/", and Windows also accepts its own separator,
// which must not be added a second time on Unix.
var separators = func() string {
	if filepath.Separator == '/' {
		return "/"
	}
	return "/" + string(filepath.Separator)
}()

func pathDepth(baseDir, path string) int {
	// The separator that ends baseDir is not a component boundary, so it is
	// trimmed first: "/" plus "x.conf" is one component below "/". Without this a
	// wildcard directly below the volume root matched nothing, because the only
	// separator in the path belonged to the base directory.
	base := strings.TrimRight(baseDir, separators)
	rest, ok := strings.CutPrefix(path, base)
	if !ok || rest == "" || !strings.ContainsAny(rest[:1], separators) {
		return 0
	}
	if rest = strings.TrimLeft(rest, separators); rest == "" {
		return 0
	}

	depth := 1
	for _, c := range rest {
		if strings.ContainsRune(separators, c) {
			depth++
		}
	}
	return depth
}

// matchComponents reports whether the path components satisfy the pattern
// components: "**" matches zero or more components, every other component must
// match exactly one via [filepath.Match].
//
// The greedy backtracking below is linear in the number of components; a
// recursive implementation retried every split point at every "**", which made a
// pattern such as "**/**/**/**/*.txt" cost minutes per candidate file.
func matchComponents(patParts, nameParts []string) bool {
	var (
		patIdx  int
		nameIdx int
		starPat = -1
		starPos int
	)

	for nameIdx < len(nameParts) {
		switch {
		case patIdx < len(patParts) && patParts[patIdx] == "**":
			// Try "**" against zero components first, and remember where to
			// resume if that fails.
			starPat, starPos = patIdx, nameIdx
			patIdx++
		case patIdx < len(patParts) && matchComponent(patParts[patIdx], nameParts[nameIdx]):
			patIdx++
			nameIdx++
		case starPat >= 0:
			// Let the last "**" consume one more component.
			starPos++
			nameIdx = starPos
			patIdx = starPat + 1
		default:
			return false
		}
	}

	// Trailing "**" components match zero components.
	for patIdx < len(patParts) && patParts[patIdx] == "**" {
		patIdx++
	}
	return patIdx == len(patParts)
}

// matchComponent matches one path component against one pattern component. Where
// the platform folds case for filenames (Windows) the comparison folds it too, so
// "*.TXT" finds "photo.txt" as a user of that platform expects; a malformed
// pattern matches nothing, which the caller reports as a pattern that matched no
// files.
func matchComponent(pattern, name string) bool {
	pattern, name = foldForMatch(pattern, name)
	matched, err := filepath.Match(pattern, name)
	return err == nil && matched
}
