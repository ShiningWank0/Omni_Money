package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newClockedSessionManager(t *testing.T, config SessionConfig, initial time.Time) (*SessionManager, *time.Time) {
	t.Helper()
	manager := NewSessionManagerWithConfig(config)
	clock := initial
	manager.now = func() time.Time { return clock }
	t.Cleanup(manager.Close)
	return manager, &clock
}

func securityTestSessionConfig() SessionConfig {
	return SessionConfig{
		MaxAge:        time.Hour,
		IdleTimeout:   10 * time.Minute,
		RecentAuthAge: 5 * time.Minute,
		MaxConcurrent: 2,
	}
}

func TestDefaultSessionConfigUsesFifteenMinuteIdleTimeout(t *testing.T) {
	cfg := DefaultSessionConfig()
	if cfg.MaxAge != 8*time.Hour || cfg.IdleTimeout != 15*time.Minute ||
		cfg.RecentAuthAge != 5*time.Minute || cfg.MaxConcurrent != 3 {
		t.Fatalf("unexpected secure defaults: %+v", cfg)
	}
}

func TestIdleTimeoutSecondsGetterIsBoundedAndSafe(t *testing.T) {
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), time.Now())
	if got := manager.IdleTimeoutSeconds(); got != 600 {
		t.Fatalf("IdleTimeoutSeconds() = %d, want 600", got)
	}
	var nilManager *SessionManager
	if got := nilManager.IdleTimeoutSeconds(); got != 0 {
		t.Fatalf("nil IdleTimeoutSeconds() = %d, want 0", got)
	}
}

func TestSessionAbsoluteAndIdleExpiryBoundaries(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)

	t.Run("idle timeout is inclusive", func(t *testing.T) {
		manager, clock := newClockedSessionManager(t, securityTestSessionConfig(), start)
		session, err := manager.CreateSession("user")
		if err != nil {
			t.Fatal(err)
		}
		*clock = start.Add(10 * time.Minute)
		if _, ok := manager.GetSession(session.ID); ok {
			t.Fatal("session survived the exact idle-timeout boundary")
		}
	})

	t.Run("activity does not extend absolute lifetime", func(t *testing.T) {
		manager, clock := newClockedSessionManager(t, securityTestSessionConfig(), start)
		session, err := manager.CreateSession("user")
		if err != nil {
			t.Fatal(err)
		}
		originalExpiry := session.ExpiresAt
		for _, elapsed := range []time.Duration{9 * time.Minute, 18 * time.Minute, 27 * time.Minute, 36 * time.Minute, 45 * time.Minute, 54 * time.Minute} {
			*clock = start.Add(elapsed)
			got, ok := manager.GetSession(session.ID)
			if !ok {
				t.Fatalf("active session expired after %s", elapsed)
			}
			if !got.ExpiresAt.Equal(originalExpiry) {
				t.Fatalf("touch extended absolute expiry: got %s want %s", got.ExpiresAt, originalExpiry)
			}
		}
		*clock = originalExpiry
		if _, ok := manager.GetSession(session.ID); ok {
			t.Fatal("session survived the exact absolute-expiry boundary")
		}
	})
}

func TestSessionMaxConcurrentEvictsOldestPerUser(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, clock := newClockedSessionManager(t, securityTestSessionConfig(), start)

	first, err := manager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}
	*clock = start.Add(time.Second)
	second, err := manager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}
	*clock = start.Add(2 * time.Second)
	otherUser, err := manager.CreateSession("other")
	if err != nil {
		t.Fatal(err)
	}
	*clock = start.Add(3 * time.Second)
	third, err := manager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := manager.GetSession(first.ID); ok {
		t.Fatal("oldest same-user session was not evicted")
	}
	for label, id := range map[string]string{
		"second same-user session": second.ID,
		"new same-user session":    third.ID,
		"different-user session":   otherUser.ID,
	} {
		if _, ok := manager.GetSession(id); !ok {
			t.Fatalf("%s was unexpectedly evicted", label)
		}
	}
}

func TestSessionConcurrentCreationNeverExceedsConfiguredMaximum(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), start)

	const workers = 32
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			if _, err := manager.CreateSession("user"); err != nil {
				t.Errorf("CreateSession failed: %v", err)
			}
		}()
	}
	wait.Wait()

	manager.mu.Lock()
	defer manager.mu.Unlock()
	count := 0
	for _, record := range manager.sessions {
		if record.session.Username == "user" {
			count++
		}
	}
	if count != manager.config.MaxConcurrent {
		t.Fatalf("concurrent session count=%d, want %d", count, manager.config.MaxConcurrent)
	}
}

