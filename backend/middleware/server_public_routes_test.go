package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerPublicAuthAllowlistIsExact(t *testing.T) {
	tests := []struct {
		method string
		path   string
		public bool
	}{
		{http.MethodGet, "/api/auth/status", true},
		{http.MethodPost, "/api/auth/login", true},
		{http.MethodPost, "/api/auth/passkeys/login/begin", true},
		{http.MethodPost, "/api/auth/passkeys/login/finish", true},
		{http.MethodPost, "/api/auth/setup", true},
		{http.MethodPost, "/api/auth/invitations/accept", true},
		{http.MethodPost, "/api/auth/password-reset/complete", true},
		{http.MethodPost, "/api/auth/status", false},
		{http.MethodGet, "/api/auth/login", false},
		{http.MethodGet, "/api/auth/passkeys/login/begin", false},
		{http.MethodPost, "/api/auth/passkeys/login/begin/", false},
		{http.MethodPost, "/api/auth/passkeys/register/begin", false},
		{http.MethodGet, "/api/auth/setup", false},
		{http.MethodPost, "/api/auth/setup/", false},
		{http.MethodPost, "/api/auth/invitations/accept/", false},
		{http.MethodGet, "/api/auth/password-reset/complete", false},
		{http.MethodPost, "/api/auth/logout", false},
		{http.MethodGet, "/api/admin/users", false},
		{http.MethodGet, "/healthz", false},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if got := isPublicServerAuthRequest(request); got != test.public {
				t.Fatalf("isPublicServerAuthRequest = %t, want %t", got, test.public)
			}
			wantRequired := !test.public && len(test.path) >= len("/api/") && test.path[:len("/api/")] == "/api/"
			if got := requiresSessionAuth(request); got != wantRequired {
				t.Fatalf("requiresSessionAuth = %t, want %t", got, wantRequired)
			}
		})
	}
}

func TestPublicServerAuthStillRequiresValidBrowserBoundary(t *testing.T) {
	manager := NewSessionManager(DefaultSessionMaxAge)
	t.Cleanup(manager.Close)
	handler := CSRFMiddleware(manager, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{
		"/api/auth/login",
		"/api/auth/passkeys/login/begin",
		"/api/auth/passkeys/login/finish",
		"/api/auth/setup",
		"/api/auth/invitations/accept",
		"/api/auth/password-reset/complete",
	} {
		t.Run(path, func(t *testing.T) {
			crossSite := httptest.NewRequest(http.MethodPost, "https://money.example.test"+path, nil)
			crossSite.Header.Set("Origin", "https://attacker.example")
			crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, crossSite)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("cross-site status = %d, want %d", recorder.Code, http.StatusForbidden)
			}

			sameOrigin := httptest.NewRequest(http.MethodPost, "https://money.example.test"+path, nil)
			sameOrigin.Header.Set("Origin", "https://money.example.test")
			sameOrigin.Header.Set("Sec-Fetch-Site", "same-origin")
			recorder = httptest.NewRecorder()
			handler.ServeHTTP(recorder, sameOrigin)
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("same-origin status = %d, want %d", recorder.Code, http.StatusNoContent)
			}
		})
	}
}
