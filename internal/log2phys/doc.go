// Package log2phys holds the layout of the structure the macOS
// fcntl(F_LOG2PHYS_EXT) call fills in.
//
// The extent query in internal/extent and the darwinfiemap probe in testtools
// both encode and decode that structure. Keeping one copy of the offsets means the
// probe cannot drift away from what production reads, which is the whole point of
// having the probe.
package log2phys