func TestSessionRotationInvalidatesOldIDAndCSRFWithoutExtendingLifetime(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, clock := newClockedSessionManager(t, securityTestSessionConfig(), start)
	session, err := manager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}
	oldID := session.ID
	oldCSRF := session.CSRFToken
	originalExpiry := session.ExpiresAt

	*clock = start.Add(4 * time.Minute)
	rotated, err := manager.RotateAfterReauthentication(oldID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ID == oldID || rotated.CSRFToken == oldCSRF {
		t.Fatal("reauthentication did not rotate both session ID and CSRF token")
	}
	if !rotated.ExpiresAt.Equal(originalExpiry) {
		t.Fatalf("rotation extended absolute expiry: got %s want %s", rotated.ExpiresAt, originalExpiry)
	}
	if _, ok := manager.GetSession(oldID); ok {
		t.Fatal("old session ID remained valid after rotation")
	}
	if manager.ValidateCSRF(oldID, oldCSRF) {
		t.Fatal("old session ID and CSRF token remained valid after rotation")
	}
	if manager.ValidateCSRF(rotated.ID, oldCSRF) {
		t.Fatal("old CSRF token remained valid on rotated session")
	}
	if !manager.ValidateCSRF(rotated.ID, rotated.CSRFToken) {
		t.Fatal("rotated CSRF token was rejected")
	}
	if !manager.IsRecent(rotated.ID) {
		t.Fatal("rotated session was not marked recently authenticated")
	}
}

func TestDeleteAllSessionsIsUserScopedAndCannotBeResurrected(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), start)
	first, _ := manager.CreateSession("user")
	second, _ := manager.CreateSession("user")
	other, _ := manager.CreateSession("other")

	if deleted := manager.DeleteAllSessions("user"); deleted != 2 {
		t.Fatalf("deleted=%d, want 2", deleted)
	}
	for _, id := range []string{first.ID, second.ID} {
		if _, ok := manager.GetSession(id); ok {
			t.Fatalf("deleted session %q was resurrected", id)
		}
	}
	if _, ok := manager.GetSession(other.ID); !ok {
		t.Fatal("another user's session was deleted")
	}
}

func TestSessionRequestRejectsDuplicateAndShadowCookies(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), start)
	session, err := manager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		target  string
		cookies []*http.Cookie
		wantOK  bool
	}{
		{name: "one HTTP cookie", target: "http://money.example/api/accounts", cookies: []*http.Cookie{{Name: SessionCookieName, Value: session.ID}}, wantOK: true},
		{name: "duplicate HTTP cookie", target: "http://money.example/api/accounts", cookies: []*http.Cookie{{Name: SessionCookieName, Value: session.ID}, {Name: SessionCookieName, Value: session.ID}}},
		{name: "secure cookie shadows HTTP cookie", target: "http://money.example/api/accounts", cookies: []*http.Cookie{{Name: SessionCookieName, Value: session.ID}, {Name: SecureSessionCookieName, Value: session.ID}}},
		{name: "one HTTPS host cookie", target: "https://money.example/api/accounts", cookies: []*http.Cookie{{Name: SecureSessionCookieName, Value: session.ID}}, wantOK: true},
		{name: "duplicate HTTPS host cookie", target: "https://money.example/api/accounts", cookies: []*http.Cookie{{Name: SecureSessionCookieName, Value: session.ID}, {Name: SecureSessionCookieName, Value: session.ID}}},
		{name: "legacy cookie shadows host cookie", target: "https://money.example/api/accounts", cookies: []*http.Cookie{{Name: SecureSessionCookieName, Value: session.ID}, {Name: SessionCookieName, Value: session.ID}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			for _, cookie := range test.cookies {
				request.AddCookie(cookie)
			}
			_, ok := manager.GetSessionFromRequest(request)
			if ok != test.wantOK {
				t.Fatalf("GetSessionFromRequest ok=%v, want %v", ok, test.wantOK)
			}
		})
	}
}

func TestHTTPSCookieUsesHostPrefixAndRequiredAttributes(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), start)
	session, err := manager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://money.example/api/accounts", nil)
	recorder := httptest.NewRecorder()
	manager.SetSessionCookie(recorder, request, session)

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Set-Cookie count=%d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != SecureSessionCookieName || !cookie.Secure || !cookie.HttpOnly ||
		cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("insecure __Host cookie attributes: %#v", cookie)
	}
}

