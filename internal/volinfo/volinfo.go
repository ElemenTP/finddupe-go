// Package volinfo reports filesystem-level facts about the volume a path lives
// on. It exists so the CoW clone and the extent query share one implementation
// (and one cache) of the Windows cluster-size probe instead of keeping two
// copies in step.
package volinfo

import "errors"

// ErrUnsupported reports that volume information is unavailable on this
// platform.
var ErrUnsupported = errors.New("volume information is only available on Windows")
