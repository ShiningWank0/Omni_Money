package atrest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequireServerProtectionAcceptsFreshExternalVolumeAttestation(t *testing.T) {
	now := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	root, dbPath, attestationPath := writeTestAttestation(t, now, nil)
	status, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if status.DataRoot != root || status.Provider != "luks2" || status.KeyID != "luks-prod-2026-01" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestRequireServerProtectionRejectsMissingOrStaleContract(t *testing.T) {
	now := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mode   string
		mutate func(*attestationDocument)
	}{
		{name: "missing mode", mode: ""},
		{name: "wrong mode", mode: "plaintext"},
		{name: "stale verification", mode: ModeExternalEncryptedVolume, mutate: func(document *attestationDocument) {
			document.VerifiedAt = now.Add(-32 * 24 * time.Hour)
		}},
		{name: "stale recovery test", mode: ModeExternalEncryptedVolume, mutate: func(document *attestationDocument) {
			document.RecoveryTestedAt = now.Add(-186 * 24 * time.Hour)
		}},
		{name: "expired rotation plan", mode: ModeExternalEncryptedVolume, mutate: func(document *attestationDocument) {
			document.NextRotationAt = now
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
			_, dbPath, attestationPath := writeTestAttestation(t, now, test.mutate)
			if _, err := RequireServerProtection(dbPath, test.mode, attestationPath, now); err == nil {
				t.Fatal("invalid at-rest contract was accepted")
			}
		})
	}
}

func TestRequireServerProtectionRejectsDuplicateFieldsAndInRootAttestation(t *testing.T) {
	now := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	root, dbPath, attestationPath := writeTestAttestation(t, now, nil)
	content, err := os.ReadFile(attestationPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(string(content), `"version":1`, `"version":1,"version":1`, 1)
	if err := os.WriteFile(attestationPath, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, attestationPath, now); err == nil {
		t.Fatal("duplicate JSON field was accepted")
	}

	_, _, outsidePath := writeTestAttestation(t, now, nil)
	inRootPath := filepath.Join(root, "attestation.json")
	inRootContent, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inRootPath, inRootContent, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RequireServerProtection(dbPath, ModeExternalEncryptedVolume, inRootPath, now); err == nil {
		t.Fatal("attestation inside financial data root was accepted")
	}
}

func writeTestAttestation(t *testing.T, now time.Time, mutate func(*attestationDocument)) (string, string, string) {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "data")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	document := attestationDocument{
		Version:          1,
		Protection:       ModeExternalEncryptedVolume,
		Provider:         "luks2",
		DataRoot:         root,
		KeyID:            "luks-prod-2026-01",
		VerifiedAt:       now.Add(-time.Hour),
		RecoveryTestedAt: now.Add(-24 * time.Hour),
		NextRotationAt:   now.Add(180 * 24 * time.Hour),
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
