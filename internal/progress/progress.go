// Package progress provides a simple progress reporter for file scanning.
package progress

import (
	"context"
	"fmt"
	"os"
	"time"

	"finddupe/internal/dupe"
)

// defaultInterval is how often the progress line is refreshed.
const defaultInterval = 500 * time.Millisecond

// Reporter displays a live-updating progress indicator.
type Reporter struct {
	stats    *dupe.Stats
	interval time.Duration
}

// New creates a new Reporter.
func New(stats *dupe.Stats) *Reporter {
	return &Reporter{
		stats:    stats,
		interval: defaultInterval,
	}
}

// IsTerminal reports whether f is a terminal. It asks the operating system
// rather than checking for a character device: /dev/null is one, and treating it
// as a terminal would let --interactive wait for an answer that can never come
// (and would draw the progress line into a redirected stream).
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return isTerminal(f.Fd())
}

// Run displays the progress indicator until ctx is cancelled. It returns
// immediately when stderr is not a terminal, so escape sequences can never end
// up in a redirected log.
func (r *Reporter) Run(ctx context.Context) {
	if !IsTerminal(os.Stderr) {
		return
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.clear()
			return
		case <-ticker.C:
			r.print()
		}
	}
}

// print writes the current progress line.
func (r *Reporter) print() {
	total := r.stats.TotalFiles.Load()
	r.clear()
	fmt.Fprintf(os.Stderr, "Scanned %4d files...", total)
}

// clear erases the progress line.
func (r *Reporter) clear() {
	fmt.Fprint(os.Stderr, "\033[2K\r")
}
