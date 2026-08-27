//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package aicredentials

import "os"

func hasInsecurePermissions(mode os.FileMode) bool {
	return mode.Perm()&0o033 != 0
}

func syncDirectory(_ string) error {
	return nil
}