func TestTLSOffloadStillUsesSecureHostCookie(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), start)
	session, err := manager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://money.example/api/accounts", nil)
	request = request.WithContext(context.WithValue(request.Context(), requestProtoKey, "https"))
	recorder := httptest.NewRecorder()
	manager.SetSessionCookie(recorder, request, session)

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SecureSessionCookieName || !cookies[0].Secure {
		t.Fatalf("TLS-offloaded request did not receive secure __Host cookie: %#v", cookies)
	}
}

func TestCanonicalSessionSecretValidation(t *testing.T) {
	valid := strings.Repeat("0123456789abcdef", 4)
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "canonical lowercase hex", value: valid, want: true},
		{name: "empty", value: ""},
		{name: "too short", value: valid[:63]},
		{name: "too long", value: valid + "0"},
		{name: "uppercase", value: strings.ToUpper(valid)},
		{name: "leading whitespace", value: " " + valid[:63]},
		{name: "trailing whitespace", value: valid[:63] + " "},
		{name: "non hex ASCII", value: valid[:63] + "g"},
		{name: "multibyte replacement", value: valid[:62] + "é"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isCanonicalSessionSecret(test.value); got != test.want {
				t.Fatalf("isCanonicalSessionSecret(%q)=%v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestSessionManagerRejectsNonCanonicalSecretRepresentations(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), start)
	session, err := manager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}
	if !isCanonicalSessionSecret(session.ID) || !isCanonicalSessionSecret(session.CSRFToken) {
		t.Fatalf("generated session secrets are not canonical: id=%q csrf=%q", session.ID, session.CSRFToken)
	}

	uppercaseID := strings.ToUpper(session.ID)
	uppercaseCSRF := strings.ToUpper(session.CSRFToken)
	if _, ok := manager.GetSession(uppercaseID); ok {
		t.Fatal("uppercase session ID was accepted")
	}
	if _, err := manager.RotateAfterReauthentication(uppercaseID); err == nil {
		t.Fatal("uppercase session ID was accepted for rotation")
	}
	if manager.ValidateCSRF(session.ID, uppercaseCSRF) {
		t.Fatal("uppercase CSRF representation was accepted")
	}
	if manager.IsRecent(uppercaseID) {
		t.Fatal("uppercase session ID was accepted for recent-auth")
	}

	request := httptest.NewRequest(http.MethodGet, "https://money.example/api/accounts", nil)
	recorder := httptest.NewRecorder()
	malformed := *session
	malformed.ID = uppercaseID
	manager.SetSessionCookie(recorder, request, &malformed)
	if got := len(recorder.Result().Cookies()); got != 0 {
		t.Fatalf("noncanonical session ID was emitted in %d cookie(s)", got)
	}
}

func TestSessionConfigFromEnvRejectsInvalidAndOverflowValues(t *testing.T) {
	environmentNames := []string{
		"SESSION_MAX_AGE_HOURS",
		"SESSION_IDLE_TIMEOUT_MINUTES",
		"SESSION_REAUTH_MAX_AGE_MINUTES",
		"SESSION_MAX_CONCURRENT",
	}
	for _, name := range environmentNames {
		t.Setenv(name, "")
	}

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "max age nonnumeric", key: "SESSION_MAX_AGE_HOURS", value: "eight"},
		{name: "max age zero", key: "SESSION_MAX_AGE_HOURS", value: "0"},
		{name: "max age over ceiling", key: "SESSION_MAX_AGE_HOURS", value: "169"},
		{name: "max age integer overflow", key: "SESSION_MAX_AGE_HOURS", value: "999999999999999999999"},
		{name: "idle under floor", key: "SESSION_IDLE_TIMEOUT_MINUTES", value: "4"},
		{name: "idle over ceiling", key: "SESSION_IDLE_TIMEOUT_MINUTES", value: "121"},
		{name: "recent under floor", key: "SESSION_REAUTH_MAX_AGE_MINUTES", value: "0"},
		{name: "recent over ceiling", key: "SESSION_REAUTH_MAX_AGE_MINUTES", value: "31"},
		{name: "concurrent zero", key: "SESSION_MAX_CONCURRENT", value: "0"},
		{name: "concurrent over ceiling", key: "SESSION_MAX_CONCURRENT", value: "11"},
		{name: "concurrent overflow", key: "SESSION_MAX_CONCURRENT", value: "999999999999999999999"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, name := range environmentNames {
				t.Setenv(name, "")
			}
			t.Setenv(test.key, test.value)
			if _, err := SessionConfigFromEnv(); err == nil {
				t.Fatalf("%s=%q was accepted", test.key, test.value)
			}
		})
	}

	t.Run("idle exceeds absolute", func(t *testing.T) {
		for _, name := range environmentNames {
			t.Setenv(name, "")
		}
		t.Setenv("SESSION_MAX_AGE_HOURS", "1")
		t.Setenv("SESSION_IDLE_TIMEOUT_MINUTES", "120")
		if _, err := SessionConfigFromEnv(); err == nil {
			t.Fatal("idle timeout exceeding absolute lifetime was accepted")
		}
	})

	t.Run("recent exceeds idle", func(t *testing.T) {
		for _, name := range environmentNames {
			t.Setenv(name, "")
		}
		t.Setenv("SESSION_IDLE_TIMEOUT_MINUTES", "5")
		t.Setenv("SESSION_REAUTH_MAX_AGE_MINUTES", "6")
		if _, err := SessionConfigFromEnv(); err == nil {
			t.Fatal("recent-auth age exceeding idle timeout was accepted")
		}
	})
}

