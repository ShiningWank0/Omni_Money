//go:build windows

package secretfile

import (
	"errors"
	"os"
)

func secureOpen(path string, _ policy) (*os.File, error) { return os.Open(path) }

// Raw TOTP seeds are rejected until Windows DACL validation is implemented.
func validatePlatformConfidential(_ string) error {
	return errors.New("confidential secret permission validation is unavailable on Windows")
}
