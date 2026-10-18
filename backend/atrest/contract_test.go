package atrest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequireServerProtectionAcceptsAttestationWithoutOperationalDates(t *testing.T) {
	root, dbPath, attestationPath := writeTestAttestation(t, nil)

	content, err := os.ReadFile(attestationPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"verified_at", "recovery_tested_at", "next_rotation_at"} {
		if _, present := document[field]; present {
			t.Fatalf("initial deployment fixture unexpectedly includes %s", field)
		}
	}
	status, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath)
	if err != nil {
		t.Fatal(err)
	}
	if status.DataRoot != root || status.Provider != "luks2" || status.KeyID != "luks-prod-2026-01" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestRequireServerProtectionOperationalDatesDoNotGateStartup(t *testing.T) {
	anchor := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	tests := []struct {
		name                                         string
		verifiedAt, recoveryTestedAt, nextRotationAt time.Time
	}{
		{name: "legacy dates", verifiedAt: anchor, recoveryTestedAt: anchor, nextRotationAt: anchor.AddDate(0, 6, 0)},
		{name: "old verification and recovery", verifiedAt: anchor.AddDate(-20, 0, 0), recoveryTestedAt: anchor.AddDate(-20, 0, 0), nextRotationAt: anchor.AddDate(0, 6, 0)},
		{name: "past rotation plan", verifiedAt: anchor, recoveryTestedAt: anchor, nextRotationAt: anchor.AddDate(-20, 0, 0)},
		{name: "distant operational dates", verifiedAt: anchor.AddDate(100, 0, 0), recoveryTestedAt: anchor.AddDate(100, 0, 0), nextRotationAt: anchor.AddDate(100, 0, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, dbPath, attestationPath := writeTestAttestation(t, func(document *attestationDocument) {
				document.VerifiedAt = test.verifiedAt
				document.RecoveryTestedAt = test.recoveryTestedAt
				document.NextRotationAt = test.nextRotationAt
			})
			// Both an initial deployment and an existing database must be
			// independent of the age of optional operational records.
			for _, existing := range []bool{false, true} {
				if existing {
					if err := os.WriteFile(dbPath, []byte("encrypted-placeholder"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				status, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath)
				if err != nil {
					t.Fatalf("existing=%v: operational dates blocked startup: %v", existing, err)
				}
				if !status.VerifiedAt.Equal(test.verifiedAt) ||
					!status.RecoveryTestedAt.Equal(test.recoveryTestedAt) ||
					!status.NextRotationAt.Equal(test.nextRotationAt) {
					t.Fatalf("legacy operational metadata changed: %#v", status)
				}
			}
		})
	}
}

func TestRequireServerProtectionRejectsMissingAttestation(t *testing.T) {
	_, dbPath, attestationPath := writeTestAttestation(t, nil)
	if err := os.Remove(attestationPath); err != nil {
		t.Fatal(err)
	}
	if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath); err == nil {
		t.Fatal("missing storage protection attestation was accepted")
	}
}

func TestRequireServerProtectionRejectsInvalidContract(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		mutate func(*attestationDocument)
	}{
		{name: "missing mode", mode: ""},
		{name: "wrong mode", mode: "plaintext"},
		{name: "wrong version", mode: ModeExternalEncryptedVolume, mutate: func(document *attestationDocument) {
			document.Version = 2
		}},
		{name: "wrong protection", mode: ModeExternalEncryptedVolume, mutate: func(document *attestationDocument) {
			document.Protection = "plaintext"
		}},
		{name: "missing provider", mode: ModeExternalEncryptedVolume, mutate: func(document *attestationDocument) {
			document.Provider = ""
		}},
		{name: "invalid key ID", mode: ModeExternalEncryptedVolume, mutate: func(document *attestationDocument) {
			document.KeyID = "short"
		}},
		{name: "database outside root", mode: ModeExternalEncryptedVolume, mutate: func(document *attestationDocument) {
			document.DataRoot = filepath.Join(document.DataRoot, "other")
			if err := os.Mkdir(document.DataRoot, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, dbPath, attestationPath := writeTestAttestation(t, test.mutate)
			if _, err := RequireServerProtection(dbPath, test.mode, attestationPath); err == nil {
				t.Fatal("invalid at-rest contract was accepted")
			}
		})
	}
}

func TestRequireServerProtectionRejectsDuplicateFieldsAndInRootAttestation(t *testing.T) {
	root, dbPath, attestationPath := writeTestAttestation(t, nil)
	content, err := os.ReadFile(attestationPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(string(content), `"version":1`, `"version":1,"version":1`, 1)
	if err := os.WriteFile(attestationPath, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath); err == nil {
		t.Fatal("duplicate JSON field was accepted")
	}

	_, _, outsidePath := writeTestAttestation(t, nil)
	inRootPath := filepath.Join(root, "attestation.json")
	inRootContent, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inRootPath, inRootContent, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, inRootPath); err == nil {
		t.Fatal("attestation inside financial data root was accepted")
	}
}

func TestRequireServerProtectionRejectsUnsafeDataPathComponents(t *testing.T) {
	t.Run("nested symlink escapes root", func(t *testing.T) {
		root, _, attestationPath := writeTestAttestation(t, nil)
		outside := t.TempDir()
		link := filepath.Join(root, "nested")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		dbPath := filepath.Join(link, "omni_money.db")
		if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath); err == nil || !strings.Contains(err.Error(), "symbolic links") {
			t.Fatalf("nested symlink escape error = %v", err)
		}
	})

	t.Run("nested symlink remains forbidden inside root", func(t *testing.T) {
		root, _, attestationPath := writeTestAttestation(t, nil)
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "nested")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := RequireServerProtection(filepath.Join(link, "omni_money.db"), ModeExternalEncryptedVolume, attestationPath); err == nil {
			t.Fatal("nested in-root symlink was accepted")
		}
	})

	t.Run("data root itself is a symlink", func(t *testing.T) {
		root, _, attestationPath := writeTestAttestation(t, nil)
		link := filepath.Join(filepath.Dir(root), "linked-data")
		if err := os.Symlink(root, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		rewriteTestAttestation(t, attestationPath, func(document *attestationDocument) {
			document.DataRoot = link
		})
		if _, err := RequireServerProtection(filepath.Join(link, "omni_money.db"), ModeExternalEncryptedVolume, attestationPath); err == nil {
			t.Fatal("symlink data root was accepted")
		}
	})

	t.Run("unsafe writable data root", func(t *testing.T) {
		root, dbPath, attestationPath := writeTestAttestation(t, nil)
		if err := os.Chmod(root, 0o770); err != nil {
			t.Fatal(err)
		}
		if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath); err == nil || !strings.Contains(err.Error(), "writable by group") {
			t.Fatalf("unsafe root permissions error = %v", err)
		}
	})

	t.Run("non-directory parent", func(t *testing.T) {
		root, _, attestationPath := writeTestAttestation(t, nil)
		parent := filepath.Join(root, "not-a-directory")
		if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := RequireServerProtection(filepath.Join(parent, "omni_money.db"), ModeExternalEncryptedVolume, attestationPath); err == nil || !strings.Contains(err.Error(), "must be a directory") {
			t.Fatalf("non-directory parent error = %v", err)
		}
	})

	t.Run("unsafe writable parent", func(t *testing.T) {
		root, _, attestationPath := writeTestAttestation(t, nil)
		parent := filepath.Join(root, "shared")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o770); err != nil {
			t.Fatal(err)
		}
		if _, err := RequireServerProtection(filepath.Join(parent, "omni_money.db"), ModeExternalEncryptedVolume, attestationPath); err == nil || !strings.Contains(err.Error(), "writable by group") {
			t.Fatalf("unsafe parent permissions error = %v", err)
		}
	})
}

