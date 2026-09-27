package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"omni_money/backend/serverauth"
)

func TestWriteLoginThrottledRoundsRetryAfter(t *testing.T) {
	recorder := httptest.NewRecorder()
	if !writeLoginThrottled(recorder, &serverauth.LoginThrottledError{RetryAfter: 250 * time.Millisecond}) {
		t.Fatal("throttle error was not recognized")
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	if recorder := httptest.NewRecorder(); writeLoginThrottled(recorder, serverauth.ErrInvalidCredentials) {
		t.Fatal("invalid credentials was treated as throttled")
	}
}

func TestWritePasskeyErrorSurfacesThrottleForLoginFlows(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePasskeyError(recorder, &serverauth.LoginThrottledError{RetryAfter: 15 * time.Minute}, true)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "900" {
		t.Fatalf("Retry-After = %q, want 900", got)
	}
	if strings.Contains(recorder.Body.String(), "person@example.com") {
		t.Fatalf("throttle response leaked an address: %s", recorder.Body.String())
	}
}

func TestServerLoginReturnsThrottleEnvelope(t *testing.T) {
	accounts := &fakeServerAccounts{loginErr: &serverauth.LoginThrottledError{RetryAfter: 15 * time.Minute}}
	handler := newTestServerRouter(t, accounts, &fakeServerControl{bootstrapped: true})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, serverJSONRequest(t, http.MethodPost, "/api/auth/login",
		`{"email":"a@example.com","password_b64":"Y29ycmVjdCBob3JzZSBiYXR0ZXJ5IHN0YXBsZQ=="}`))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusTooManyRequests, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "900" {
		t.Fatalf("Retry-After = %q, want 900", got)
	}
	if strings.Contains(recorder.Body.String(), "a@example.com") {
		t.Fatalf("throttle response leaked the submitted address: %s", recorder.Body.String())
	}
}
