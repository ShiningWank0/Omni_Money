package serverauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"omni_money/backend/control"
)

// Account-level authentication throttle. The per-IP HTTP limiter bounds a
// single source; this progression bounds guessing against one account even
// when the source rotates. Five consecutive failures lock the account for
// 15 minutes; each further locked cycle doubles the wait up to one hour, and
// the progression forgets itself after a full day without failures. A
// successful authentication by any method clears the counter.
const (
	loginThrottleMaxFailures = 5
	loginThrottleBaseLock    = 15 * time.Minute
	loginThrottleMaxLock     = 60 * time.Minute
	loginThrottleDecay       = 24 * time.Hour
)

var loginThrottlePolicy = control.LoginThrottlePolicy{
	MaxFailures: loginThrottleMaxFailures,
	BaseLock:    loginThrottleBaseLock,
	MaxLock:     loginThrottleMaxLock,
	Decay:       loginThrottleDecay,
}

// LoginThrottledError reports that an account is temporarily locked because of
// repeated failed authentication. It carries no account identifier so callers
// cannot leak one through logs or error envelopes.
type LoginThrottledError struct {
	RetryAfter time.Duration
}

func (e *LoginThrottledError) Error() string {
	return fmt.Sprintf("login attempts are temporarily rejected for %s", e.RetryAfter)
}

// LoginThrottledRetryAfter extracts the remaining lock window from err.
func LoginThrottledRetryAfter(err error) (time.Duration, bool) {
	var target *LoginThrottledError
	if !errors.As(err, &target) || target == nil || target.RetryAfter <= 0 {
		return 0, false
	}
	return target.RetryAfter, true
}

// LoginThrottleKey derives the stored counter key from a normalized login
// email. The digest keeps raw addresses of unknown or disabled accounts out of
// the throttle table while remaining stable across restarts.
func LoginThrottleKey(email string) string {
	digest := sha256.Sum256([]byte("omni-money/login-throttle/v1\x00" + email))
	return hex.EncodeToString(digest[:])
}

func (s *Service) checkLoginThrottle(ctx context.Context, accountKey string, now time.Time) (time.Duration, error) {
	lockedUntil, err := s.store.LoginThrottleLockedUntil(ctx, accountKey, now)
	if err != nil {
		return 0, err
	}
	if lockedUntil.IsZero() {
		return 0, nil
	}
	remaining := lockedUntil.Sub(now)
	if remaining <= 0 {
		return 0, nil
	}
	return remaining, nil
}

// recordLoginFailure is deliberately non-fatal: an audit-grade failure to
// persist the counter must not turn a rejected login into a different
// observable error, but it is logged as a security event.
func (s *Service) recordLoginFailure(ctx context.Context, accountKey string, now time.Time) {
	if _, err := s.store.RegisterLoginFailure(ctx, accountKey, now, loginThrottlePolicy); err != nil {
		log.Printf("security_event=login_throttle_record_failed error=%v", err)
	}
}

func (s *Service) clearLoginFailures(ctx context.Context, accountKey string) {
	if err := s.store.ClearLoginFailures(ctx, accountKey); err != nil {
		log.Printf("security_event=login_throttle_clear_failed error=%v", err)
	}
}