func TestSessionConfigFromEnvAcceptsBoundedValues(t *testing.T) {
	t.Setenv("SESSION_MAX_AGE_HOURS", "24")
	t.Setenv("SESSION_IDLE_TIMEOUT_MINUTES", "60")
	t.Setenv("SESSION_REAUTH_MAX_AGE_MINUTES", "10")
	t.Setenv("SESSION_MAX_CONCURRENT", "4")
	cfg, err := SessionConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxAge != 24*time.Hour || cfg.IdleTimeout != time.Hour ||
		cfg.RecentAuthAge != 10*time.Minute || cfg.MaxConcurrent != 4 {
		t.Fatalf("unexpected session config: %+v", cfg)
	}
}

func TestGetAndDeleteRaceCannotResurrectSession(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), start)

	for iteration := range 200 {
		session, err := manager.CreateSession(fmt.Sprintf("race-user-%d", iteration))
		if err != nil {
			t.Fatal(err)
		}
		startRace := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-startRace
			_, _ = manager.GetSession(session.ID)
		}()
		go func() {
			defer wait.Done()
			<-startRace
			manager.DeleteSession(session.ID)
		}()
		close(startRace)
		wait.Wait()
		if _, ok := manager.GetSession(session.ID); ok {
			t.Fatalf("iteration %d: deleted session was resurrected", iteration)
		}
	}
}

func TestConcurrentRotationOfOneSessionHasSingleWinner(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), start)
	session, err := manager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	startRace := make(chan struct{})
	results := make(chan *Session, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			<-startRace
			rotated, rotateErr := manager.RotateAfterReauthentication(session.ID)
			if rotateErr == nil {
				results <- rotated
				return
			}
			results <- nil
		}()
	}
	close(startRace)
	wait.Wait()
	close(results)

	winners := 0
	var winner *Session
	for result := range results {
		if result != nil {
			winners++
			winner = result
		}
	}
	if winners != 1 {
		t.Fatalf("rotation winners=%d, want 1", winners)
	}
	if _, ok := manager.GetSession(session.ID); ok {
		t.Fatal("old session survived concurrent rotation")
	}
	if winner == nil {
		t.Fatal("rotation winner is nil")
	}
	if _, ok := manager.GetSession(winner.ID); !ok {
		t.Fatal("winning rotated session is invalid")
	}
}

func TestGetDeleteAndRotateRaceIsLinearizable(t *testing.T) {
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), start)

	for iteration := range 200 {
		session, err := manager.CreateSession(fmt.Sprintf("three-way-race-%d", iteration))
		if err != nil {
			t.Fatal(err)
		}
		startRace := make(chan struct{})
		rotatedResult := make(chan *Session, 1)
		var wait sync.WaitGroup
		wait.Add(3)
		go func() {
			defer wait.Done()
			<-startRace
			_, _ = manager.GetSession(session.ID)
		}()
		go func() {
			defer wait.Done()
			<-startRace
			manager.DeleteSession(session.ID)
		}()
		go func() {
			defer wait.Done()
			<-startRace
			rotated, rotateErr := manager.RotateAfterReauthentication(session.ID)
			if rotateErr != nil {
				rotatedResult <- nil
				return
			}
			rotatedResult <- rotated
		}()
		close(startRace)
		wait.Wait()
		rotated := <-rotatedResult

		if _, ok := manager.GetSession(session.ID); ok {
			t.Fatalf("iteration %d: old session survived get/delete/rotate race", iteration)
		}
		if manager.ValidateCSRF(session.ID, session.CSRFToken) {
			t.Fatalf("iteration %d: old CSRF pair survived get/delete/rotate race", iteration)
		}
		if rotated != nil {
			if _, ok := manager.GetSession(rotated.ID); !ok {
				t.Fatalf("iteration %d: successful rotation produced no valid new session", iteration)
			}
			manager.DeleteSession(rotated.ID)
		}
	}
}
