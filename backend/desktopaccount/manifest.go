package desktopaccount

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"omni_money/backend/keyenvelope"
)

const (
	manifestVersion  = 1
	maxManifestBytes = 64 * 1024
	passwordKDF      = "argon2id-hkdf-sha256"
	recoveryKDF      = "hkdf-sha256"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

type manifest struct {
	Version          int                   `json:"version"`
	InstallationID   string                `json:"installation_id"`
	UserID           string                `json:"user_id"`
	VaultID          string                `json:"vault_id"`
	Role             string                `json:"role"`
	Generation       uint64                `json:"generation"`
	PasswordEnvelope *keyenvelope.Envelope `json:"password_envelope"`
	RecoveryEnvelope *keyenvelope.Envelope `json:"recovery_envelope"`
}

func (m *manifest) context() keyenvelope.Context {
	return keyenvelope.Context{UserID: m.UserID, VaultID: m.VaultID}
}

func (m *manifest) validate() error {
	if m == nil {
		return errors.New("Desktop account manifest is nil")
	}
	if m.Version != manifestVersion {
		return fmt.Errorf("unsupported Desktop account manifest version %d", m.Version)
	}
	for name, value := range map[string]string{
		"installation_id": m.InstallationID,
		"user_id":         m.UserID,
		"vault_id":        m.VaultID,
	} {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("Desktop account manifest %s is invalid", name)
		}
	}
	if m.Role != RoleAdmin {
		return errors.New("Desktop account manifest role must be admin")
	}
	if m.Generation == 0 {
		return errors.New("Desktop account manifest generation must be positive")
	}
	if err := validateEnvelope(m.PasswordEnvelope, keyenvelope.KindPassword); err != nil {
		return fmt.Errorf("validate Desktop password envelope: %w", err)
	}
	if err := validateEnvelope(m.RecoveryEnvelope, keyenvelope.KindRecovery); err != nil {
		return fmt.Errorf("validate Desktop recovery envelope: %w", err)
	}
	return nil
}

func validateEnvelope(envelope *keyenvelope.Envelope, kind keyenvelope.Kind) error {
	if envelope == nil || envelope.Version != keyenvelope.CurrentVersion || envelope.Kind != kind {
		return keyenvelope.ErrInvalidEnvelope
	}
	if len(envelope.Salt) != keyenvelope.SaltSize || len(envelope.Nonce) != 12 || len(envelope.Ciphertext) != keyenvelope.DEKSize+16 {
		return keyenvelope.ErrInvalidEnvelope
	}
	switch kind {
	case keyenvelope.KindPassword:
		if envelope.KDF != passwordKDF || envelope.Profile != keyenvelope.DefaultProfile() || len(envelope.Verifier) != keyenvelope.VerifierSize {
			return keyenvelope.ErrInvalidEnvelope
		}
	case keyenvelope.KindRecovery:
		if envelope.KDF != recoveryKDF || envelope.Profile != (keyenvelope.Argon2idProfile{}) || len(envelope.Verifier) != 0 {
			return keyenvelope.ErrInvalidEnvelope
		}
	default:
		return keyenvelope.ErrInvalidEnvelope
	}
	return nil
}

func (m *manifest) withEnvelopes(passwordEnvelope, recoveryEnvelope *keyenvelope.Envelope) (*manifest, error) {
	if m == nil || m.Generation == ^uint64(0) {
		return nil, errors.New("Desktop account manifest generation cannot be advanced")
	}
	replacement := &manifest{
		Version:          m.Version,
		InstallationID:   m.InstallationID,
		UserID:           m.UserID,
		VaultID:          m.VaultID,
		Role:             m.Role,
		Generation:       m.Generation + 1,
		PasswordEnvelope: cloneEnvelope(passwordEnvelope),
		RecoveryEnvelope: cloneEnvelope(recoveryEnvelope),
	}
	if err := replacement.validate(); err != nil {
		return nil, err
	}
	return replacement, nil
}

func cloneEnvelope(source *keyenvelope.Envelope) *keyenvelope.Envelope {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.Salt = append([]byte(nil), source.Salt...)
	cloned.Nonce = append([]byte(nil), source.Nonce...)
	cloned.Ciphertext = append([]byte(nil), source.Ciphertext...)
	cloned.Verifier = append([]byte(nil), source.Verifier...)
	return &cloned
}

