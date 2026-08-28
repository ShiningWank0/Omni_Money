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

// Integrity-protected documents contain authorization policy as well as hashes.
// Until owner/DACL validation is implemented against the already-open handle,
// Windows must fail closed instead of treating unavailable Unix mode bits as
// proof that another local user cannot replace the file.
func validatePlatformIntegrity(_ os.FileMode) error {
	return errors.New("integrity-protected file permission validation is unavailable on Windows")
}
