//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package secretfile

import (
	"errors"
	"fmt"
	"os"
)

func secureOpen(path string, _ policy) (*os.File, error) { return os.Open(path) }

func validatePlatformConfidential(_ string) error {
	return errors.New("confidential secret permission validation is unavailable on this platform")
}

func validatePlatformIntegrity(permissions os.FileMode) error {
	if permissions&0o133 != 0 {
		return fmt.Errorf("secret file permissions are unsafe: %04o", permissions)
	}
	return nil
}
