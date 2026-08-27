// Package authn provides the independent second factor used by the public
// Omni Money web UI. It intentionally does not share credentials with the
// upstream access gateway.
package authn

import (
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- RFC 6238 TOTP requires HMAC-SHA-1 interoperability.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"omni_money/backend/secretfile"
)

const (
	totpPeriodSeconds = int64(30)
	totpDigits        = 6
	minTOTPSecretSize = 20
	maxTOTPSecretSize = 64
	maxTOTPFileSize   = 256
)

var (
	// ErrInvalidTOTP is deliberately generic so callers do not expose whether
	// a submitted code was malformed, expired, or simply incorrect.
	ErrInvalidTOTP = errors.New("invalid one-time code")
	// ErrReplayedTOTP distinguishes an already consumed time-step for bounded
	// audit logging while callers still return the same public auth failure.
	ErrReplayedTOTP = errors.New("one-time code was already used")
)

// TOTPVerifier validates RFC 6238 codes and consumes each accepted time-step
// at most once for this process. A mutex makes concurrent login races
// deterministic: only one request can consume a code.
type TOTPVerifier struct {
	secret []byte
	now    func() time.Time

	mu           sync.Mutex
	lastAccepted uint64
	hasAccepted  bool
}

// LoadTOTPVerifier securely loads one unpadded Base32 secret from an
// owner-only file (or a read-only Compose secret directly under /run/secrets).
func LoadTOTPVerifier(path string) (*TOTPVerifier, error) {
	content, err := secretfile.ReadConfidential(strings.TrimSpace(path), maxTOTPFileSize)
	if err != nil {
		return nil, fmt.Errorf("read TOTP secret: %w", err)
	}
	secret, err := DecodeTOTPSecret(string(content))
	if err != nil {
		return nil, err
	}
	return NewTOTPVerifier(secret, time.Now)
}

// DecodeTOTPSecret parses the canonical Base32 representation stored on disk.
func DecodeTOTPSecret(raw string) ([]byte, error) {
	encoded := strings.ToUpper(strings.TrimSpace(raw))
	if encoded == "" || strings.ContainsAny(encoded, " \t\r\n-") {
		return nil, errors.New("TOTP secret must be one Base32 value without whitespace or separators")
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode TOTP secret: %w", err)
	}
	if len(secret) < minTOTPSecretSize || len(secret) > maxTOTPSecretSize {
		return nil, fmt.Errorf("TOTP secret must decode to %d-%d bytes", minTOTPSecretSize, maxTOTPSecretSize)
	}
	return secret, nil
}

// EncodeTOTPSecret returns the unpadded Base32 form understood by common apps
// such as Google Authenticator, 1Password, and Aegis.
func EncodeTOTPSecret(secret []byte) (string, error) {
	if len(secret) < minTOTPSecretSize || len(secret) > maxTOTPSecretSize {
		return "", fmt.Errorf("TOTP secret must contain %d-%d bytes", minTOTPSecretSize, maxTOTPSecretSize)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), nil
}

// NewTOTPVerifier constructs a verifier. The clock argument is injectable for
// deterministic security tests.
func NewTOTPVerifier(secret []byte, now func() time.Time) (*TOTPVerifier, error) {
	if len(secret) < minTOTPSecretSize || len(secret) > maxTOTPSecretSize {
		return nil, fmt.Errorf("TOTP secret must contain %d-%d bytes", minTOTPSecretSize, maxTOTPSecretSize)
	}
	if now == nil {
		return nil, errors.New("TOTP clock is required")
	}
	return &TOTPVerifier{secret: append([]byte(nil), secret...), now: now}, nil
}

// VerifyAndConsume accepts the current 30-second time-step and one adjacent
// step for normal clock skew. An accepted counter cannot be reused.
func (v *TOTPVerifier) VerifyAndConsume(rawCode string) error {
	if v == nil {
		return ErrInvalidTOTP
	}
	code := strings.TrimSpace(rawCode)
	if len(code) != totpDigits {
		return ErrInvalidTOTP
	}
	for _, char := range code {
		if char < '0' || char > '9' {
			return ErrInvalidTOTP
		}
	}

	now := v.now().UTC()
	unixSeconds := now.Unix()
	if unixSeconds < 0 {
		return ErrInvalidTOTP
	}
	current := uint64(unixSeconds) / uint64(totpPeriodSeconds)
	candidates := []uint64{current}
	if current > 0 {
		candidates = append(candidates, current-1)
	}
	candidates = append(candidates, current+1)

	var matched uint64
	found := false
	for _, counter := range candidates {
		expected := generateTOTPCode(v.secret, counter, totpDigits)
		if subtle.ConstantTimeCompare([]byte(code), []byte(expected)) == 1 {
			matched = counter
			found = true
		}
	}
	if !found {
		return ErrInvalidTOTP
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if v.hasAccepted && matched <= v.lastAccepted {
		return ErrReplayedTOTP
	}
	v.lastAccepted = matched
	v.hasAccepted = true
	return nil
}

func generateTOTPCode(secret []byte, counter uint64, digits int) string {
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac := hmac.New(sha1.New, secret) // #nosec G401 -- HMAC-SHA-1 is mandated by RFC 6238.
	_, _ = mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	binaryCode := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])

	modulus := uint32(1)
	for range digits {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", digits, binaryCode%modulus)
}
