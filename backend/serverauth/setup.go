// Package serverauth coordinates server account authentication without ever
// putting vault data or a vault DEK in the administrative control database.
package serverauth

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	if err := validateSetupTokenPath(path); err != nil {
		return nil, err
	}
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

// validateSetupTokenPath makes the setup token an integrity boundary as well
// as a confidential file. File mode alone is insufficient: a different UID
// that owns a parent directory can replace an otherwise read-only leaf by
// rename. Both the configured path and the fully resolved parent path are
// checked before secretfile performs its descriptor-based, O_NOFOLLOW read.
func validateSetupTokenPath(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("initial administrator setup token file must use an absolute path")
	}
	cleaned := filepath.Clean(path)
	if cleaned != path {
		// Cleaning a path across a symbolic-link component can change filesystem
		// resolution semantics. Reject ambiguous spellings instead of validating
		// one path and opening another.
		return errors.New("initial administrator setup token file path must be clean")
	}

	parent := filepath.Dir(cleaned)
	if err := validateSetupTokenDirectoryChain(parent, true); err != nil {
		return err
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve initial administrator setup token parent: %w", err)
	}
	if err := validateSetupTokenDirectoryChain(filepath.Clean(resolvedParent), false); err != nil {
		return err
	}

	info, err := os.Lstat(cleaned)
	if err != nil {
		return fmt.Errorf("inspect initial administrator setup token file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("initial administrator setup token file must be a regular file, not a symbolic link")
	}
	if !trustedSetupTokenOwner(info) {
		return errors.New("initial administrator setup token file must be owned by root or the server user")
	}
	return nil
}

func validateSetupTokenDirectoryChain(current string, allowProtectedSymlinks bool) error {
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect initial administrator setup token parent: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !allowProtectedSymlinks {
				return errors.New("resolved initial administrator setup token path must contain only real directories")
			}
		} else if !info.IsDir() {
			return errors.New("initial administrator setup token parent path must contain only real directories")
		} else if !trustedSetupTokenOwner(info) {
			return errors.New("initial administrator setup token parent must be owned by root or the server user")
		} else if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("initial administrator setup token parent must not be writable by group or other users: %04o", info.Mode().Perm())
		}

		next := filepath.Dir(current)
		if next == current {
			return nil
		}
		current = next
	}
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
