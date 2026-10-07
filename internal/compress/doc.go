// Package compress reports whether a file's content is stored compressed by the
// filesystem (transparent compression). It is a heuristic used by the keeper
// policy: a CoW clone inherits the source's extent layout, so keeping a
// compressed member as the clone source keeps the group compressed, while
// choosing an uncompressed member spreads its layout to every victim.
//
// Detection is best-effort by design: an unavailable or inconclusive answer is
// reported as "not compressed", which only means the keeper keeps its default
// order.
package compress
