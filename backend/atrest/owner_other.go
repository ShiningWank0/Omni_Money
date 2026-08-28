//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package atrest

import "os"

// Owner/DACL validation is unavailable on this platform, so the at-rest
// authorization contract fails closed.
func trustedAttestationOwner(os.FileInfo) bool { return false }
