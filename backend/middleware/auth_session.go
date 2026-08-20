package middleware

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	LoginAttemptLimit             = 5
	LoginLockoutDuration          = 15 * time.Minute
	loginAttemptRetentionDuration = 30 * time.Minute
	loginAttemptGCInterval        = time.Minute
	maxLoginAttemptEntries        = 4096
	globalAuthAttemptLimit        = 30
	globalAuthAttemptWindow       = time.Minute
	maxConcurrentPasswordChecks   = 4
	MinimumBcryptCost             = 12
	MaximumBcryptCost             = 16
	bcryptHashLength              = 60
	bcryptEncodedSaltLength       = 22
	bcryptDecodedSaltLength       = 16
	bcryptDecodedDigestLength     = 23
)

var bcryptBase64 = base64.NewEncoding("./ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789").WithPadding(base64.NoPadding).Strict()

type loginAttempt struct {
	Count       int
	LastAttempt time.Time
}

// OneTimeCodeVerifier is implemented by authn.TOTPVerifier. Keeping this small
// interface avoids coupling session storage to one TOTP implementation.
type OneTimeCodeVerifier interface {
	VerifyAndConsume(string) error
}

// AuthSessionManager bounds login state and expensive bcrypt work in addition
// to managing Omni's independent second factor.
type AuthSessionManager struct {
	passwordHash   string
	sessionManager *SessionManager
	totpVerifier   OneTimeCodeVerifier

	mu             sync.Mutex
	attempts       map[string]loginAttempt
	globalAttempts []time.Time
	lastGC         time.Time
	verifySlots    chan struct{}
}

// NewAuthSessionManager accepts an optional verifier for source compatibility
// with older unit tests.
func NewAuthSessionManager(sessionManager *SessionManager, passwordHash string, verifier ...OneTimeCodeVerifier) *AuthSessionManager {
	var totpVerifier OneTimeCodeVerifier
	if len(verifier) > 0 {
		totpVerifier = verifier[0]
	}
	return &AuthSessionManager{
		passwordHash:   strings.TrimSpace(passwordHash),
		sessionManager: sessionManager,
		totpVerifier:   totpVerifier,
		attempts:       make(map[string]loginAttempt),
		verifySlots:    make(chan struct{}, maxConcurrentPasswordChecks),
	}
}

func ValidatePasswordHash(passwordHash string) error {
	passwordHash = strings.TrimSpace(passwordHash)
	if passwordHash == "" {
		return errors.New("AUTH_PASSWORD_HASH is required")
	}
	if !hasValidBcryptEncoding(passwordHash) {
		return errors.New("AUTH_PASSWORD_HASH must be a valid bcrypt hash")
	}
	cost, err := bcrypt.Cost([]byte(passwordHash))
	if err != nil {
		return errors.New("AUTH_PASSWORD_HASH must be a valid bcrypt hash")
	}
	if cost < MinimumBcryptCost {
		return fmt.Errorf("AUTH_PASSWORD_HASH bcrypt cost must be at least %d", MinimumBcryptCost)
	}
	if cost > MaximumBcryptCost {
		return fmt.Errorf("AUTH_PASSWORD_HASH bcrypt cost must not exceed %d", MaximumBcryptCost)
	}
	return nil
}

// hasValidBcryptEncoding validates the complete modular-crypt representation
// without performing an expensive password comparison. bcrypt.Cost validates
// only the version and cost prefix, so it can otherwise accept truncated data,
// trailing data, and invalid characters in the salt or digest.
func hasValidBcryptEncoding(passwordHash string) bool {
	if len(passwordHash) != bcryptHashLength {
		return false
	}
	switch passwordHash[:4] {
	case "$2a$", "$2b$", "$2y$":
	default:
		return false
	}
	if passwordHash[6] != '$' {
		return false
	}

	payload := passwordHash[7:]
	salt, err := bcryptBase64.DecodeString(payload[:bcryptEncodedSaltLength])
	if err != nil || len(salt) != bcryptDecodedSaltLength {
		return false
	}
	digest, err := bcryptBase64.DecodeString(payload[bcryptEncodedSaltLength:])
	return err == nil && len(digest) == bcryptDecodedDigestLength
}

func (a *AuthSessionManager) SessionManager() *SessionManager { return a.sessionManager }

func (a *AuthSessionManager) PasswordConfigured() bool {
	return ValidatePasswordHash(a.passwordHash) == nil
}

func (a *AuthSessionManager) TOTPRequired() bool { return a.totpVerifier != nil }

