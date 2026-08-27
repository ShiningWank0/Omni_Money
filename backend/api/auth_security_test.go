package api

import (
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- RFC 6238 requires HMAC-SHA-1.
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"omni_money/backend/middleware"
)

func prepareAuthSecurityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AUTH_PASSWORD_HASH", testPasswordHash)
	t.Setenv("AUTH_REQUIRE_TOTP", "false")
	t.Setenv("AUTH_TOTP_SECRET_FILE", "")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("FORCE_HTTPS", "false")
	t.Setenv("HTTPS_REDIRECT_HOST", "")
	t.Setenv("ALLOWED_HOSTS", "example.com")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
}

func securityJSONRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func serveSecurityRequest(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestAuthLoginRejectsNonCanonicalJSON(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
	}{
		{name: "unknown field", body: `{"password":"test-password","unexpected":true}`, code: http.StatusBadRequest},
		{name: "case variant", body: `{"Password":"test-password"}`, code: http.StatusBadRequest},
		{name: "case alias collision", body: `{"password":"wrong","Password":"test-password"}`, code: http.StatusBadRequest},
		{name: "duplicate password", body: `{"password":"test-password","password":"test-password"}`, code: http.StatusBadRequest},
		{name: "trailing value", body: `{"password":"test-password"}{}`, code: http.StatusBadRequest},
		{name: "oversized body", body: `{"password":"` + strings.Repeat("a", maxAuthRequestBody) + `"}`, code: http.StatusRequestEntityTooLarge},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepareAuthSecurityEnv(t)
			handler := NewRouter()
			req := securityJSONRequest(http.MethodPost, "/api/auth/login", tc.body)
			req.RemoteAddr = "198.51.100." + string(rune('1'+i)) + ":12345"
			recorder := serveSecurityRequest(handler, req)
			if recorder.Code != tc.code {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tc.code, recorder.Body.String())
			}
		})
	}
}

