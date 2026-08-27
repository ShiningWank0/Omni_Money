//go:build windows

package secretfile

import (
	"errors"
	"os"
)

func secureOpen(path string, _ policy) (*os.File, error) { return os.Open(path) }

func validatePlatformConfidential(_ string) error {
	return errors.New("confidential secret permission validation is unavailable on Windows")
}

// Public certificates and hashed credentials retain their existing Windows
// behavior. Confidential raw keys are rejected above until DACL checks exist.
func validatePlatformIntegrity(_ os.FileMode) error { return nil }
