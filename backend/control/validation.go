package control

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"omni_money/backend/keyenvelope"
)

const tokenHashSize = 32

// HashBearerToken converts a high-entropy invitation or reset bearer token to
// the only form that may be persisted in the control database. Tokens should
// contain at least 32 random bytes before any textual encoding.
func HashBearerToken(token []byte) ([]byte, error) {
	if len(token) < 32 || len(token) > 4096 {
		return nil, errors.New("bearer token must contain between 32 and 4096 bytes")
	}
	digest := sha256.Sum256(token)
	result := make([]byte, len(digest))
	copy(result, digest[:])
	return result, nil
}

func GenerateID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate control-plane id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func normalizeID(id string) (string, error) {
	if strings.TrimSpace(id) != id {
		return "", errors.New("id must not contain surrounding whitespace")
	}
	if len(id) < 16 || len(id) > 128 {
		return "", errors.New("id must contain between 16 and 128 characters")
	}
	for _, character := range id {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			continue
		}
		return "", errors.New("id contains an invalid character")
	}
	return id, nil
}

func idOrGenerate(id string) (string, error) {
	if id == "" {
		return GenerateID()
	}
	return normalizeID(id)
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(email) < 3 || len(email) > 254 || strings.ContainsAny(email, "\r\n\x00") {
		return "", errors.New("email address is invalid")
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || strings.Count(email, "@") != 1 {
		return "", errors.New("email address is invalid")
	}
	return email, nil
}

func normalizeDisplayName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if len(name) == 0 || len(name) > 200 {
		return "", errors.New("display name must contain between 1 and 200 bytes")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", errors.New("display name contains a control character")
		}
	}
	return name, nil
}

func validateRole(role Role) error {
	if role != RoleAdmin && role != RoleUser {
		return errors.New("role is invalid")
	}
	return nil
}

func normalizeVaultID(id string) (string, error) {
	id, err := normalizeID(id)
	if err != nil {
		return "", fmt.Errorf("vault id: %w", err)
	}
	return id, nil
}

func validateOperationTime(value time.Time) (time.Time, error) {
	if value.IsZero() || value.Year() < 2000 || value.Year() > 9999 {
		return time.Time{}, errors.New("operation time is invalid")
	}
	return time.UnixMilli(value.UnixMilli()).UTC(), nil
}

func validateExpiry(now, expiresAt time.Time) (time.Time, error) {
	expiresAt, err := validateOperationTime(expiresAt)
	if err != nil {
		return time.Time{}, errors.New("expiry time is invalid")
	}
	if !expiresAt.After(now) {
		return time.Time{}, errors.New("expiry time must be in the future")
	}
	return expiresAt, nil
}

func validateTokenHash(value []byte) error {
	if len(value) != tokenHashSize {
		return fmt.Errorf("token hash must be exactly %d bytes", tokenHashSize)
	}
	return nil
}

func validateOpaque(value []byte, name string, minimum, maximum int) error {
	if len(value) < minimum || len(value) > maximum {
		return fmt.Errorf("%s must contain between %d and %d bytes", name, minimum, maximum)
	}
	return nil
}

const (
	passwordEnvelopeKDF = "argon2id-hkdf-sha256"
	recoveryEnvelopeKDF = "hkdf-sha256"
)

func validatePasswordCredential(value PasswordCredentialInput) error {
	return validateKeyEnvelope(&value.Envelope, keyenvelope.KindPassword)
}

func validateRecoveryEnvelope(value RecoveryEnvelopeInput) error {
	return validateKeyEnvelope(&value.Envelope, keyenvelope.KindRecovery)
}

func validateKeyEnvelope(envelope *keyenvelope.Envelope, kind keyenvelope.Kind) error {
	if envelope == nil || envelope.Version != keyenvelope.CurrentVersion || envelope.Kind != kind {
		return errors.New("key envelope version or kind is invalid")
	}
	if err := validateOpaque(envelope.Salt, "key envelope salt", keyenvelope.SaltSize, keyenvelope.SaltSize); err != nil {
		return err
	}
	if err := validateOpaque(envelope.Nonce, "key envelope nonce", 12, 12); err != nil {
		return err
	}
	if err := validateOpaque(envelope.Ciphertext, "key envelope ciphertext", keyenvelope.DEKSize+16, keyenvelope.DEKSize+16); err != nil {
		return err
	}
	switch kind {
	case keyenvelope.KindPassword:
		if envelope.KDF != passwordEnvelopeKDF || envelope.Profile != keyenvelope.DefaultProfile() {
			return errors.New("password key envelope KDF profile is invalid")
		}
		if err := validateOpaque(envelope.Verifier, "password verifier", keyenvelope.VerifierSize, keyenvelope.VerifierSize); err != nil {
			return err
		}
	case keyenvelope.KindRecovery:
		if envelope.KDF != recoveryEnvelopeKDF || envelope.Profile != (keyenvelope.Argon2idProfile{}) || len(envelope.Verifier) != 0 {
			return errors.New("recovery key envelope KDF profile is invalid")
		}
	default:
		return errors.New("key envelope kind is invalid")
	}
	return nil
}

func encodeKeyEnvelope(envelope keyenvelope.Envelope, kind keyenvelope.Kind) (string, error) {
	if err := validateKeyEnvelope(&envelope, kind); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(&envelope)
	if err != nil {
		return "", fmt.Errorf("encode key envelope: %w", err)
	}
	if len(encoded) > 8192 {
		return "", errors.New("encoded key envelope is too large")
	}
	return string(encoded), nil
}

func decodeKeyEnvelope(encoded string, kind keyenvelope.Kind) (keyenvelope.Envelope, error) {
	if len(encoded) < 2 || len(encoded) > 8192 {
		return keyenvelope.Envelope{}, errors.New("stored key envelope has invalid size")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(encoded))
	decoder.DisallowUnknownFields()
	var envelope keyenvelope.Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return keyenvelope.Envelope{}, fmt.Errorf("decode stored key envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return keyenvelope.Envelope{}, errors.New("stored key envelope has trailing data")
	}
	if err := validateKeyEnvelope(&envelope, kind); err != nil {
		return keyenvelope.Envelope{}, fmt.Errorf("validate stored key envelope: %w", err)
	}
	return envelope, nil
}

func cloneBytes(value []byte) []byte {
	result := make([]byte, len(value))
	copy(result, value)
	return result
}
