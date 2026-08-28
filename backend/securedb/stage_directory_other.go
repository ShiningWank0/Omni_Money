//go:build !windows

package securedb

import "os"

func createPrivateMigrationDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil { // #nosec G301 -- the owner needs traversal; group and other receive no access.
		return err
	}
	return os.Chmod(path, 0700) // #nosec G302 -- enforce owner-only traversal independent of umask behavior.
}
