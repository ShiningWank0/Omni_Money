package middleware

import (
	"strconv"
	"testing"
	"time"
)

const securityTestPasswordHash = "$2y$12$TMw6R8z61SPOp1Y/4t3mLu/LVqe3.L5d5.H9piLwdDjKpSytNxaEi"

func TestAuthAttemptMapIsBoundedAndOverflowLockAppliesToNewAddresses(t *testing.T) {
	sessionManager := NewSessionManager(time.Hour)
	t.Cleanup(sessionManager.Close)
	authManager := NewAuthSessionManager(sessionManager, securityTestPasswordHash)

	for index := 0; index < maxLoginAttemptEntries-1; index++ {
		authManager.RecordLoginAttempt("test-address-"+strconv.Itoa(index), false)
	}
	for index := 0; index < LoginAttemptLimit; index++ {
		authManager.RecordLoginAttempt("overflow-source-"+strconv.Itoa(index), false)
	}

	authManager.mu.Lock()
	entryCount := len(authManager.attempts)
	overflow := authManager.attempts["overflow"]
	authManager.mu.Unlock()
	if entryCount > maxLoginAttemptEntries {
		t.Fatalf("attempt map entries=%d, exceeds cap %d", entryCount, maxLoginAttemptEntries)
	}
	if overflow.Count != LoginAttemptLimit {
		t.Fatalf("overflow count=%d, want %d", overflow.Count, LoginAttemptLimit)
	}
	if remaining := authManager.RemainingAttempts("brand-new-address"); remaining != 0 {
		t.Fatalf("new address remaining attempts=%d, want 0 from overflow lock", remaining)
	}
	if locked, _ := authManager.IsIPLocked("another-new-address"); !locked {
		t.Fatal("overflow lock did not apply to a new address")
	}
	authManager.RecordLoginAttempt("successful-new-address", true)
	if locked, _ := authManager.IsIPLocked("yet-another-new-address"); !locked {
		t.Fatal("success through overflow address cleared the shared overflow lock")
	}
}

func TestGlobalAuthenticationAttemptCeiling(t *testing.T) {
	sessionManager := NewSessionManager(time.Hour)
	t.Cleanup(sessionManager.Close)
	authManager := NewAuthSessionManager(sessionManager, securityTestPasswordHash)

	for index := 0; index < globalAuthAttemptLimit; index++ {
		allowed, retry := authManager.ReserveAuthAttempt("rotating-address-" + strconv.Itoa(index))
		if !allowed || retry != 0 {
			t.Fatalf("attempt %d allowed=%v retry=%s", index+1, allowed, retry)
		}
	}
	allowed, retry := authManager.ReserveAuthAttempt("one-address-too-many")
	if allowed || retry <= 0 || retry > globalAuthAttemptWindow {
		t.Fatalf("ceiling result allowed=%v retry=%s", allowed, retry)
	}
}

func TestPasswordVerificationSemaphoreFailsBusyWithoutStartingExtraBcrypt(t *testing.T) {
	sessionManager := NewSessionManager(time.Hour)
	t.Cleanup(sessionManager.Close)
	authManager := NewAuthSessionManager(sessionManager, securityTestPasswordHash)

	for range maxConcurrentPasswordChecks {
		authManager.verifySlots <- struct{}{}
	}
	valid, busy := authManager.VerifyCredentials("test-password", "")
	if valid || !busy {
		t.Fatalf("saturated semaphore valid=%v busy=%v, want false true", valid, busy)
	}
	<-authManager.verifySlots
	valid, busy = authManager.VerifyCredentials("test-password", "")
	if !valid || busy {
		t.Fatalf("available semaphore valid=%v busy=%v, want true false", valid, busy)
	}
}

func TestPasswordOnlyReauthenticationDoesNotConsumeOrRequireTOTP(t *testing.T) {
	sessionManager := NewSessionManager(time.Hour)
	t.Cleanup(sessionManager.Close)
	verifier := &countingOneTimeCodeVerifier{}
	authManager := NewAuthSessionManager(sessionManager, securityTestPasswordHash, verifier)

	if valid, busy := authManager.VerifyPasswordOnlyForReauthentication("test-password"); !valid || busy {
		t.Fatalf("password-only reauthentication valid=%v busy=%v, want true false", valid, busy)
	}
	if verifier.calls != 0 {
		t.Fatalf("password-only reauthentication called TOTP verifier %d times", verifier.calls)
	}
	if valid, busy := authManager.VerifyPasswordOnlyForReauthentication("wrong-password"); valid || busy {
		t.Fatalf("wrong password-only reauthentication valid=%v busy=%v, want false false", valid, busy)
	}
}

type countingOneTimeCodeVerifier struct {
	calls int
}

func (v *countingOneTimeCodeVerifier) VerifyAndConsume(string) error {
	v.calls++
	return nil
}

func TestPasswordVerificationDoesNotTrimCredential(t *testing.T) {
	sessionManager := NewSessionManager(time.Hour)
	t.Cleanup(sessionManager.Close)
	authManager := NewAuthSessionManager(sessionManager, securityTestPasswordHash)

	if valid, busy := authManager.VerifyCredentials(" test-password ", ""); valid || busy {
		t.Fatalf("whitespace-modified credential valid=%v busy=%v", valid, busy)
	}
}

func TestValidatePasswordHashRequiresMinimumCost(t *testing.T) {
	for name, hash := range map[string]string{
		"empty":                    "",
		"malformed":                "not-a-bcrypt-hash",
		"invalid salt character":   "$2y$12$!Mw6R8z61SPOp1Y/4t3mLu/LVqe3.L5d5.H9piLwdDjKpSytNxaEi",
		"invalid digest character": "$2y$12$TMw6R8z61SPOp1Y/4t3mLu/LVqe3.L5d5.H9piLwdDjKpSytNxaE!",
		"truncated digest":         "$2y$12$TMw6R8z61SPOp1Y/4t3mLu/LVqe3.L5d5.H9piLwdDjKpSytNxaE",
		"trailing data":            "$2y$12$TMw6R8z61SPOp1Y/4t3mLu/LVqe3.L5d5.H9piLwdDjKpSytNxaEi.",
		"unsupported 2x version":   "$2x$12$TMw6R8z61SPOp1Y/4t3mLu/LVqe3.L5d5.H9piLwdDjKpSytNxaEi",
		"cost four":                "$2y$04$.OWNgfSMaTsdqHrwD6ydEeCs3dBUsAzNlpFzq3kJuK4BtUqU8E0WG",
		"cost seventeen":           "$2y$17$TMw6R8z61SPOp1Y/4t3mLu/LVqe3.L5d5.H9piLwdDjKpSytNxaEi",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePasswordHash(hash); err == nil {
				t.Fatal("weak or invalid password hash was accepted")
			}
		})
	}
	if err := ValidatePasswordHash(securityTestPasswordHash); err != nil {
		t.Fatalf("cost-12 password hash rejected: %v", err)
	}
	for _, prefix := range []string{"$2a$", "$2b$", "$2y$"} {
		hash := prefix + securityTestPasswordHash[len(prefix):]
		if err := ValidatePasswordHash(hash); err != nil {
			t.Errorf("supported bcrypt prefix %q rejected: %v", prefix, err)
		}
	}
}
