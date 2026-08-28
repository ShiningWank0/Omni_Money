//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package serverauth

import (
	"os"
	"syscall"
)

// trustedSetupTokenOwner accepts only root or the effective server account.
// A different UID can exercise owner permissions even when group/other write
// bits are clear, so an unavailable owner identity must fail closed.
func trustedSetupTokenOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return false
	}
	return trustedSetupTokenUID(uint64(stat.Uid))
}

func trustedSetupTokenUID(uid uint64) bool {
	effectiveUID := os.Geteuid()
	if effectiveUID < 0 {
		return false
	}
	// #nosec G115 -- the signed value is checked non-negative before the
	// widening conversion and Unix effective UIDs are represented in this range.
	return uid == 0 || uid == uint64(effectiveUID)
}
