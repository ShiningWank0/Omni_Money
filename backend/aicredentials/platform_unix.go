//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package aicredentials

import (
	"os"
)

func hasInsecurePermissions(mode os.FileMode) bool {
	return mode.Perm()&0o033 != 0
}

func syncDirectory(path string) error {
	// #nosec G304 -- path is the parent of the operator-selected credential file;
	// opening it is required to fsync the directory after an atomic rename.
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