// ReserveAuthAttempt atomically applies an IP lock and a global ceiling before
// any expensive bcrypt work. The global ceiling protects against rotating-IP
// CPU exhaustion and is deliberately short-lived.
func (a *AuthSessionManager) ReserveAuthAttempt(ip string) (bool, time.Duration) {
	ip = normalizeIPKey(ip)
	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.gcLoginAttemptsLocked(now)
	key := a.attemptKeyLocked(ip, false)
	if attempt, ok := a.attempts[key]; ok && attempt.Count >= LoginAttemptLimit {
		elapsed := now.Sub(attempt.LastAttempt)
		if elapsed < LoginLockoutDuration {
			return false, LoginLockoutDuration - elapsed
		}
		delete(a.attempts, key)
	}

	cutoff := now.Add(-globalAuthAttemptWindow)
	firstValid := 0
	for firstValid < len(a.globalAttempts) && !a.globalAttempts[firstValid].After(cutoff) {
		firstValid++
	}
	a.globalAttempts = append(a.globalAttempts[:0], a.globalAttempts[firstValid:]...)
	if len(a.globalAttempts) >= globalAuthAttemptLimit {
		return false, a.globalAttempts[0].Add(globalAuthAttemptWindow).Sub(now)
	}
	a.globalAttempts = append(a.globalAttempts, now)
	return true, 0
}

// VerifyCredentials performs the expensive comparison under a small semaphore.
// It never trims the submitted password; whitespace may be part of a credential.
func (a *AuthSessionManager) VerifyCredentials(password, oneTimeCode string) (valid bool, busy bool) {
	valid, busy = a.verifyPassword(password)
	if busy {
		return false, true
	}
	if !valid {
		return false, false
	}
	if a.totpVerifier != nil && a.totpVerifier.VerifyAndConsume(oneTimeCode) != nil {
		return false, false
	}
	return true, false
}

// VerifyPasswordOnlyForReauthentication verifies only the password for an
// already-authenticated in-session step-up. It must not be used for login:
// login uses VerifyCredentials, which also enforces configured TOTP.
func (a *AuthSessionManager) VerifyPasswordOnlyForReauthentication(password string) (valid bool, busy bool) {
	return a.verifyPassword(password)
}

func (a *AuthSessionManager) verifyPassword(password string) (valid bool, busy bool) {
	if !a.PasswordConfigured() {
		return false, false
	}
	select {
	case a.verifySlots <- struct{}{}:
		defer func() { <-a.verifySlots }()
	default:
		return false, true
	}
	return bcrypt.CompareHashAndPassword([]byte(a.passwordHash), []byte(password)) == nil, false
}

func (a *AuthSessionManager) IsIPLocked(ip string) (bool, time.Duration) {
	ip = normalizeIPKey(ip)
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gcLoginAttemptsLocked(now)
	key := a.attemptKeyLocked(ip, false)
	attempt, ok := a.attempts[key]
	if !ok || attempt.Count < LoginAttemptLimit {
		return false, 0
	}
	elapsed := now.Sub(attempt.LastAttempt)
	if elapsed >= LoginLockoutDuration {
		delete(a.attempts, key)
		return false, 0
	}
	return true, LoginLockoutDuration - elapsed
}

func (a *AuthSessionManager) RecordLoginAttempt(ip string, success bool) {
	ip = normalizeIPKey(ip)
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gcLoginAttemptsLocked(now)
	key := a.attemptKeyLocked(ip, !success)
	if success {
		// Do not clear the shared overflow record after a successful login.
		if key != "overflow" {
			delete(a.attempts, key)
		}
		return
	}
	attempt := a.attempts[key]
	attempt.Count++
	attempt.LastAttempt = now
	a.attempts[key] = attempt
}

func (a *AuthSessionManager) RemainingAttempts(ip string) int {
	ip = normalizeIPKey(ip)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gcLoginAttemptsLocked(time.Now())
	attempt, ok := a.attempts[a.attemptKeyLocked(ip, false)]
	if !ok {
		return LoginAttemptLimit
	}
	if attempt.Count >= LoginAttemptLimit {
		return 0
	}
	return LoginAttemptLimit - attempt.Count
}

func (a *AuthSessionManager) CreateSession(username string) (*Session, error) {
	return a.sessionManager.CreateSession(username)
}

func normalizeIPKey(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "unknown"
	}
	return ip
}

func (a *AuthSessionManager) attemptKeyLocked(ip string, create bool) string {
	if _, ok := a.attempts[ip]; ok {
		return ip
	}
	// Reserve one bounded entry for all additional addresses. Reads must map
	// to the same overflow record or rotating IPs could bypass its lockout.
	if len(a.attempts) >= maxLoginAttemptEntries-1 {
		return "overflow"
	}
	if !create {
		return ip
	}
	return ip
}

func (a *AuthSessionManager) gcLoginAttemptsLocked(now time.Time) {
	if !a.lastGC.IsZero() && now.Sub(a.lastGC) < loginAttemptGCInterval {
		return
	}
	cutoff := now.Add(-loginAttemptRetentionDuration)
	for ip, attempt := range a.attempts {
		if attempt.LastAttempt.Before(cutoff) {
			delete(a.attempts, ip)
		}
	}
	a.lastGC = now
}
