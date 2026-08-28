//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package serverauth

import "os"

// Owner/DACL validation is unavailable on this platform. Initial administrator
// setup is an authorization boundary, so accepting the file would be unsafe.
func trustedSetupTokenOwner(os.FileInfo) bool { return false }
