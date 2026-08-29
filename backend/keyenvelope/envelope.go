// Package keyenvelope wraps per-vault data-encryption keys without giving the
// server administrator a key that can decrypt a user's vault.
//
// Passwords and recovery secrets passed to this package remain owned by the
// caller and should be cleared as soon as they are no longer needed. Returned
// plaintext DEKs must likewise be cleared by the caller after use.
//
// Intermediate byte slices are cleared on a best-effort basis. Go's cipher and
// KDF implementations may retain internal key schedules until garbage
// collection, so this package does not claim to resist compromise of a live,
// unlocked process.
package keyenvelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/hkdf"
)

const (
	// CurrentVersion identifies the only envelope format currently accepted.
	CurrentVersion uint8 = 1

	DEKSize             = 32
	RecoverySecretSize  = 32
	PasskeySecretSize   = 32
	SaltSize            = 32
	VerifierSize        = sha256.Size
	Argon2idMemoryKiB   = 64 * 1024
	Argon2idIterations  = 3
	Argon2idParallelism = 2

	maxPasswordSize  = 1024 * 1024
	maxContextIDSize = 4096

	passwordKDF = "argon2id-hkdf-sha256" // #nosec G101 -- public algorithm identifier, not a credential.
	recoveryKDF = "hkdf-sha256"
	passkeyKDF  = "hkdf-sha256" // #nosec G101 -- public algorithm identifier, not a credential.
)

// Kind separates envelopes protected by a password from those protected by a
// high-entropy recovery secret.
type Kind string

const (
	KindPassword Kind = "password"
	KindRecovery Kind = "recovery"
	KindPasskey  Kind = "passkey-prf"
)

var (
	ErrAuthentication        = errors.New("key envelope authentication failed")
	ErrInvalidContext        = errors.New("invalid key envelope context")
	ErrInvalidDEK            = errors.New("data-encryption key must be exactly 32 bytes")
	ErrInvalidEnvelope       = errors.New("invalid key envelope")
	ErrInvalidPassword       = errors.New("password must not be empty or excessively large")
	ErrInvalidRecoverySecret = errors.New("recovery secret must be exactly 32 bytes")
	ErrInvalidPasskeySecret  = errors.New("passkey PRF secret must be exactly 32 bytes")
)

