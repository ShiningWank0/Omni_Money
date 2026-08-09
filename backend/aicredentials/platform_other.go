//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package aicredentials

import "os"

func secureOpen(path string) (*os.File, error) {
	return os.Open(path)
}

func hasInsecurePermissions(mode os.FileMode) bool {
	return mode.Perm()&0o033 != 0
}

func syncDirectory(_ string) error {
	return nil
}
