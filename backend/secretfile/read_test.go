//go:build !windows

package secretfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostConfidentialPermissionsAndSymlink(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "secret")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfidential(path, 64); err != nil {
		t.Fatalf("0600 secret rejected: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfidential(path, 64); err == nil {
		t.Fatal("0644 host secret accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "secret-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfidential(link, 64); err == nil {
		t.Fatal("symlinked secret accepted")
	}
}

func TestIntegrityAndDockerPermissionPolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "public-config")
	if err := os.WriteFile(path, []byte("public"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIntegrityProtected(path, 64); err != nil {
		t.Fatalf("0644 integrity-protected file rejected: %v", err)
	}
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIntegrityProtected(path, 64); err == nil {
		t.Fatal("group-writable integrity-protected file accepted")
	}
	if err := validateConfidentialPermissions("/run/secrets/audit-key", 0o444); err != nil {
		t.Fatalf("Docker 0444 secret rejected: %v", err)
	}
	if err := validateConfidentialPermissions("/run/secrets/audit-key", 0o644); err == nil {
		t.Fatal("writable Docker secret accepted")
	}
	if err := validateConfidentialPermissions("/run/secrets/nested/audit-key", 0o444); err == nil {
		t.Fatal("nested /run/secrets path received Docker permission exception")
	}
}
