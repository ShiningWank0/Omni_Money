//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package serverauth

import (
	"os"
	"testing"
)

func TestSetupTokenOwnerValidationFailsClosed(t *testing.T) {
	info, err := os.Lstat(".")
	if err != nil {
		t.Fatal(err)
	}
	if trustedSetupTokenOwner(info) {
		t.Fatal("setup token ownership was accepted on a platform without owner validation")
	}
}
