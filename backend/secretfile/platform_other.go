//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package secretfile

import (
	"errors"
	"os"
)

func secureOpen(path string, _ policy) (*os.File, error) { return os.Open(path) }

func validatePlatformConfidential(_ string) error {
	return errors.New("confidential secret permission validation is unavailable on this platform")
}
