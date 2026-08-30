//go:build !windows

// Package fileprivacy applies OS-specific owner-only permissions to files.
package fileprivacy

import "os"

// CreateExclusive atomically creates an owner-only file beneath a pinned root.
func CreateExclusive(root *os.Root, _ string, name string) (*os.File, error) {
	// The descriptor is retained across the private-file lifecycle.  Callers
	// may need to rewind/read it after generation (and must not reopen a path),
	// so it is deliberately read/write rather than write-only.
	return root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
}

// Harden restricts a newly created file to its owner.
func Harden(file *os.File) error {
	return file.Chmod(0600)
}

// IsPrivate reports the owner-only mode expected on Unix-like systems. The
// open descriptor is accepted so callers can use one identity throughout the
// create/stat/check sequence; Unix permission checks use the supplied info.
func IsPrivate(_ *os.File, info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600
}
