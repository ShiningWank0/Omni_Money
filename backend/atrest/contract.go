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
	maxAttestationBytes         = 16 * 1024
	verificationMaxAge          = 31 * 24 * time.Hour
	recoveryTestMaxAge          = 185 * 24 * time.Hour
	rotationPlanMaxHorizon      = 400 * 24 * time.Hour
	clockSkewAllowance          = 5 * time.Minute
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
	if err := validateAttestationParents(attestationPath); err != nil {
		return Status{}, err
	}
	attestationInfo, err := os.Lstat(attestationPath)
	if err != nil {
		return Status{}, fmt.Errorf("inspect data-at-rest attestation owner: %w", err)
	}
	if !trustedAttestationOwner(attestationInfo) {
		return Status{}, errors.New("data-at-rest attestation must be owned by root or the server user")
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
	if rootInfo.Mode().Perm()&0o022 != 0 {
		return Status{}, fmt.Errorf("attested data_root must not be writable by group or other users: %04o", rootInfo.Mode().Perm())
	}
	absoluteDB, err := filepath.Abs(dbPath)
	if err != nil {
		return Status{}, fmt.Errorf("resolve DB_PATH: %w", err)
	}
	absoluteDB = filepath.Clean(absoluteDB)
	if err := validateDataPath(dataRoot, absoluteDB); err != nil {
		return Status{}, err
	}
	absoluteAttestation, err := filepath.Abs(attestationPath)
	if err != nil {
		return Status{}, fmt.Errorf("resolve attestation path: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return Status{}, fmt.Errorf("resolve attested data_root: %w", err)
	}
	resolvedAttestation, err := filepath.EvalSymlinks(filepath.Clean(absoluteAttestation))
	if err != nil {
		return Status{}, fmt.Errorf("resolve attestation path: %w", err)
	}
	if pathContains(filepath.Clean(resolvedRoot), filepath.Clean(resolvedAttestation)) {
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

// validateAttestationParents prevents a different OS user from replacing an
// otherwise read-only attestation by renaming it through a shared writable
// directory. Both the configured lexical chain and the resolved chain are
// checked. This permits protected system aliases such as macOS /var while
// rejecting aliases placed in, or targeting, a shared writable directory. A
// process with the same UID remains inside the operator trust boundary.
func validateAttestationParents(path string) error {
	parent := filepath.Clean(filepath.Dir(path))
	if err := validateAttestationDirectoryChain(parent, true); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve attestation parent directory: %w", err)
	}
	return validateAttestationDirectoryChain(filepath.Clean(resolved), false)
}

func validateAttestationDirectoryChain(current string, allowProtectedSymlinks bool) error {
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect attestation parent directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !allowProtectedSymlinks {
				return errors.New("resolved attestation parent path must contain only real directories")
			}
		} else if !info.IsDir() {
			return errors.New("attestation parent path must contain only real directories")
		} else if !trustedAttestationOwner(info) {
			return errors.New("attestation parent directory must be owned by root or the server user")
		} else if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("attestation parent directory must not be writable by group or other users: %04o", info.Mode().Perm())
		}
		next := filepath.Dir(current)
		if next == current {
			return nil
		}
		current = next
	}
}

// validateDataPath verifies both lexical containment and every existing path
// component below dataRoot. A path such as dataRoot/link/database.db must not
// pass merely because its spelling is contained when link redirects to another
// volume. Missing components are allowed so a fresh deployment can create its
// database after this preflight; the deepest existing parent is still checked.
func validateDataPath(dataRoot, candidate string) error {
	relative, err := filepath.Rel(dataRoot, candidate)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("DB_PATH is outside the attested encrypted data_root")
	}
	if relative == "." {
		return errors.New("DB_PATH must name a file below the attested encrypted data_root")
	}

	components := strings.Split(relative, string(filepath.Separator))
	current := dataRoot
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			return errors.New("DB_PATH contains an invalid path component")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			// filepath.Join cannot escape after the lexical check above. Any
			// remaining components will be created below this checked parent.
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect DB_PATH component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("DB_PATH must not contain symbolic links below the attested data_root")
		}
		isLeaf := index == len(components)-1
		if !isLeaf && !info.IsDir() {
			return errors.New("DB_PATH parent component must be a directory")
		}
		if isLeaf && !info.Mode().IsRegular() {
			return errors.New("existing DB_PATH must be a regular file")
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("DB_PATH component must not be writable by group or other users: %04o", info.Mode().Perm())
		}
	}
	return nil
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
