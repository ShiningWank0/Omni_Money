//go:build !windows

// Package fileprivacy applies OS-specific owner-only permissions to files.
package fileprivacy

import (
	"errors"
	"fmt"
	"os"
)

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

// HardenDirectory applies the owner-only directory mode used for vault and
// snapshot roots.
func HardenDirectory(path string) error {
	return os.Chmod(path, 0700) // #nosec G302 -- financial data directories are private.
}

func ValidateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("private path is not a real directory")
	}
	if info.Mode().Perm() != 0700 {
		return fmt.Errorf("private directory permissions must be 0700, got %04o", info.Mode().Perm())
	}
	return nil
}