func TestRequireServerProtectionAllowsExistingAndMissingSafeDataPaths(t *testing.T) {
	t.Run("Docker-style root and missing leaf", func(t *testing.T) {
		root, _, attestationPath := writeTestAttestation(t, nil)
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatal(err)
		}
		dbPath := filepath.Join(root, "control", "omni_control.db")
		if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath); err != nil {
			t.Fatalf("safe missing candidate was rejected: %v", err)
		}
	})

	t.Run("existing regular database", func(t *testing.T) {
		root, dbPath, attestationPath := writeTestAttestation(t, nil)
		if err := os.WriteFile(dbPath, []byte("encrypted-placeholder"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath); err != nil {
			t.Fatalf("safe existing database was rejected: %v", err)
		}
		if root == "" {
			t.Fatal("test root is empty")
		}
	})
}

func TestRequireServerProtectionRejectsUnsafeAttestationPermissions(t *testing.T) {
	_, dbPath, attestationPath := writeTestAttestation(t, nil)
	if err := os.Chmod(attestationPath, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath); err == nil {
		t.Fatal("group/other-writable attestation was accepted")
	}
}

func TestRequireServerProtectionRejectsReplaceableAttestationParents(t *testing.T) {
	t.Run("group writable parent", func(t *testing.T) {
		_, dbPath, attestationPath := writeTestAttestation(t, nil)
		parent := filepath.Dir(attestationPath)
		if err := os.Chmod(parent, 0o770); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath); err == nil || !strings.Contains(err.Error(), "attestation parent directory") {
			t.Fatalf("replaceable attestation parent error = %v", err)
		}
	})

	t.Run("symlink inside writable parent", func(t *testing.T) {
		_, dbPath, attestationPath := writeTestAttestation(t, nil)
		shared := t.TempDir()
		if err := os.Chmod(shared, 0o770); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(shared, 0o700) })
		link := filepath.Join(shared, "attestation-parent")
		if err := os.Symlink(filepath.Dir(attestationPath), link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		linkedPath := filepath.Join(link, filepath.Base(attestationPath))
		if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, linkedPath); err == nil || !strings.Contains(err.Error(), "attestation parent directory") {
			t.Fatalf("symlink attestation parent error = %v", err)
		}
	})
}

func rewriteTestAttestation(t *testing.T, path string, mutate func(*attestationDocument)) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document attestationDocument
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	mutate(&document)
	content, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTestAttestation(t *testing.T, mutate func(*attestationDocument)) (string, string, string) {
	t.Helper()
	// Prefer the test runner's private temporary tree (for example macOS
	// TMPDIR). Linux normally places it below shared /tmp, so fall back to the
	// user's protected home directory to preserve the production ancestor
	// contract without making every test write to HOME unnecessarily.
	base := t.TempDir()
	if err := validateAttestationParents(filepath.Join(base, "attestation.json")); err != nil {
		base, err = os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
	}
	parent, err := os.MkdirTemp(base, ".omni-atrest-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	parent, err = filepath.Abs(parent)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "data")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	document := attestationDocument{
		Version:    1,
		Protection: ModeExternalEncryptedVolume,
		Provider:   "luks2",
		DataRoot:   root,
		KeyID:      "luks-prod-2026-01",
	}
	if mutate != nil {
		mutate(&document)
	}
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	attestationPath := filepath.Join(parent, "at-rest-attestation.json")
	if err := os.WriteFile(attestationPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "omni_money.db"), attestationPath
}
