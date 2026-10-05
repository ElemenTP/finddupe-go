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

// IsTerminal reports whether the progress line may be drawn on f. The check is
// deliberately dependency-free: a character device is treated as a terminal
// (which also accepts /dev/null, where a stray escape sequence is harmless),
// while a pipe or a regular file is not, so redirected output never collects
// escape sequences or a stream of "Scanned N files..." lines.
func IsTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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
