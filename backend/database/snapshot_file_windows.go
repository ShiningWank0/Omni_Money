//go:build windows

package database

import "os"

func openSnapshotFile(path string) (*os.File, error) {
	return os.Open(path) // #nosec G304 -- path is a validated snapshot basename under a private directory.
}

func validSnapshotFile(info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0022 == 0
}

func syncDirectory(path string) error { return nil }
