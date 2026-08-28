//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package atrest

import (
	"os"
	"syscall"
)

// trustedAttestationOwner accepts only the server account itself or root.
// Another UID can use owner permissions to chmod and replace a path even when
// its current group/other mode bits are read-only.
func trustedAttestationOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return false
	}
	uid := uint64(stat.Uid)
	euid := os.Geteuid()
	if euid < 0 {
		return false
	}
	// #nosec G115 -- the signed value is checked non-negative before the
	// widening conversion and Unix effective UIDs are represented in this range.
	return uid == 0 || uid == uint64(euid)
}