func readManifest(path string) (*manifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("Desktop account manifest must be a regular file, not a symbolic link")
	}
	if hasInsecurePermissions(info.Mode()) {
		return nil, fmt.Errorf("Desktop account manifest must be owner-only: %04o", info.Mode().Perm())
	}
	if info.Size() <= 0 || info.Size() > maxManifestBytes {
		return nil, fmt.Errorf("Desktop account manifest size must be between 1 and %d bytes", maxManifestBytes)
	}
	file, err := openRegularNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("open Desktop account manifest: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened Desktop account manifest: %w", err)
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, errors.New("Desktop account manifest changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Desktop account manifest: %w", err)
	}
	defer clear(content)
	if len(content) == 0 || len(content) > maxManifestBytes {
		return nil, fmt.Errorf("Desktop account manifest size must be between 1 and %d bytes", maxManifestBytes)
	}
	if err := rejectDuplicateJSONFields(content); err != nil {
		return nil, fmt.Errorf("decode Desktop account manifest: %w", err)
	}
	if err := requireExactManifestFields(content); err != nil {
		return nil, fmt.Errorf("decode Desktop account manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var document manifest
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode Desktop account manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode Desktop account manifest: %w", err)
	}
	if err := document.validate(); err != nil {
		return nil, err
	}
	return &document, nil
}

// encoding/json matches struct fields case-insensitively. Security metadata is
// intentionally stricter: every field name and every required field must be
// present exactly as specified by manifest version 1.
func requireExactManifestFields(content []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(content, &root); err != nil {
		return err
	}
	if err := requireExactObject(root, []string{
		"version",
		"installation_id",
		"user_id",
		"vault_id",
		"role",
		"generation",
		"password_envelope",
		"recovery_envelope",
	}); err != nil {
		return fmt.Errorf("manifest object: %w", err)
	}
	if err := requireExactEnvelopeFields(root["password_envelope"], true); err != nil {
		return fmt.Errorf("password_envelope: %w", err)
	}
	if err := requireExactEnvelopeFields(root["recovery_envelope"], false); err != nil {
		return fmt.Errorf("recovery_envelope: %w", err)
	}
	return nil
}

func requireExactEnvelopeFields(content json.RawMessage, password bool) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil {
		return err
	}
	required := []string{"version", "kind", "kdf", "argon2id_profile", "salt", "nonce", "ciphertext"}
	if password {
		required = append(required, "password_verifier")
	}
	if err := requireExactObject(object, required); err != nil {
		return err
	}
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(object["argon2id_profile"], &profile); err != nil {
		return err
	}
	return requireExactObject(profile, []string{"memory_kib", "iterations", "parallelism"})
}

func requireExactObject(object map[string]json.RawMessage, required []string) error {
	if object == nil {
		return errors.New("must be a JSON object")
	}
	expected := make(map[string]struct{}, len(required))
	for _, name := range required {
		expected[name] = struct{}{}
		if _, ok := object[name]; !ok {
			return fmt.Errorf("required field %q is missing", name)
		}
	}
	for name := range object {
		if _, ok := expected[name]; !ok {
			return fmt.Errorf("field %q is not allowed", name)
		}
	}
	return nil
}

func writeManifestAtomic(path string, document *manifest) error {
	if err := document.validate(); err != nil {
		return err
	}
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Desktop account manifest: %w", err)
	}
	content = append(content, '\n')
	defer clear(content)
	if len(content) > maxManifestBytes {
		return errors.New("Desktop account manifest exceeds its size limit")
	}
	if existing, err := os.Lstat(path); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return errors.New("Desktop account manifest target must be a regular file")
		}
		if hasInsecurePermissions(existing.Mode()) {
			return fmt.Errorf("Desktop account manifest must be owner-only: %04o", existing.Mode().Perm())
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Desktop account manifest target: %w", err)
	}

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".desktop-account.tmp-")
	if err != nil {
		return fmt.Errorf("create Desktop account manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil { // #nosec G302 -- manifest must be owner-only.
		return fmt.Errorf("secure Desktop account manifest temporary file: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write Desktop account manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync Desktop account manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Desktop account manifest: %w", err)
	}
	if err := replaceFileAtomic(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Desktop account manifest: %w", err)
	}
	committed = true
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync Desktop account manifest directory: %w", err)
	}
	return nil
}

func rejectDuplicateJSONFields(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object field name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", name)
			}
			seen[name] = struct{}{}
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
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected data after JSON document")
		}
		return err
	}
	return nil
}
