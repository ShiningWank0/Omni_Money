//go:build !windows

// Package fileprivacy applies OS-specific owner-only permissions to files.
package fileprivacy

import "os"

// CreateExclusive atomically creates an owner-only file beneath a pinned root.
func CreateExclusive(root *os.Root, _ string, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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