// Argon2idProfile is persisted with password envelopes. Version 1 deliberately
// accepts only DefaultProfile(). Validating it before derivation prevents
// a corrupted or attacker-controlled envelope from causing an unbounded memory
// or CPU allocation.
type Argon2idProfile struct {
	MemoryKiB   uint32 `json:"memory_kib"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint8  `json:"parallelism"`
}

// DefaultProfile returns the immutable Argon2id parameters for version 1.
func DefaultProfile() Argon2idProfile {
	return Argon2idProfile{
		MemoryKiB:   Argon2idMemoryKiB,
		Iterations:  Argon2idIterations,
		Parallelism: Argon2idParallelism,
	}
}

// Context identifies the sole user and vault for which an envelope is valid.
// Both identifiers are authenticated, not encrypted.
type Context struct {
	UserID  string
	VaultID string
}

// Envelope contains only encrypted key material and public KDF metadata. Byte
// slices use base64 when marshalled by encoding/json.
type Envelope struct {
	Version    uint8           `json:"version"`
	Kind       Kind            `json:"kind"`
	KDF        string          `json:"kdf"`
	Profile    Argon2idProfile `json:"argon2id_profile"`
	Salt       []byte          `json:"salt"`
	Nonce      []byte          `json:"nonce"`
	Ciphertext []byte          `json:"ciphertext"`
	Verifier   []byte          `json:"password_verifier,omitempty"`
}

// GenerateDEK returns a fresh AES-256/SQLCipher data-encryption key.
func GenerateDEK() ([]byte, error) {
	return randomBytes(DEKSize)
}

// GenerateRecoverySecret returns a fresh 256-bit recovery secret. It should be
// shown or exported once and must never be stored beside its envelope.
func GenerateRecoverySecret() ([]byte, error) {
	return randomBytes(RecoverySecretSize)
}

// WrapWithPassword encrypts dek with a key derived from password. The password
// is not retained or modified.
func WrapWithPassword(dek, password []byte, context Context) (*Envelope, error) {
	if err := validateDEK(dek); err != nil {
		return nil, err
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if err := validateContext(context); err != nil {
		return nil, err
	}

	salt, err := randomBytes(SaltSize)
	if err != nil {
		return nil, err
	}
	profile := DefaultProfile()
	root := argon2.IDKey(password, salt, profile.Iterations,
		profile.MemoryKiB, profile.Parallelism, DEKSize)
	defer clear(root)

	wrappingKey, err := deriveKey(root, salt, hkdfInfo(KindPassword, "aead"))
	if err != nil {
		return nil, err
	}
	defer clear(wrappingKey)
	verifierKey, err := deriveKey(root, salt, hkdfInfo(KindPassword, "verifier"))
	if err != nil {
		return nil, err
	}
	defer clear(verifierKey)

	aad := authenticatedData(context, KindPassword, CurrentVersion)
	defer clear(aad)
	nonce, ciphertext, err := seal(wrappingKey, dek, aad)
	if err != nil {
		return nil, err
	}

	return &Envelope{
		Version:    CurrentVersion,
		Kind:       KindPassword,
		KDF:        passwordKDF,
		Profile:    profile,
		Salt:       salt,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		Verifier:   passwordVerifier(verifierKey, aad),
	}, nil
}

// UnwrapWithPassword authenticates the password, context, and envelope before
// returning the plaintext DEK. All authentication failures use one error.
func UnwrapWithPassword(envelope *Envelope, password []byte, context Context) ([]byte, error) {
	if err := validatePasswordEnvelope(envelope); err != nil {
		return nil, err
	}
	if err := validatePassword(password); err != nil {
		return nil, ErrAuthentication
	}
	if err := validateContext(context); err != nil {
		return nil, err
	}

	root := argon2.IDKey(password, envelope.Salt, envelope.Profile.Iterations,
		envelope.Profile.MemoryKiB, envelope.Profile.Parallelism, DEKSize)
	defer clear(root)
	wrappingKey, err := deriveKey(root, envelope.Salt, hkdfInfo(KindPassword, "aead"))
	if err != nil {
		return nil, err
	}
	defer clear(wrappingKey)
	verifierKey, err := deriveKey(root, envelope.Salt, hkdfInfo(KindPassword, "verifier"))
	if err != nil {
		return nil, err
	}
	defer clear(verifierKey)

	aad := authenticatedData(context, envelope.Kind, envelope.Version)
	defer clear(aad)
	expectedVerifier := passwordVerifier(verifierKey, aad)
	verifierOK := subtle.ConstantTimeCompare(expectedVerifier, envelope.Verifier)
	clear(expectedVerifier)

	dek, openErr := open(wrappingKey, envelope.Nonce, envelope.Ciphertext, aad)
	if verifierOK != 1 || openErr != nil {
		clear(dek)
		return nil, ErrAuthentication
	}
	if len(dek) != DEKSize {
		clear(dek)
		return nil, ErrAuthentication
	}
	return dek, nil
}

// VerifyPassword checks the password verifier using constant-time comparison.
// It does not decrypt or return the wrapped DEK.
func VerifyPassword(envelope *Envelope, password []byte, context Context) (bool, error) {
	if err := validatePasswordEnvelope(envelope); err != nil {
		return false, err
	}
	if err := validatePassword(password); err != nil {
		return false, nil
	}
	if err := validateContext(context); err != nil {
		return false, err
	}

	root := argon2.IDKey(password, envelope.Salt, envelope.Profile.Iterations,
		envelope.Profile.MemoryKiB, envelope.Profile.Parallelism, DEKSize)
	defer clear(root)
	verifierKey, err := deriveKey(root, envelope.Salt, hkdfInfo(KindPassword, "verifier"))
	if err != nil {
		return false, err
	}
	defer clear(verifierKey)
	aad := authenticatedData(context, envelope.Kind, envelope.Version)
	defer clear(aad)
	expected := passwordVerifier(verifierKey, aad)
	defer clear(expected)
	return subtle.ConstantTimeCompare(expected, envelope.Verifier) == 1, nil
}

// WrapWithRecovery encrypts the same DEK with an independent 256-bit recovery
// secret. HKDF provides purpose separation from all password-derived keys.
func WrapWithRecovery(dek, recoverySecret []byte, context Context) (*Envelope, error) {
	if err := validateDEK(dek); err != nil {
		return nil, err
	}
	if err := validateRecoverySecret(recoverySecret); err != nil {
		return nil, err
	}
	if err := validateContext(context); err != nil {
		return nil, err
	}

	salt, err := randomBytes(SaltSize)
	if err != nil {
		return nil, err
	}
	wrappingKey, err := deriveKey(recoverySecret, salt, hkdfInfo(KindRecovery, "aead"))
	if err != nil {
		return nil, err
	}
	defer clear(wrappingKey)
	aad := authenticatedData(context, KindRecovery, CurrentVersion)
	defer clear(aad)
	nonce, ciphertext, err := seal(wrappingKey, dek, aad)
	if err != nil {
		return nil, err
	}
	return &Envelope{
		Version:    CurrentVersion,
		Kind:       KindRecovery,
		KDF:        recoveryKDF,
		Salt:       salt,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}, nil
}

// UnwrapWithRecovery authenticates and decrypts an envelope using the
// independent recovery secret.
func UnwrapWithRecovery(envelope *Envelope, recoverySecret []byte, context Context) ([]byte, error) {
	if err := validateRecoveryEnvelope(envelope); err != nil {
		return nil, err
	}
	if err := validateRecoverySecret(recoverySecret); err != nil {
		return nil, ErrAuthentication
	}
	if err := validateContext(context); err != nil {
		return nil, err
	}
	wrappingKey, err := deriveKey(recoverySecret, envelope.Salt, hkdfInfo(KindRecovery, "aead"))
	if err != nil {
		return nil, err
	}
	defer clear(wrappingKey)
	aad := authenticatedData(context, envelope.Kind, envelope.Version)
	defer clear(aad)
	dek, err := open(wrappingKey, envelope.Nonce, envelope.Ciphertext, aad)
	if err != nil || len(dek) != DEKSize {
		clear(dek)
		return nil, ErrAuthentication
	}
	return dek, nil
}

// WrapWithPasskey encrypts dek with the 256-bit output of the WebAuthn PRF
// extension. The PRF output is credential-bound high-entropy key material; it
// is never persisted and is purpose-separated from password and recovery
// envelopes by both HKDF info and authenticated envelope metadata.
func WrapWithPasskey(dek, prfSecret []byte, context Context) (*Envelope, error) {
	if err := validateDEK(dek); err != nil {
		return nil, err
	}
	if err := validatePasskeySecret(prfSecret); err != nil {
		return nil, err
	}
	if err := validateContext(context); err != nil {
		return nil, err
	}
	salt, err := randomBytes(SaltSize)
	if err != nil {
		return nil, err
	}
	wrappingKey, err := deriveKey(prfSecret, salt, hkdfInfo(KindPasskey, "aead"))
	if err != nil {
		return nil, err
	}
	defer clear(wrappingKey)
	aad := authenticatedData(context, KindPasskey, CurrentVersion)
	defer clear(aad)
	nonce, ciphertext, err := seal(wrappingKey, dek, aad)
	if err != nil {
		return nil, err
	}
	return &Envelope{
		Version: CurrentVersion, Kind: KindPasskey, KDF: passkeyKDF,
		Salt: salt, Nonce: nonce, Ciphertext: ciphertext,
	}, nil
}

// UnwrapWithPasskey authenticates and decrypts a passkey envelope with a
// freshly evaluated WebAuthn PRF result.
func UnwrapWithPasskey(envelope *Envelope, prfSecret []byte, context Context) ([]byte, error) {
	if err := validatePasskeyEnvelope(envelope); err != nil {
		return nil, err
	}
	if err := validatePasskeySecret(prfSecret); err != nil {
		return nil, ErrAuthentication
	}
	if err := validateContext(context); err != nil {
		return nil, err
	}
	wrappingKey, err := deriveKey(prfSecret, envelope.Salt, hkdfInfo(KindPasskey, "aead"))
	if err != nil {
		return nil, err
	}
	defer clear(wrappingKey)
	aad := authenticatedData(context, KindPasskey, envelope.Version)
	defer clear(aad)
	dek, err := open(wrappingKey, envelope.Nonce, envelope.Ciphertext, aad)
	if err != nil || len(dek) != DEKSize {
		clear(dek)
		return nil, ErrAuthentication
	}
	return dek, nil
}

func validatePasswordEnvelope(envelope *Envelope) error {
	if err := validateCommonEnvelope(envelope, KindPassword, passwordKDF); err != nil {
		return err
	}
	if envelope.Profile != DefaultProfile() {
		return fmt.Errorf("%w: unsupported Argon2id profile", ErrInvalidEnvelope)
	}
	if len(envelope.Verifier) != VerifierSize {
		return fmt.Errorf("%w: invalid password verifier", ErrInvalidEnvelope)
	}
	return nil
}

func validateRecoveryEnvelope(envelope *Envelope) error {
	if err := validateCommonEnvelope(envelope, KindRecovery, recoveryKDF); err != nil {
		return err
	}
	if envelope.Profile != (Argon2idProfile{}) || len(envelope.Verifier) != 0 {
		return fmt.Errorf("%w: recovery envelope contains password metadata", ErrInvalidEnvelope)
	}
	return nil
}

func validatePasskeyEnvelope(envelope *Envelope) error {
	if err := validateCommonEnvelope(envelope, KindPasskey, passkeyKDF); err != nil {
		return err
	}
	if envelope.Profile != (Argon2idProfile{}) || len(envelope.Verifier) != 0 {
		return fmt.Errorf("%w: passkey envelope contains password metadata", ErrInvalidEnvelope)
	}
	return nil
}

func validateCommonEnvelope(envelope *Envelope, kind Kind, kdf string) error {
	if envelope == nil {
		return fmt.Errorf("%w: envelope is nil", ErrInvalidEnvelope)
	}
	if envelope.Version != CurrentVersion || envelope.Kind != kind || envelope.KDF != kdf {
		return fmt.Errorf("%w: unsupported metadata", ErrInvalidEnvelope)
	}
	if len(envelope.Salt) != SaltSize || len(envelope.Nonce) != nonceSize || len(envelope.Ciphertext) != DEKSize+gcmTagSize {
		return fmt.Errorf("%w: invalid cryptographic field size", ErrInvalidEnvelope)
	}
	return nil
}

func validateDEK(dek []byte) error {
	if len(dek) != DEKSize {
		return ErrInvalidDEK
	}
	return nil
}

func validatePassword(password []byte) error {
	if len(password) == 0 || len(password) > maxPasswordSize {
		return ErrInvalidPassword
	}
	return nil
}

func validateRecoverySecret(secret []byte) error {
	if len(secret) != RecoverySecretSize {
		return ErrInvalidRecoverySecret
	}
	return nil
}

func validatePasskeySecret(secret []byte) error {
	if len(secret) != PasskeySecretSize {
		return ErrInvalidPasskeySecret
	}
	return nil
}

func validateContext(context Context) error {
	if len(context.UserID) == 0 || len(context.UserID) > maxContextIDSize ||
		len(context.VaultID) == 0 || len(context.VaultID) > maxContextIDSize {
		return ErrInvalidContext
	}
	return nil
}

const (
	nonceSize  = 12
	gcmTagSize = 16
)

func seal(key, plaintext, aad []byte) ([]byte, []byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce, err := randomBytes(aead.NonceSize())
	if err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plaintext, aad), nil
}

func open(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrAuthentication
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create envelope cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create envelope AEAD: %w", err)
	}
	return aead, nil
}

func deriveKey(secret, salt, info []byte) ([]byte, error) {
	key := make([]byte, DEKSize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, salt, info), key); err != nil {
		clear(key)
		return nil, fmt.Errorf("derive envelope key: %w", err)
	}
	return key, nil
}

func passwordVerifier(key, aad []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("omni-money/key-envelope/password-verifier/v1\x00"))
	_, _ = mac.Write(aad)
	return mac.Sum(nil)
}

func hkdfInfo(kind Kind, purpose string) []byte {
	return []byte("omni-money/key-envelope/v1/" + string(kind) + "/" + purpose)
}

// authenticatedData uses length-prefixed identifiers to avoid ambiguous
// concatenations. The format is intentionally stable for persisted envelopes.
func authenticatedData(context Context, kind Kind, version uint8) []byte {
	prefix := []byte("omni-money/key-envelope/aad\x00")
	result := make([]byte, 0, len(prefix)+1+4+len(context.UserID)+4+len(context.VaultID)+4+len(kind))
	result = append(result, prefix...)
	result = append(result, version)
	result = appendLengthPrefixed(result, []byte(kind))
	result = appendLengthPrefixed(result, []byte(context.UserID))
	result = appendLengthPrefixed(result, []byte(context.VaultID))
	return result
}

func appendLengthPrefixed(destination, value []byte) []byte {
	var length [4]byte
	// All callers pass validated identifiers capped at 4096 bytes or a fixed
	// envelope kind. The conversion cannot truncate under that invariant.
	binary.BigEndian.PutUint32(length[:], uint32(len(value))) // #nosec G115 -- bounded above before this helper is called.
	destination = append(destination, length[:]...)
	return append(destination, value...)
}

func randomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		clear(value)
		return nil, fmt.Errorf("generate cryptographic random bytes: %w", err)
	}
	return value, nil
}