func TestTOTPIsOptionalWhenNoSecretIsConfigured(t *testing.T) {
	prepareAuthSecurityEnv(t)
	handler, err := NewRouterWithError()
	if err != nil {
		t.Fatalf("NewRouterWithError without TOTP: %v", err)
	}

	status := serveSecurityRequest(handler, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", status.Code, http.StatusOK)
	}
	var statusBody struct {
		TOTPRequired bool `json:"totp_required"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &statusBody); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if statusBody.TOTPRequired {
		t.Fatal("TOTP was reported as required without a configured secret")
	}

	login := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password"}`)
	login.RemoteAddr = "198.51.100.20:12345"
	response := serveSecurityRequest(handler, login)
	if response.Code != http.StatusOK {
		t.Fatalf("password-only login status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	statusRequest.AddCookie(securitySessionCookie(t, response))
	statusResponse := serveSecurityRequest(handler, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, want %d; body=%s", statusResponse.Code, http.StatusOK, statusResponse.Body.String())
	}
	var authenticatedStatus struct {
		IdleTimeoutSeconds int64 `json:"idle_timeout_seconds"`
	}
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &authenticatedStatus); err != nil {
		t.Fatalf("decode authenticated status: %v", err)
	}
	if authenticatedStatus.IdleTimeoutSeconds != int64((15*time.Minute)/time.Second) {
		t.Fatalf("idle_timeout_seconds = %d, want 900", authenticatedStatus.IdleTimeoutSeconds)
	}
}

func TestAuthKeepaliveRequiresSessionAndCSRFAndReturnsNoContent(t *testing.T) {
	prepareAuthSecurityEnv(t)
	handler, err := NewRouterWithError()
	if err != nil {
		t.Fatal(err)
	}

	unauthenticated := securityJSONRequest(http.MethodPost, "/api/auth/keepalive", ``)
	unauthenticated.RemoteAddr = "198.51.100.60:12345"
	if response := serveSecurityRequest(handler, unauthenticated); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated keepalive status=%d, want %d", response.Code, http.StatusUnauthorized)
	}

	login := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password"}`)
	login.RemoteAddr = unauthenticated.RemoteAddr
	loginResponse := serveSecurityRequest(handler, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d; body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := securitySessionCookie(t, loginResponse)
	csrfToken := securityCSRFToken(t, loginResponse)

	missingToken := securityJSONRequest(http.MethodPost, "/api/auth/keepalive", ``)
	missingToken.RemoteAddr = login.RemoteAddr
	missingToken.AddCookie(cookie)
	if response := serveSecurityRequest(handler, missingToken); response.Code != http.StatusForbidden {
		t.Fatalf("keepalive without CSRF status=%d, want %d", response.Code, http.StatusForbidden)
	}

	keepalive := securityJSONRequest(http.MethodPost, "/api/auth/keepalive", ``)
	keepalive.RemoteAddr = login.RemoteAddr
	keepalive.AddCookie(cookie)
	keepalive.Header.Set("X-CSRF-Token", csrfToken)
	response := serveSecurityRequest(handler, keepalive)
	if response.Code != http.StatusNoContent {
		t.Fatalf("keepalive status=%d, want %d", response.Code, http.StatusNoContent)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("keepalive body=%q, want empty", response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("keepalive Cache-Control=%q, want no-store", got)
	}

	wrongMethod := httptest.NewRequest(http.MethodGet, "/api/auth/keepalive", nil)
	wrongMethod.RemoteAddr = login.RemoteAddr
	wrongMethod.AddCookie(cookie)
	if response := serveSecurityRequest(handler, wrongMethod); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET keepalive status=%d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

func TestReauthenticationRotationFailureRequiresFullLoginAndClearsCookies(t *testing.T) {
	sessionManager := middleware.NewSessionManager(time.Hour)
	t.Cleanup(sessionManager.Close)
	authManager := middleware.NewAuthSessionManager(sessionManager, testPasswordHash, nil)
	session, err := sessionManager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}

	// SessionAuth and CSRF must both succeed first. Deleting the server-side
	// record in this inner handler deterministically reproduces expiry or a
	// logout-all race while bcrypt/reauthentication is in progress.
	reauthenticate := handleAuthReauthenticate(authManager)
	invalidateAfterSecurityChecks := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionManager.DeleteSession(session.ID)
		reauthenticate.ServeHTTP(w, r)
	})
	handler := middleware.SessionAuthMiddleware(
		sessionManager,
		middleware.CSRFMiddleware(sessionManager, invalidateAfterSecurityChecks),
	)

	request := securityJSONRequest(http.MethodPost, "/api/auth/reauth", `{"password":"test-password"}`)
	request.RemoteAddr = "198.51.100.61:12345"
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: session.ID, Path: "/"})
	request.Header.Set(middleware.CSRFHeaderName, session.CSRFToken)
	response := serveSecurityRequest(handler, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("rotation failure status=%d, want %d; body=%s", response.Code, http.StatusUnauthorized, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("rotation failure Cache-Control=%q, want no-store", got)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode rotation failure body: %v", err)
	}
	if body["login_required"] != true {
		t.Fatalf("rotation failure body=%v, want login_required=true", body)
	}

	cleared := map[string]bool{}
	for _, cookie := range response.Result().Cookies() {
		if (cookie.Name == middleware.SessionCookieName || cookie.Name == middleware.SecureSessionCookieName) &&
			cookie.Value == "" && cookie.MaxAge < 0 {
			cleared[cookie.Name] = true
		}
	}
	for _, name := range []string{middleware.SessionCookieName, middleware.SecureSessionCookieName} {
		if !cleared[name] {
			t.Fatalf("rotation failure did not clear cookie %q; cookies=%v", name, response.Result().Cookies())
		}
	}
}

