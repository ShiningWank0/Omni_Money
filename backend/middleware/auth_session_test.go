package middleware

import (
	"testing"
	"time"
)

func TestVerifyPasswordPreservesBcryptBehavior(t *testing.T) {
	const hash = "$2y$04$.OWNgfSMaTsdqHrwD6ydEeCs3dBUsAzNlpFzq3kJuK4BtUqU8E0WG"
	sessionManager := NewSessionManager(time.Hour)
	defer sessionManager.Close()

	manager := NewAuthSessionManager(sessionManager, hash)
	if !manager.VerifyPassword("test-password") {
		t.Fatal("valid bcrypt password was rejected")
	}
	if manager.VerifyPassword("wrong-password") {
		t.Fatal("invalid bcrypt password was accepted")
	}
	if NewAuthSessionManager(sessionManager, "not-a-bcrypt-hash").VerifyPassword("test-password") {
		t.Fatal("malformed bcrypt hash was accepted")
	}
}

func TestAuthSessionManagerGCOldLoginAttempts(t *testing.T) {
	sessionManager := NewSessionManager(time.Hour)
	defer sessionManager.Close()

	authManager := NewAuthSessionManager(sessionManager, "")
	authManager.attempts["old"] = loginAttempt{
		Count:       1,
		LastAttempt: time.Now().Add(-loginAttemptRetentionDuration - time.Minute),
	}
	authManager.attempts["fresh"] = loginAttempt{
		Count:       1,
		LastAttempt: time.Now(),
	}
	authManager.lastGC = time.Now().Add(-loginAttemptGCInterval - time.Second)

	remaining := authManager.RemainingAttempts("fresh")

	if remaining != LoginAttemptLimit-1 {
		t.Fatalf("remaining attempts = %d, want %d", remaining, LoginAttemptLimit-1)
	}
	if _, ok := authManager.attempts["old"]; ok {
		t.Fatal("old login attempt was not garbage collected")
	}
	if _, ok := authManager.attempts["fresh"]; !ok {
		t.Fatal("fresh login attempt was garbage collected")
	}
}

func TestAuthSessionManagerClearsExpiredLock(t *testing.T) {
	sessionManager := NewSessionManager(time.Hour)
	defer sessionManager.Close()

	authManager := NewAuthSessionManager(sessionManager, "")
	authManager.attempts["locked"] = loginAttempt{
		Count:       LoginAttemptLimit,
		LastAttempt: time.Now().Add(-LoginLockoutDuration - time.Second),
	}

	locked, remaining := authManager.IsIPLocked("locked")

	if locked {
		t.Fatal("expired lock is still locked")
	}
	if remaining != 0 {
		t.Fatalf("remaining lock duration = %s, want 0", remaining)
	}
	if _, ok := authManager.attempts["locked"]; ok {
		t.Fatal("expired lock attempt was not deleted")
	}
}
