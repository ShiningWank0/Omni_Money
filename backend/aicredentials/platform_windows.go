//go:build windows

package aicredentials

import "os"

// Windows does not expose Unix group/other permission bits through os.FileMode.
func hasInsecurePermissions(_ os.FileMode) bool {
	return false
}

// Rename provides the atomic replacement boundary on Windows. There is no
// portable standard-library directory fsync operation.
func syncDirectory(_ string) error {
	return nil
}
