// Package atrest validates the operator contract for externally encrypted data volumes.
package atrest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"omni_money/backend/secretfile"
)

const (
	ModeExternalEncryptedVolume = "external-encrypted-volume"
	maxAttestationBytes          = 16 * 1024
	verificationMaxAge           = 31 * 24 * time.Hour
	recoveryTestMaxAge           = 185 * 24 * time.Hour
	rotationPlanMaxHorizon       = 400 * 24 * time.Hour
	clockSkewAllowance           = 5 * time.Minute
)

var (
	keyIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)
	providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)
)

type attestationDocument struct {
	Version          int       `json:"version"`
	Protection       string    `json:"protection"`
	Provider         string    `json:"provider"`
	DataRoot         string    `json:"data_root"`
	KeyID            string    `json:"key_id"`
	VerifiedAt       time.Time `json:"verified_at"`
	RecoveryTestedAt time.Time `json:"recovery_tested_at"`
	NextRotationAt   time.Time `json:"next_rotation_at"`
}

// Status contains only non-secret metadata that is safe to log.
type Status struct {
	Provider         string
	DataRoot         string
	KeyID            string
	VerifiedAt       time.Time
	RecoveryTestedAt time.Time
	NextRotationAt   time.Time
}

// RequireServerProtection fails closed unless a fresh, integrity-protected
// attestation binds the configured database to an externally encrypted volume.
// The attestation records an operator verification; it is not cryptographic
// proof that the host storage is encrypted.
func RequireServerProtection(dbPath, mode, attestationPath string, now time.Time) (Status, error) {
	if strings.TrimSpace(mode) != ModeExternalEncryptedVolume {
		return Status{}, fmt.Errorf("DATA_AT_REST_MODE must be %q", ModeExternalEncryptedVolume)
	}
	if !filepath.IsAbs(attestationPath) {
		return Status{}, errors.New("DATA_AT_REST_ATTESTATION_FILE must be an absolute path")
	}
	content, err := secretfile.ReadIntegrityProtected(attestationPath, maxAttestationBytes)
	if err != nil {
		return Status{}, fmt.Errorf("read data-at-rest attestation: %w", err)
	}
	if err := rejectDuplicateJSONFields(content); err != nil {
		return Status{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var document attestationDocument
	if err := decoder.Decode(&document); err != nil {
		return Status{}, fmt.Errorf("decode data-at-rest attestation: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Status{}, err
	}
	if document.Version != 1 {
		return Status{}, fmt.Errorf("unsupported data-at-rest attestation version %d", document.Version)
	}
	if document.Protection != ModeExternalEncryptedVolume {
		return Status{}, fmt.Errorf("attestation protection must be %q", ModeExternalEncryptedVolume)
	}
	if !providerPattern.MatchString(document.Provider) {
		return Status{}, errors.New("attestation provider identifier is invalid")
	}
	if !keyIDPattern.MatchString(document.KeyID) {
		return Status{}, errors.New("attestation key_id must be a non-secret identifier of 8 to 128 safe characters")
	}
	if !filepath.IsAbs(document.DataRoot) {
		return Status{}, errors.New("attestation data_root must be an absolute path")
	}

	dataRoot := filepath.Clean(document.DataRoot)
	rootInfo, err := os.Lstat(dataRoot)
	if err != nil {
		return Status{}, fmt.Errorf("stat attested data_root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return Status{}, errors.New("attested data_root must be a real directory, not a symlink")
	}
	absoluteDB, err := filepath.Abs(dbPath)
	if err != nil {
		return Status{}, fmt.Errorf("resolve DB_PATH: %w", err)
	}
	if !pathContains(dataRoot, filepath.Clean(absoluteDB)) {
		return Status{}, errors.New("DB_PATH is outside the attested encrypted data_root")
	}
	absoluteAttestation, err := filepath.Abs(attestationPath)
	if err != nil {
		return Status{}, fmt.Errorf("resolve attestation path: %w", err)
	}
	if pathContains(dataRoot, filepath.Clean(absoluteAttestation)) {
		return Status{}, errors.New("attestation must be stored outside the financial data root")
	}
	if now.IsZero() {
		return Status{}, errors.New("current time is required")
	}
	now = now.UTC()
	if err := validatePastTimestamp("verified_at", document.VerifiedAt, now, verificationMaxAge); err != nil {
		return Status{}, err
	}
	if err := validatePastTimestamp("recovery_tested_at", document.RecoveryTestedAt, now, recoveryTestMaxAge); err != nil {
		return Status{}, err
	}
	if document.NextRotationAt.IsZero() || !document.NextRotationAt.After(now) {
		return Status{}, errors.New("next_rotation_at must be in the future")
	}
	if document.NextRotationAt.After(now.Add(rotationPlanMaxHorizon)) {
		return Status{}, errors.New("next_rotation_at is too far in the future")
	}
	return Status{
		Provider:         document.Provider,
		DataRoot:         dataRoot,
		KeyID:            document.KeyID,
		VerifiedAt:       document.VerifiedAt.UTC(),
		RecoveryTestedAt: document.RecoveryTestedAt.UTC(),
		NextRotationAt:   document.NextRotationAt.UTC(),
	}, nil
}

func validatePastTimestamp(name string, value, now time.Time, maxAge time.Duration) error {
	if value.IsZero() {
		return fmt.Errorf("%s is required", name)
	}
	value = value.UTC()
	if value.After(now.Add(clockSkewAllowance)) {
		return fmt.Errorf("%s is in the future", name)
	}
	if value.Before(now.Add(-maxAge)) {
		return fmt.Errorf("%s is stale", name)
	}
	return nil
}

func pathContains(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func rejectDuplicateJSONFields(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := scanJSONValue(decoder); err != nil {
		return fmt.Errorf("validate data-at-rest JSON structure: %w", err)
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("malformed JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("data-at-rest attestation contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing data-at-rest data: %w", err)
	}
	return nil
}
