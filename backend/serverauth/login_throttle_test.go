package serverauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/middleware"
	"omni_money/backend/securedb"
)

type throttleTestFixture struct {
	service  *Service
	store    *fakeControlStore
	user     control.UserSummary
	email    string
	password []byte
	opened   int
}

func newThrottleTestFixture(t *testing.T) *throttleTestFixture {
	t.Helper()
	password := []byte("correct horse battery staple")
	dek := bytes.Repeat([]byte{0x57}, keyenvelope.DEKSize)
	binding := keyenvelope.Context{UserID: serverAuthTestUserID, VaultID: serverAuthTestVaultID}
	passwordEnvelope, err := keyenvelope.WrapWithPassword(dek, password, binding)
	if err != nil {
		t.Fatal(err)
	}
	user := control.UserSummary{
		ID:          serverAuthTestUserID,
		Email:       "person@example.com",
		DisplayName: "Person",
		Role:        control.RoleUser,
		State:       control.UserActive,
	}
	credential := control.PasswordCredential{
		UserID:    user.ID,
		Envelope:  *passwordEnvelope,
		CreatedAt: serverAuthTestNow.Add(-time.Hour),
		UpdatedAt: serverAuthTestNow.Add(-time.Minute),
	}
	store := &fakeControlStore{
		getUserByEmailFn: func(_ context.Context, email string) (control.UserSummary, error) {
			if email != user.Email {
				return control.UserSummary{}, control.ErrNotFound
			}
			return user, nil
		},
		getUserFn: func(_ context.Context, userID string) (control.UserSummary, error) {
			if userID != user.ID {
				return control.UserSummary{}, control.ErrNotFound
			}
			return user, nil
		},
		getPasswordCredentialFn: func(_ context.Context, userID string) (control.PasswordCredential, error) {
			if userID != user.ID {
				return control.PasswordCredential{}, control.ErrNotFound
			}
			return credential, nil
		},
		lookupVaultIDFn: func(context.Context, string) (string, error) {
			return serverAuthTestVaultID, nil
		},
		recordSuccessfulLoginFn: func(context.Context, string, control.PasswordCredential, time.Time) error {
			return nil
		},
	}
	fixture := &throttleTestFixture{store: store, user: user, email: user.Email, password: password}
	fixture.service = newServerAuthTestService(t, store, nil, func(user control.UserSummary, vaultID string, key *securedb.RawKey) (*middleware.Session, error) {
		defer key.Destroy()
		if user.ID != fixture.user.ID || vaultID != serverAuthTestVaultID {
			return nil, errors.New("open session received wrong account or vault")
		}
		fixture.opened++
		return &middleware.Session{ID: "throttle-session", UserID: user.ID}, nil
	}, nil, nil)
	return fixture
}

func (f *throttleTestFixture) failLogin(t *testing.T, now time.Time) error {
	t.Helper()
	_, err := f.service.Login(context.Background(), f.email, []byte("wrong password value"), now)
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("failed login error = %v, want invalid credentials", err)
	}
	return err
}

func TestLoginThrottleLocksAccountAndRejectsCorrectPassword(t *testing.T) {
	fixture := newThrottleTestFixture(t)
	for attempt := 0; attempt < loginThrottleMaxFailures; attempt++ {
		fixture.failLogin(t, serverAuthTestNow)
	}
	openedBefore := fixture.opened
	_, err := fixture.service.Login(context.Background(), fixture.email, fixture.password, serverAuthTestNow)
	retryAfter, throttled := LoginThrottledRetryAfter(err)
	if !throttled {
		t.Fatalf("correct password while locked error = %v, want throttle", err)
	}
	if retryAfter != loginThrottleBaseLock {
		t.Fatalf("retry after = %s, want %s", retryAfter, loginThrottleBaseLock)
	}
	if fixture.opened != openedBefore {
		t.Fatal("locked login opened a vault")
	}

	recovered := serverAuthTestNow.Add(loginThrottleBaseLock + time.Minute)
	if _, err := fixture.service.Login(context.Background(), fixture.email, fixture.password, recovered); err != nil {
		t.Fatalf("login after lock expiry: %v", err)
	}
	if fixture.opened != openedBefore+1 {
		t.Fatalf("vault opens after recovery = %d, want %d", fixture.opened, openedBefore+1)
	}
}

func TestLoginSuccessClearsThrottleCounter(t *testing.T) {
	fixture := newThrottleTestFixture(t)
	for attempt := 0; attempt < loginThrottleMaxFailures-1; attempt++ {
		fixture.failLogin(t, serverAuthTestNow)
	}
	if _, err := fixture.service.Login(context.Background(), fixture.email, fixture.password, serverAuthTestNow); err != nil {
		t.Fatalf("successful login: %v", err)
	}
	for attempt := 0; attempt < loginThrottleMaxFailures-1; attempt++ {
		if _, err := fixture.service.Login(context.Background(), fixture.email, []byte("wrong password value"), serverAuthTestNow); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("post-clear failure %d error = %v", attempt, err)
		}
	}
	if _, err := fixture.service.Login(context.Background(), fixture.email, fixture.password, serverAuthTestNow); err != nil {
		t.Fatalf("counter was not cleared by success: %v", err)
	}
}

