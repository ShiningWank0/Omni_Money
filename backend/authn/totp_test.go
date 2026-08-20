package authn

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRFC6238SHA1Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	tests := []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, tt := range tests {
		counter := uint64(tt.unix / totpPeriodSeconds)
		if got := generateTOTPCode(secret, counter, 8); got != tt.want {
			t.Errorf("time %d code=%s, want %s", tt.unix, got, tt.want)
		}
	}
}

func TestTOTPWindowAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1_800_000_000, 0)
	verifier, err := NewTOTPVerifier(secret, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	currentCounter := uint64(now.Unix() / totpPeriodSeconds)

	previous := generateTOTPCode(secret, currentCounter-1, totpDigits)
	if err := verifier.VerifyAndConsume(previous); err != nil {
		t.Fatalf("previous window rejected: %v", err)
	}
	current := generateTOTPCode(secret, currentCounter, totpDigits)
	if err := verifier.VerifyAndConsume(current); err != nil {
		t.Fatalf("current window rejected: %v", err)
	}
	if err := verifier.VerifyAndConsume(current); !errors.Is(err, ErrReplayedTOTP) {
		t.Fatalf("replay error=%v, want ErrReplayedTOTP", err)
	}

	now = now.Add(30 * time.Second)
	next := generateTOTPCode(secret, currentCounter+1, totpDigits)
	if err := verifier.VerifyAndConsume(next); err != nil {
		t.Fatalf("next window rejected: %v", err)
	}
	tooOld := generateTOTPCode(secret, currentCounter-2, totpDigits)
	if err := verifier.VerifyAndConsume(tooOld); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("old code error=%v, want ErrInvalidTOTP", err)
	}
}

func TestTOTPConcurrentReplayOnlyOneRequestConsumesCode(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1_800_000_000, 0)
	verifier, err := NewTOTPVerifier(secret, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	code := generateTOTPCode(secret, uint64(now.Unix()/totpPeriodSeconds), totpDigits)

	const workers = 32
	start := make(chan struct{})
	results := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for range workers {
		go func() {
			ready.Done()
			<-start
			results <- verifier.VerifyAndConsume(code)
		}()
	}
	ready.Wait()
	close(start)

	accepted := 0
	replayed := 0
	for range workers {
		switch err := <-results; {
		case err == nil:
			accepted++
		case errors.Is(err, ErrReplayedTOTP):
			replayed++
		default:
			t.Fatalf("unexpected verification result: %v", err)
		}
	}
	if accepted != 1 || replayed != workers-1 {
		t.Fatalf("accepted=%d replayed=%d, want 1 and %d", accepted, replayed, workers-1)
	}
}

func TestTOTPAcceptingFutureWindowPreventsCounterRollback(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1_800_000_000, 0)
	verifier, err := NewTOTPVerifier(secret, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	currentCounter := uint64(now.Unix() / totpPeriodSeconds)

	future := generateTOTPCode(secret, currentCounter+1, totpDigits)
	if err := verifier.VerifyAndConsume(future); err != nil {
		t.Fatalf("future window rejected: %v", err)
	}
	current := generateTOTPCode(secret, currentCounter, totpDigits)
	if err := verifier.VerifyAndConsume(current); !errors.Is(err, ErrReplayedTOTP) {
		t.Fatalf("counter rollback error=%v, want ErrReplayedTOTP", err)
	}
}

func TestTOTPRejectsMalformedCodesAndSecrets(t *testing.T) {
	for _, secret := range []string{"", "ABC DEF", "%%%%", "JBSWY3DP"} {
		if _, err := DecodeTOTPSecret(secret); err == nil {
			t.Errorf("secret %q was accepted", secret)
		}
	}

	verifier, err := NewTOTPVerifier([]byte("12345678901234567890"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"", "12345", "1234567", "abcdef", "１２３４５６"} {
		if err := verifier.VerifyAndConsume(code); !errors.Is(err, ErrInvalidTOTP) {
			t.Errorf("code %q error=%v, want ErrInvalidTOTP", code, err)
		}
	}

	var nilVerifier *TOTPVerifier
	if err := nilVerifier.VerifyAndConsume("123456"); !errors.Is(err, ErrInvalidTOTP) {
		t.Errorf("nil verifier error=%v, want ErrInvalidTOTP", err)
	}
}

func TestTOTPVerifierCopiesSecret(t *testing.T) {
	secret := []byte("12345678901234567890")
	original := append([]byte(nil), secret...)
	now := time.Unix(1_800_000_000, 0)
	verifier, err := NewTOTPVerifier(secret, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for i := range secret {
		secret[i] = 0
	}
	code := generateTOTPCode(original, uint64(now.Unix()/totpPeriodSeconds), totpDigits)
	if err := verifier.VerifyAndConsume(code); err != nil {
		t.Fatalf("mutating caller-owned secret changed verifier: %v", err)
	}
}

func TestTOTPSecretRoundTrip(t *testing.T) {
	secret := []byte("12345678901234567890")
	encoded, err := EncodeTOTPSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTOTPSecret(encoded + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(secret) {
		t.Fatalf("decoded secret mismatch")
	}
}
