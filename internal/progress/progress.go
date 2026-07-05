// Package progress provides a simple progress reporter for file scanning.
package progress

import (
	"context"
	"fmt"
	"os"
	"time"

	"finddupe/internal/dupe"
)

// Reporter displays a live-updating progress indicator.
type Reporter struct {
	stats    *dupe.Stats
	interval time.Duration
}

// New creates a new Reporter.
func New(stats *dupe.Stats) *Reporter {
	return &Reporter{
		stats:    stats,
		interval: 500 * time.Millisecond,
	}
}

// Run displays the progress indicator until ctx is cancelled.
func (r *Reporter) Run(ctx context.Context) {
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