func TestReauthenticationSharesLoginThrottleCounter(t *testing.T) {
	fixture := newThrottleTestFixture(t)
	for attempt := 0; attempt < loginThrottleMaxFailures; attempt++ {
		err := fixture.service.Reauthenticate(context.Background(), fixture.user.ID, []byte("wrong password value"), serverAuthTestNow)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("reauth failure %d error = %v", attempt, err)
		}
	}
	if _, err := fixture.service.Login(context.Background(), fixture.email, fixture.password, serverAuthTestNow); !isThrottled(err) {
		t.Fatalf("login after reauth failures error = %v, want throttle", err)
	}
	if err := fixture.service.Reauthenticate(context.Background(), fixture.user.ID, fixture.password, serverAuthTestNow); !isThrottled(err) {
		t.Fatalf("correct reauth while locked error = %v, want throttle", err)
	}

	recovered := serverAuthTestNow.Add(loginThrottleBaseLock + time.Minute)
	if err := fixture.service.Reauthenticate(context.Background(), fixture.user.ID, fixture.password, recovered); err != nil {
		t.Fatalf("reauth after lock expiry: %v", err)
	}
	for attempt := 0; attempt < loginThrottleMaxFailures-1; attempt++ {
		fixture.failLogin(t, recovered)
	}
	if _, err := fixture.service.Login(context.Background(), fixture.email, fixture.password, recovered); err != nil {
		t.Fatalf("reauth success did not clear the counter: %v", err)
	}
}

func isThrottled(err error) bool {
	_, ok := LoginThrottledRetryAfter(err)
	return ok
}

func TestChangePasswordSharesLoginThrottleCounter(t *testing.T) {
	fixture := newThrottleTestFixture(t)
	replacement := []byte("the replacement password")
	for attempt := 0; attempt < loginThrottleMaxFailures; attempt++ {
		if _, err := fixture.service.ChangePassword(context.Background(), fixture.user.ID, []byte("wrong password value"), replacement, false, serverAuthTestNow); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("change-password failure %d error = %v", attempt, err)
		}
	}
	if _, err := fixture.service.Login(context.Background(), fixture.email, fixture.password, serverAuthTestNow); !isThrottled(err) {
		t.Fatalf("login after change-password failures error = %v, want throttle", err)
	}
	if _, err := fixture.service.ChangePassword(context.Background(), fixture.user.ID, fixture.password, replacement, false, serverAuthTestNow); !isThrottled(err) {
		t.Fatalf("correct change-password while locked error = %v, want throttle", err)
	}
}

func TestPasskeyLoginBeginIsThrottledForEveryAddress(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		email string
	}{
		{name: "existing account", email: "person@example.test"},
		{name: "unknown account", email: "nobody@example.test"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := &privacyTestStore{user: control.UserSummary{ID: serverAuthTestUserID, Email: "person@example.test", State: control.UserActive}}
			service := privacyTestService(t, store)
			now := time.Now().UTC()
			key := LoginThrottleKey(testCase.email)
			for attempt := 0; attempt < loginThrottleMaxFailures; attempt++ {
				if _, err := store.RegisterLoginFailure(context.Background(), key, now, loginThrottlePolicy); err != nil {
					t.Fatal(err)
				}
			}
			_, err := service.BeginPasskeyLogin(context.Background(), testCase.email, "client-key")
			if !isThrottled(err) {
				t.Fatalf("begin error = %v, want throttle", err)
			}
		})
	}
}

func TestPasskeyLoginFinishFeedsTheSharedCounter(t *testing.T) {
	store := &privacyTestStore{user: control.UserSummary{ID: serverAuthTestUserID, Email: "person@example.test", State: control.UserActive}}
	service := privacyTestService(t, store)
	input := func(ceremonyID string) FinishPasskeyLoginInput {
		return FinishPasskeyLoginInput{
			CeremonyID:     ceremonyID,
			ClientKey:      "client-key",
			CredentialJSON: json.RawMessage(`{}`),
			PRFResult:      bytes.Repeat([]byte{9}, keyenvelope.PasskeySecretSize),
		}
	}
	for attempt := 0; attempt < loginThrottleMaxFailures; attempt++ {
		begin, err := service.BeginPasskeyLogin(context.Background(), store.user.Email, "client-key")
		if err != nil {
			t.Fatalf("begin %d: %v", attempt, err)
		}
		if _, err := service.FinishPasskeyLogin(context.Background(), input(begin.CeremonyID), time.Now().UTC()); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("finish %d error = %v", attempt, err)
		}
	}
	if _, err := service.BeginPasskeyLogin(context.Background(), store.user.Email, "client-key"); !isThrottled(err) {
		t.Fatalf("begin after passkey failures error = %v, want throttle", err)
	}
}