func TestProductionRouterDoesNotDispatchHighImpactTrailingSlashLookalikes(t *testing.T) {
	prepareAuthSecurityEnv(t)
	handler, err := NewRouterWithError()
	if err != nil {
		t.Fatal(err)
	}

	login := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password"}`)
	login.RemoteAddr = "198.51.100.62:12345"
	loginResponse := serveSecurityRequest(handler, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d; body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := securitySessionCookie(t, loginResponse)
	csrfToken := securityCSRFToken(t, loginResponse)

	// A fresh session would be allowed through RecentAuthMiddleware, so these
	// production-router responses prove the lookalikes reached the FileServer
	// fallback instead of any high-impact handler. Include every protected path,
	// not only the six routes that have a distinct leaf name.
	for _, target := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/backup_csv/", http.StatusNotFound},
		{http.MethodPost, "/api/import_csv/", http.StatusNotFound},
		{http.MethodPost, "/api/snapshots/", http.StatusNotFound},
		{http.MethodPost, "/api/snapshots/restore/", http.StatusNotFound},
		{http.MethodPost, "/api/auth/logout-all/", http.StatusNotFound},
		{http.MethodPost, "/api/ai-console/transactions/", http.StatusNotFound},
		{http.MethodPost, "/api/ai-console/analysis/", http.StatusNotFound},
	} {
		t.Run(target.method+" "+target.path, func(t *testing.T) {
			request := securityJSONRequest(target.method, target.path, `{}`)
			request.RemoteAddr = login.RemoteAddr
			request.AddCookie(cookie)
			if target.method != http.MethodGet {
				request.Header.Set("X-CSRF-Token", csrfToken)
			}
			response := serveSecurityRequest(handler, request)
			if response.Code != target.want {
				t.Fatalf("lookalike status=%d, want %d; body=%s", response.Code, target.want, response.Body.String())
			}
		})
	}

	// In particular, the logout-all lookalike must not invalidate the session.
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	statusRequest.RemoteAddr = login.RemoteAddr
	statusRequest.AddCookie(cookie)
	statusResponse := serveSecurityRequest(handler, statusRequest)
	var statusBody struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &statusBody); err != nil {
		t.Fatalf("decode post-lookalike status: %v", err)
	}
	if statusResponse.Code != http.StatusOK || !statusBody.Authenticated {
		t.Fatalf("lookalike invalidated session: status=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
}

func TestAuthLoginWrongPasswordAndWrongTOTPHaveSamePublicResponse(t *testing.T) {
	prepareAuthSecurityEnv(t)
	secretPath := writeSecurityTOTPSecret(t, []byte("12345678901234567890"), 0o600)
	t.Setenv("AUTH_REQUIRE_TOTP", "true")
	t.Setenv("AUTH_TOTP_SECRET_FILE", secretPath)
	handler := NewRouter()
	validCode := securityTOTPCode([]byte("12345678901234567890"), time.Now().UTC())
	wrongCode := "0" + validCode[1:]
	if validCode[0] == '0' {
		wrongCode = "1" + validCode[1:]
	}

	wrongPassword := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"wrong-password","totp_code":"`+wrongCode+`"}`)
	wrongPassword.RemoteAddr = "198.51.100.21:12345"
	first := serveSecurityRequest(handler, wrongPassword)
	wrongTOTP := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password","totp_code":"`+wrongCode+`"}`)
	wrongTOTP.RemoteAddr = "198.51.100.22:12345"
	second := serveSecurityRequest(handler, wrongTOTP)

	if first.Code != http.StatusUnauthorized || second.Code != http.StatusUnauthorized {
		t.Fatalf("statuses = %d and %d, want both %d", first.Code, second.Code, http.StatusUnauthorized)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("public auth responses differ: %q vs %q", first.Body.String(), second.Body.String())
	}
}

func TestAuthLoginWithIndependentTOTPAndReplayProtection(t *testing.T) {
	prepareAuthSecurityEnv(t)
	secret := []byte("12345678901234567890")
	secretPath := writeSecurityTOTPSecret(t, secret, 0o600)
	t.Setenv("AUTH_REQUIRE_TOTP", "true")
	t.Setenv("AUTH_TOTP_SECRET_FILE", secretPath)
	handler := NewRouter()

	code := securityTOTPCode(secret, time.Now().UTC())
	login := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password","totp_code":"`+code+`"}`)
	login.RemoteAddr = "198.51.100.23:12345"
	first := serveSecurityRequest(handler, login)
	if first.Code != http.StatusOK {
		t.Fatalf("valid TOTP login status = %d, want %d; body=%s", first.Code, http.StatusOK, first.Body.String())
	}

	replay := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password","totp_code":"`+code+`"}`)
	replay.RemoteAddr = "198.51.100.24:12345"
	second := serveSecurityRequest(handler, replay)
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("replayed TOTP status = %d, want %d", second.Code, http.StatusUnauthorized)
	}
}

