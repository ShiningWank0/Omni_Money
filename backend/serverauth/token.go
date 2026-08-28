package serverauth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"omni_money/backend/control"
	"omni_money/backend/keyenvelope"
)

const bearerTokenBytes = 32

// GenerateBearerToken returns the one-time textual bearer and the only form
// that may be persisted. Callers must deliver the token out of band and must
// never log it.
func GenerateBearerToken() (string, []byte, error) {
	raw := make([]byte, bearerTokenBytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		clear(raw)
		return "", nil, fmt.Errorf("generate bearer token: %w", err)
	}
	defer clear(raw)
	digest, err := control.HashBearerToken(raw)
	if err != nil {
		return "", nil, err
	}
	return base64.RawURLEncoding.EncodeToString(raw), digest, nil
}

// HashEncodedBearerToken parses the canonical unpadded base64url wire form
// and returns its SHA-256 digest for control-store lookup.
func HashEncodedBearerToken(encoded string) ([]byte, error) {
	raw, err := decodeFixedSecret(encoded, bearerTokenBytes, "bearer token")
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	return control.HashBearerToken(raw)
}

// ParseRecoverySecret decodes a client-generated recovery secret. The caller
// owns the returned bytes and must clear them after wrapping or unwrapping.
func ParseRecoverySecret(encoded string) ([]byte, error) {
	return decodeFixedSecret(encoded, keyenvelope.RecoverySecretSize, "recovery secret")
}

func decodeFixedSecret(encoded string, size int, name string) ([]byte, error) {
	if encoded == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != size || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		clear(raw)
		return nil, errors.New(name + " is invalid")
	}
	return raw, nil
}
