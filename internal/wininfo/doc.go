// Package wininfo collects the Windows handle probes shared by the scanner, the
// hardlink limit check and the metadata restoration: each of them needs the same
// GetFileInformationByHandle call, and keeping three copies of the call and its
// field mapping is how the 32-bit struct size bug hid for so long.
package wininfo
