// Package serverauth coordinates server account authentication without ever
// putting vault data or a vault DEK in the administrative control database.
package serverauth

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"

	"omni_money/backend/secretfile"
)

const maxSetupTokenFileBytes int64 = 4096

var (
	ErrSetupUnauthorized = errors.New("initial administrator setup is not authorized")
	ErrAlreadySetup      = errors.New("initial administrator setup is already complete")
)

// SetupAuthorizer keeps only a digest of the deployment-local bootstrap
// secret. The control store's atomic zero-user check is the final one-time
// gate; a token file left mounted after setup cannot create a second admin.
type SetupAuthorizer struct {
	digest [sha256.Size]byte
}

// LoadSetupAuthorizer reads an owner-only setup token file. A single trailing
// line ending is accepted for ordinary secret-file tooling; other surrounding
// whitespace is part of the token and therefore rejected.
func LoadSetupAuthorizer(path string) (*SetupAuthorizer, error) {
	content, err := secretfile.ReadConfidential(path, maxSetupTokenFileBytes)
	if err != nil {
		return nil, fmt.Errorf("read initial administrator setup token: %w", err)
	}
	defer clear(content)
	token := trimSingleLineEnding(content)
	if err := validateSetupToken(token); err != nil {
		return nil, err
	}
	return &SetupAuthorizer{digest: sha256.Sum256(token)}, nil
}

func trimSingleLineEnding(value []byte) []byte {
	if bytes.HasSuffix(value, []byte("\r\n")) {
		return value[:len(value)-2]
	}
	if bytes.HasSuffix(value, []byte("\n")) {
		return value[:len(value)-1]
	}
	return value
}

func validateSetupToken(token []byte) error {
	if len(token) < 32 || len(token) > 512 {
		return errors.New("initial administrator setup token must contain between 32 and 512 bytes")
	}
	for _, character := range token {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '-' && character != '_' {
			return errors.New("initial administrator setup token must be an unpadded base64url-style string")
		}
	}
	return nil
}

// Authorize performs a length-independent digest comparison. The candidate is
// caller-owned and should be cleared by the HTTP boundary after this call.
func (a *SetupAuthorizer) Authorize(candidate []byte) bool {
	if a == nil {
		return false
	}
	digest := sha256.Sum256(candidate)
	return subtle.ConstantTimeCompare(digest[:], a.digest[:]) == 1
}

// Destroy best-effort clears the in-memory digest during shutdown.
func (a *SetupAuthorizer) Destroy() {
	if a == nil {
		return
	}
	clear(a.digest[:])
}
