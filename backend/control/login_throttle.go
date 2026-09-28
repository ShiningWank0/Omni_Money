package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const loginThrottleGCInterval = 128

// LoginThrottlePolicy describes the account-level lockout progression applied
// by RegisterLoginFailure. The store keeps the arithmetic atomic; policy stays
// in the authentication layer so the curve can be tested without SQL.
type LoginThrottlePolicy struct {
	MaxFailures int
	BaseLock    time.Duration
	MaxLock     time.Duration
	Decay       time.Duration
}

func (p LoginThrottlePolicy) validate() error {
	if p.MaxFailures < 1 || p.MaxFailures > 1000 {
		return fmt.Errorf("login throttle MaxFailures must be between 1 and 1000")
	}
	if p.BaseLock < time.Second || p.MaxLock < p.BaseLock {
		return fmt.Errorf("login throttle lock durations are invalid")
	}
	if p.MaxLock > 24*time.Hour {
		return fmt.Errorf("login throttle MaxLock must not exceed 24h")
	}
	if p.Decay < p.MaxLock {
		return fmt.Errorf("login throttle Decay must not be shorter than MaxLock")
	}
	return nil
}

func validateLoginThrottleKey(key string) error {
	if len(key) != 64 {
		return errors.New("login throttle key must be a 64-character lowercase hex digest")
	}
	for index := 0; index < len(key); index++ {
		character := key[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return errors.New("login throttle key must be a 64-character lowercase hex digest")
		}
	}
	return nil
}

// LoginThrottleLockedUntil reports the active lock expiry for an account key.
// A zero time means no lock is active; expired rows are treated as unlocked
// without writing, so an attacker cannot extend a lock by probing it.
func (s *Store) LoginThrottleLockedUntil(ctx context.Context, accountKey string, now time.Time) (time.Time, error) {
	if err := validateLoginThrottleKey(accountKey); err != nil {
		return time.Time{}, err
	}
	now, err := validateOperationTime(now)
	if err != nil {
		return time.Time{}, err
	}
	db, err := s.database()
	if err != nil {
		return time.Time{}, err
	}
	var lockedUntil sql.NullInt64
	err = db.QueryRowContext(ctx,
		`SELECT locked_until_ms FROM login_throttle WHERE account_key = ?`, accountKey,
	).Scan(&lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read login throttle: %w", err)
	}
	if !lockedUntil.Valid {
		return time.Time{}, nil
	}
	until := time.UnixMilli(lockedUntil.Int64).UTC()
	if !until.After(now) {
		return time.Time{}, nil
	}
	return until, nil
}

// RegisterLoginFailure atomically records one failed authentication for an
// account key and returns the lock expiry when the failure triggered or
// extended one. Consecutive lock cycles double the lock duration up to
// MaxLock; the progression resets after Decay without a failure or after a
// successful ClearLoginFailures.
func (s *Store) RegisterLoginFailure(ctx context.Context, accountKey string, now time.Time, policy LoginThrottlePolicy) (time.Time, error) {
	if err := validateLoginThrottleKey(accountKey); err != nil {
		return time.Time{}, err
	}
	if err := policy.validate(); err != nil {
		return time.Time{}, err
	}
	now, err := validateOperationTime(now)
	if err != nil {
		return time.Time{}, err
	}
	var lockedUntil time.Time
	err = s.withImmediate(ctx, func(connection *sql.Conn) error {
		var failureCount, lockLevel int
		var lastFailure, locked sql.NullInt64
		readErr := connection.QueryRowContext(ctx, `
			SELECT failure_count, lock_level, locked_until_ms, last_failure_ms
			FROM login_throttle WHERE account_key = ?`, accountKey,
		).Scan(&failureCount, &lockLevel, &locked, &lastFailure)
		switch {
		case errors.Is(readErr, sql.ErrNoRows):
			failureCount, lockLevel = 0, 0
		case readErr != nil:
			return fmt.Errorf("read login throttle: %w", readErr)
		}
		nowMillis := now.UnixMilli()
		if lastFailure.Valid && now.Sub(time.UnixMilli(lastFailure.Int64)) > policy.Decay {
			failureCount, lockLevel = 0, 0
		}
		if locked.Valid && time.UnixMilli(locked.Int64).After(now) {
			// Already locked. Preserve the remaining window without advancing
			// the counter, so the lock cannot be extended by traffic.
			lockedUntil = time.UnixMilli(locked.Int64).UTC()
			return nil
		}
		failureCount++
		locked.Valid = false
		locked.Int64 = 0
		if failureCount >= policy.MaxFailures {
			// The level only escalates until the lock duration reaches
			// MaxLock; keeping it bounded also keeps it inside the schema
			// CHECK range, where an out-of-range level would make every
			// subsequent failure fail to persist (fail-open).
			maxLevel := 1
			for duration := policy.BaseLock; duration < policy.MaxLock && maxLevel < 64; duration *= 2 {
				maxLevel++
			}
			if lockLevel < maxLevel {
				lockLevel++
			}
			duration := policy.BaseLock
			for cycle := 1; cycle < lockLevel && duration < policy.MaxLock; cycle++ {
				duration *= 2
			}
			if duration > policy.MaxLock {
				duration = policy.MaxLock
			}
			lockedUntil = now.Add(duration).UTC()
			locked.Valid = true
			locked.Int64 = lockedUntil.UnixMilli()
			failureCount = 0
		}
		if _, err := connection.ExecContext(ctx, `
			INSERT INTO login_throttle(
				account_key, failure_count, lock_level, locked_until_ms, last_failure_ms, updated_at_ms
			) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(account_key) DO UPDATE SET
				failure_count = excluded.failure_count,
				lock_level = excluded.lock_level,
				locked_until_ms = excluded.locked_until_ms,
				last_failure_ms = excluded.last_failure_ms,
				updated_at_ms = excluded.updated_at_ms`,
			accountKey, failureCount, lockLevel, locked, nowMillis, nowMillis,
		); err != nil {
			return fmt.Errorf("record login failure: %w", err)
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	s.maybePurgeLoginThrottle(ctx, now, policy)
	return lockedUntil, nil
}

// ClearLoginFailures removes the account throttle after a successful
// authentication. Failure to clear never blocks the authenticated request, but
// callers must surface it as a security event.
func (s *Store) ClearLoginFailures(ctx context.Context, accountKey string) error {
	if err := validateLoginThrottleKey(accountKey); err != nil {
		return err
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM login_throttle WHERE account_key = ?`, accountKey); err != nil {
		return fmt.Errorf("clear login failures: %w", err)
	}
	return nil
}

// maybePurgeLoginThrottle bounds table growth from attempts against arbitrary
// addresses. Rows older than the decay window would reset on their next write,
// so they are safe to drop. Cleanup is best-effort and never fails a login.
func (s *Store) maybePurgeLoginThrottle(ctx context.Context, now time.Time, policy LoginThrottlePolicy) {
	if s.throttleWrites.Add(1)%loginThrottleGCInterval != 0 {
		return
	}
	db, err := s.database()
	if err != nil {
		return
	}
	_, _ = db.ExecContext(ctx, `
		DELETE FROM login_throttle
		WHERE COALESCE(locked_until_ms, last_failure_ms) < ?`,
		now.Add(-policy.Decay).UnixMilli(),
	)
}
