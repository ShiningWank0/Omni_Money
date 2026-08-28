//go:build windows

package secretfile

import "testing"

func TestWindowsIntegrityProtectedFilesFailClosedWithoutDACLValidation(t *testing.T) {
	if err := validatePlatformIntegrity(0); err == nil {
		t.Fatal("Windows integrity-protected file was accepted without DACL validation")
	}
	if err := validatePlatformConfidential(""); err == nil {
		t.Fatal("Windows confidential file was accepted without DACL validation")
	}
}