func TestAuthReauthenticationRotatesSessionAndCSRFBeforeDangerousRoute(t *testing.T) {
	prepareAuthSecurityEnv(t)
	handler := NewRouter()

	login := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password"}`)
	login.RemoteAddr = "198.51.100.30:12345"
	loginResponse := serveSecurityRequest(handler, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d; body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	oldCookie := securitySessionCookie(t, loginResponse)
	oldCSRF := securityCSRFToken(t, loginResponse)

	reauth := securityJSONRequest(http.MethodPost, "/api/auth/reauth", `{"password":"test-password"}`)
	reauth.RemoteAddr = login.RemoteAddr
	reauth.AddCookie(oldCookie)
	reauth.Header.Set("X-CSRF-Token", oldCSRF)
	reauthResponse := serveSecurityRequest(handler, reauth)
	if reauthResponse.Code != http.StatusOK {
		t.Fatalf("reauth status = %d; body=%s", reauthResponse.Code, reauthResponse.Body.String())
	}
	newCookie := securitySessionCookie(t, reauthResponse)
	newCSRF := securityCSRFToken(t, reauthResponse)
	if newCookie.Value == oldCookie.Value {
		t.Fatal("reauthentication did not rotate the session cookie")
	}
	if newCSRF == oldCSRF {
		t.Fatal("reauthentication did not rotate the CSRF token")
	}

	oldSessionRequest := securityJSONRequest(http.MethodPost, "/api/auth/logout-all", `{}`)
	oldSessionRequest.RemoteAddr = login.RemoteAddr
	oldSessionRequest.AddCookie(oldCookie)
	oldSessionRequest.Header.Set("X-CSRF-Token", oldCSRF)
	oldSessionResponse := serveSecurityRequest(handler, oldSessionRequest)
	if oldSessionResponse.Code != http.StatusUnauthorized {
		t.Fatalf("old session dangerous route status = %d, want %d", oldSessionResponse.Code, http.StatusUnauthorized)
	}

	oldTokenRequest := securityJSONRequest(http.MethodPost, "/api/auth/logout-all", `{}`)
	oldTokenRequest.RemoteAddr = login.RemoteAddr
	oldTokenRequest.AddCookie(newCookie)
	oldTokenRequest.Header.Set("X-CSRF-Token", oldCSRF)
	oldTokenResponse := serveSecurityRequest(handler, oldTokenRequest)
	if oldTokenResponse.Code != http.StatusForbidden {
		t.Fatalf("old CSRF token dangerous route status = %d, want %d", oldTokenResponse.Code, http.StatusForbidden)
	}

	newRequest := securityJSONRequest(http.MethodPost, "/api/auth/logout-all", `{}`)
	newRequest.RemoteAddr = login.RemoteAddr
	newRequest.AddCookie(newCookie)
	newRequest.Header.Set("X-CSRF-Token", newCSRF)
	newResponse := serveSecurityRequest(handler, newRequest)
	if newResponse.Code != http.StatusOK {
		t.Fatalf("new session dangerous route status = %d, want %d; body=%s", newResponse.Code, http.StatusOK, newResponse.Body.String())
	}
}

func TestNewRouterFailsForWeakBcryptCost(t *testing.T) {
	prepareAuthSecurityEnv(t)
	weak, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate weak bcrypt hash: %v", err)
	}
	t.Setenv("AUTH_PASSWORD_HASH", string(weak))
	if _, err := NewRouterWithError(); err == nil {
		t.Fatal("NewRouterWithError accepted bcrypt cost below the security minimum")
	}
	if got := NewRouter(); got == nil {
		t.Fatal("NewRouter returned nil handler on invalid configuration")
	}
}

func TestNewRouterFailsForWeakMissingOrSymlinkedTOTPSecret(t *testing.T) {
	prepareAuthSecurityEnv(t)
	t.Setenv("AUTH_REQUIRE_TOTP", "true")
	if _, err := NewRouterWithError(); err == nil {
		t.Fatal("missing required TOTP secret was accepted")
	}

	weakPath := writeSecurityTOTPSecret(t, []byte("short"), 0o600)
	t.Setenv("AUTH_TOTP_SECRET_FILE", weakPath)
	if _, err := NewRouterWithError(); err == nil {
		t.Fatal("weak TOTP secret was accepted")
	}

	validTarget := writeSecurityTOTPSecret(t, []byte("12345678901234567890"), 0o600)
	symlinkPath := filepath.Join(t.TempDir(), "totp-link")
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links require elevated privileges on Windows")
	}
	if err := os.Symlink(validTarget, symlinkPath); err != nil {
		t.Fatalf("create TOTP symlink: %v", err)
	}
	t.Setenv("AUTH_TOTP_SECRET_FILE", symlinkPath)
	if _, err := NewRouterWithError(); err == nil {
		t.Fatal("symlinked TOTP secret was accepted")
	}
}

func TestNewRouterFailsForWorldReadableTOTPSecret(t *testing.T) {
	prepareAuthSecurityEnv(t)
	path := writeSecurityTOTPSecret(t, []byte("12345678901234567890"), 0o644)
	t.Setenv("AUTH_REQUIRE_TOTP", "true")
	t.Setenv("AUTH_TOTP_SECRET_FILE", path)
	if _, err := NewRouterWithError(); err == nil {
		t.Fatal("world-readable TOTP secret was accepted")
	}
}

func TestTOTPSecretEnablesSecondFactorEvenWithoutRequireAssertion(t *testing.T) {
	prepareAuthSecurityEnv(t)
	secret := []byte("12345678901234567890")
	t.Setenv("AUTH_TOTP_SECRET_FILE", writeSecurityTOTPSecret(t, secret, 0o600))
	t.Setenv("AUTH_REQUIRE_TOTP", "false")

	handler, err := NewRouterWithError()
	if err != nil {
		t.Fatalf("NewRouterWithError: %v", err)
	}
	withoutCode := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password"}`)
	withoutCode.RemoteAddr = "198.51.100.41:12345"
	if response := serveSecurityRequest(handler, withoutCode); response.Code != http.StatusUnauthorized {
		t.Fatalf("login without TOTP status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	code := securityTOTPCode(secret, time.Now().UTC())
	withCode := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password","totp_code":"`+code+`"}`)
	withCode.RemoteAddr = "198.51.100.42:12345"
	if response := serveSecurityRequest(handler, withCode); response.Code != http.StatusOK {
		t.Fatalf("login with auto-enabled TOTP status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
}

func TestInvalidConfiguredTOTPSecretFailsEvenWhenRequireAssertionIsFalse(t *testing.T) {
	prepareAuthSecurityEnv(t)
	t.Setenv("AUTH_REQUIRE_TOTP", "false")
	t.Setenv("AUTH_TOTP_SECRET_FILE", filepath.Join(t.TempDir(), "missing-totp-secret"))
	if _, err := NewRouterWithError(); err == nil {
		t.Fatal("configured but missing TOTP secret silently downgraded to password-only")
	}

	malformedPath := filepath.Join(t.TempDir(), "malformed-totp-secret")
	if err := os.WriteFile(malformedPath, []byte("not-base32\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(malformedPath, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTH_TOTP_SECRET_FILE", malformedPath)
	if _, err := NewRouterWithError(); err == nil {
		t.Fatal("configured but malformed TOTP secret silently downgraded to password-only")
	}
}

func TestConfiguredTOTPIsRequiredForLoginButNotReauthentication(t *testing.T) {
	prepareAuthSecurityEnv(t)
	secret := []byte("12345678901234567890")
	t.Setenv("AUTH_TOTP_SECRET_FILE", writeSecurityTOTPSecret(t, secret, 0o600))
	t.Setenv("AUTH_REQUIRE_TOTP", "false")
	handler, err := NewRouterWithError()
	if err != nil {
		t.Fatal(err)
	}

	withoutTOTP := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password"}`)
	withoutTOTP.RemoteAddr = "198.51.100.42:12345"
	withoutTOTPResponse := serveSecurityRequest(handler, withoutTOTP)
	if withoutTOTPResponse.Code != http.StatusUnauthorized {
		t.Fatalf("login without configured TOTP status=%d, want %d", withoutTOTPResponse.Code, http.StatusUnauthorized)
	}
	if cookies := withoutTOTPResponse.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("login without configured TOTP created session cookies: %v", cookies)
	}

	now := time.Now().UTC()
	login := securityJSONRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password","totp_code":"`+securityTOTPCode(secret, now)+`"}`)
	login.RemoteAddr = "198.51.100.43:12345"
	loginResponse := serveSecurityRequest(handler, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("TOTP login status=%d; body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := securitySessionCookie(t, loginResponse)
	csrfToken := securityCSRFToken(t, loginResponse)

	missingCode := securityJSONRequest(http.MethodPost, "/api/auth/reauth", `{"password":"test-password"}`)
	missingCode.RemoteAddr = login.RemoteAddr
	missingCode.AddCookie(cookie)
	missingCode.Header.Set("X-CSRF-Token", csrfToken)
	missingResponse := serveSecurityRequest(handler, missingCode)
	if missingResponse.Code != http.StatusOK {
		t.Fatalf("password-only reauth with configured TOTP status=%d, want %d", missingResponse.Code, http.StatusOK)
	}

	// Reauthentication is intentionally password-only; even an invalid TOTP
	// value must not turn it back into a full login verification.
	wrongCode := securityJSONRequest(http.MethodPost, "/api/auth/reauth", `{"password":"test-password","totp_code":"000000"}`)
	wrongCode.RemoteAddr = login.RemoteAddr
	wrongCode.AddCookie(securitySessionCookie(t, missingResponse))
	wrongCode.Header.Set("X-CSRF-Token", securityCSRFToken(t, missingResponse))
	wrongCodeResponse := serveSecurityRequest(handler, wrongCode)
	if wrongCodeResponse.Code != http.StatusOK {
		t.Fatalf("password-only reauth with invalid TOTP status=%d; body=%s", wrongCodeResponse.Code, wrongCodeResponse.Body.String())
	}
}

func TestReauthenticationWithoutSessionRequiresLoginEvenWhenTOTPIsConfigured(t *testing.T) {
	prepareAuthSecurityEnv(t)
	secret := []byte("12345678901234567890")
	t.Setenv("AUTH_TOTP_SECRET_FILE", writeSecurityTOTPSecret(t, secret, 0o600))
	handler, err := NewRouterWithError()
	if err != nil {
		t.Fatal(err)
	}

	reauth := securityJSONRequest(http.MethodPost, "/api/auth/reauth", `{"password":"test-password"}`)
	reauth.RemoteAddr = "198.51.100.44:12345"
	response := serveSecurityRequest(handler, reauth)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated reauth status=%d, want %d", response.Code, http.StatusUnauthorized)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode unauthenticated reauth body: %v", err)
	}
	if body["login_required"] != true {
		t.Fatalf("unauthenticated reauth body=%v, want login_required=true", body)
	}
	if cookies := response.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("unauthenticated reauth created session cookies: %v", cookies)
	}
}

func TestNewRouterRejectsNonCanonicalTOTPRequireFlag(t *testing.T) {
	for _, raw := range []string{"1", "TRUE", "t", "yes"} {
		t.Run(raw, func(t *testing.T) {
			prepareAuthSecurityEnv(t)
			t.Setenv("AUTH_REQUIRE_TOTP", raw)
			if _, err := NewRouterWithError(); err == nil {
				t.Fatalf("AUTH_REQUIRE_TOTP=%q was accepted", raw)
			}
		})
	}
}

func writeSecurityTOTPSecret(t *testing.T, secret []byte, mode os.FileMode) string {
	t.Helper()
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	path := filepath.Join(t.TempDir(), "totp-secret")
	if err := os.WriteFile(path, []byte(encoded), mode); err != nil {
		t.Fatalf("write TOTP secret: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod TOTP secret: %v", err)
	}
	return path
}

func securityTOTPCode(secret []byte, now time.Time) string {
	counter := uint64(now.Unix() / 30)
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac := hmac.New(sha1.New, secret) // #nosec G401 -- RFC 6238 interoperability.
	_, _ = mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	return formatSecurityTOTPCode(value % 1_000_000)
}

func formatSecurityTOTPCode(value uint32) string {
	return string([]byte{
		byte('0' + value/100000),
		byte('0' + (value/10000)%10),
		byte('0' + (value/1000)%10),
		byte('0' + (value/100)%10),
		byte('0' + (value/10)%10),
		byte('0' + value%10),
	})
}

func securitySessionCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "omni_money_session" || cookie.Name == "__Host-omni_money_session" {
			return cookie
		}
	}
	t.Fatalf("authentication response did not set a session cookie")
	return nil
}

func securityCSRFToken(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode CSRF token response: %v", err)
	}
	if body.CSRFToken == "" {
		t.Fatal("authentication response did not include CSRF token")
	}
	return body.CSRFToken
}
