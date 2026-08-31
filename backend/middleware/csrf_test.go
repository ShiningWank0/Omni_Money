package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func requestWithSession(request *http.Request, session *Session) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), sessionKey, session))
}

func newCSRFTestFixture(t *testing.T) (*SessionManager, *Session, *time.Time) {
	t.Helper()
	start := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	manager, clock := newClockedSessionManager(t, securityTestSessionConfig(), start)
	session, err := manager.CreateSession("user")
	if err != nil {
		t.Fatal(err)
	}
	return manager, session, clock
}

func csrfProtectedHandler(manager *SessionManager, called *bool) http.Handler {
	return CSRFMiddleware(manager, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*called = true
		w.WriteHeader(http.StatusNoContent)
	}))
}

func TestCSRFMiddlewareRequiresTokenForEveryUnsafeMethod(t *testing.T) {
	manager, session, _ := newCSRFTestFixture(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodTrace} {
		t.Run(method, func(t *testing.T) {
			called := false
			handler := csrfProtectedHandler(manager, &called)
			request := httptest.NewRequest(http.MethodPost, "https://money.example/api/transactions", nil)
			request.Method = method
			request = requestWithSession(request, session)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden || called {
				t.Fatalf("missing token status=%d called=%v, want 403 false", recorder.Code, called)
			}

			called = false
			request = httptest.NewRequest(http.MethodPost, "https://money.example/api/transactions", nil)
			request.Method = method
			request.Header.Set(CSRFHeaderName, session.CSRFToken)
			request = requestWithSession(request, session)
			recorder = httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent || !called {
				t.Fatalf("valid token status=%d called=%v, want 204 true", recorder.Code, called)
			}
		})
	}
}

func TestCSRFMiddlewareAllowsSafeMethodsWithoutToken(t *testing.T) {
	manager, _, _ := newCSRFTestFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			called := false
			request := httptest.NewRequest(method, "https://money.example/api/accounts", nil)
			recorder := httptest.NewRecorder()
			csrfProtectedHandler(manager, &called).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent || !called {
				t.Fatalf("safe method status=%d called=%v, want 204 true", recorder.Code, called)
			}
		})
	}
}

func TestCSRFMiddlewareRejectsWrongAndAmbiguousHeaders(t *testing.T) {
	manager, session, _ := newCSRFTestFixture(t)
	tests := []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "wrong", values: []string{"0000000000000000000000000000000000000000000000000000000000000000"}},
		{name: "comma joined", values: []string{session.CSRFToken + "," + session.CSRFToken}},
		{name: "multiple", values: []string{session.CSRFToken, session.CSRFToken}},
		{name: "uppercase representation", values: []string{strings.ToUpper(session.CSRFToken)}},
		{name: "surrounding whitespace", values: []string{" " + session.CSRFToken + " "}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			request := httptest.NewRequest(http.MethodPost, "https://money.example/api/transactions", nil)
			if test.values != nil {
				request.Header[http.CanonicalHeaderKey(CSRFHeaderName)] = append([]string(nil), test.values...)
			}
			request = requestWithSession(request, session)
			recorder := httptest.NewRecorder()
			csrfProtectedHandler(manager, &called).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden || called {
				t.Fatalf("status=%d called=%v, want 403 false", recorder.Code, called)
			}
		})
	}
}

func TestCSRFMiddlewareEnforcesExactOrigin(t *testing.T) {
	manager, session, _ := newCSRFTestFixture(t)
	tests := []struct {
		name    string
		origins []string
		wantOK  bool
	}{
		{name: "origin absent with token", wantOK: true},
		{name: "exact origin", origins: []string{"https://money.example"}, wantOK: true},
		{name: "wrong scheme", origins: []string{"http://money.example"}},
		{name: "wrong host", origins: []string{"https://evil.example"}},
		{name: "null origin", origins: []string{"null"}},
		{name: "userinfo", origins: []string{"https://user@money.example"}},
		{name: "path", origins: []string{"https://money.example/path"}},
		{name: "empty query", origins: []string{"https://money.example?"}},
		{name: "comma joined", origins: []string{"https://money.example, https://evil.example"}},
		{name: "multiple", origins: []string{"https://money.example", "https://money.example"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			request := httptest.NewRequest(http.MethodPost, "https://money.example/api/transactions", nil)
			request.Header.Set(CSRFHeaderName, session.CSRFToken)
			if test.origins != nil {
				request.Header["Origin"] = append([]string(nil), test.origins...)
			}
			request = requestWithSession(request, session)
			recorder := httptest.NewRecorder()
			csrfProtectedHandler(manager, &called).ServeHTTP(recorder, request)
			if gotOK := recorder.Code == http.StatusNoContent && called; gotOK != test.wantOK {
				t.Fatalf("status=%d called=%v, wantOK=%v", recorder.Code, called, test.wantOK)
			}
		})
	}
}

func TestCSRFMiddlewareEnforcesFetchMetadata(t *testing.T) {
	manager, session, _ := newCSRFTestFixture(t)
	tests := []struct {
		name   string
		values []string
		wantOK bool
	}{
		{name: "absent", wantOK: true},
		{name: "same origin", values: []string{"same-origin"}, wantOK: true},
		{name: "browser none", values: []string{"none"}, wantOK: true},
		{name: "same site is not sufficient", values: []string{"same-site"}},
		{name: "cross site", values: []string{"cross-site"}},
		{name: "empty", values: []string{""}},
		{name: "multiple", values: []string{"same-origin", "same-origin"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			request := httptest.NewRequest(http.MethodPost, "https://money.example/api/transactions", nil)
			request.Header.Set(CSRFHeaderName, session.CSRFToken)
			if test.values != nil {
				request.Header["Sec-Fetch-Site"] = append([]string(nil), test.values...)
			}
			request = requestWithSession(request, session)
			recorder := httptest.NewRecorder()
			csrfProtectedHandler(manager, &called).ServeHTTP(recorder, request)
			if gotOK := recorder.Code == http.StatusNoContent && called; gotOK != test.wantOK {
				t.Fatalf("status=%d called=%v, wantOK=%v", recorder.Code, called, test.wantOK)
			}
		})
	}
}

func TestLoginRequiresBrowserBoundaryButNotSessionToken(t *testing.T) {
	manager, _, _ := newCSRFTestFixture(t)
	tests := []struct {
		name      string
		origin    string
		fetchSite string
		wantOK    bool
	}{
		{name: "same origin", origin: "https://money.example", fetchSite: "same-origin", wantOK: true},
		{name: "non browser client", wantOK: true},
		{name: "cross origin", origin: "https://evil.example", fetchSite: "cross-site"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			request := httptest.NewRequest(http.MethodPost, "https://money.example/api/auth/login", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			recorder := httptest.NewRecorder()
			csrfProtectedHandler(manager, &called).ServeHTTP(recorder, request)
			if gotOK := recorder.Code == http.StatusNoContent && called; gotOK != test.wantOK {
				t.Fatalf("status=%d called=%v, wantOK=%v", recorder.Code, called, test.wantOK)
			}
		})
	}
}

func TestRecentAuthenticationGuardsSensitiveRoutes(t *testing.T) {
	manager, session, clock := newCSRFTestFixture(t)
	targets := []struct {
		method string
		path   string
	}{
		// Bulk and administrative operations.
		{http.MethodGet, "/api/backup_csv"},
		{http.MethodPost, "/api/import_csv"},
		{http.MethodPost, "/api/snapshots"},
		{http.MethodPost, "/api/snapshots/restore"},
		{http.MethodPost, "/api/auth/logout-all"},
		{http.MethodPost, "/api/auth/password"},
		{http.MethodPost, "/api/auth/recovery-code"},
		{http.MethodDelete, "/api/auth/passkeys/all"},
		{http.MethodPost, "/api/ai-console/transactions"},
		{http.MethodPost, "/api/ai-console/analysis"},
	}

	for _, target := range targets {
		t.Run(target.method+" "+target.path, func(t *testing.T) {
			called := false
			handler := RecentAuthMiddleware(manager, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))

			request := httptest.NewRequest(target.method, "https://money.example"+target.path, nil)
			request = requestWithSession(request, session)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent || !called {
				t.Fatalf("fresh auth status=%d called=%v, want 204 true", recorder.Code, called)
			}

			*clock = session.ReauthenticatedAt.Add(manager.config.RecentAuthAge + time.Nanosecond)
			called = false
			request = httptest.NewRequest(target.method, "https://money.example"+target.path, nil)
			request = requestWithSession(request, session)
			recorder = httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusPreconditionRequired || called {
				t.Fatalf("stale auth status=%d called=%v, want 428 false", recorder.Code, called)
			}
			*clock = session.ReauthenticatedAt
		})
	}
}

func TestRecentAuthenticationDoesNotGuardOrdinaryFinancialRoutesAfterFiveMinutes(t *testing.T) {
	manager, session, clock := newCSRFTestFixture(t)
	*clock = session.ReauthenticatedAt.Add(manager.config.RecentAuthAge + time.Second)
	for _, target := range []struct {
		method string
		path   string
	}{
		// Financial data reads.
		{http.MethodGet, "/api/accounts"},
		{http.MethodGet, "/api/items"},
		{http.MethodGet, "/api/transactions"},
		{http.MethodGet, "/api/balance_history"},
		{http.MethodGet, "/api/balance_history_filtered"},
		{http.MethodGet, "/api/credit_card_settings"},
		{http.MethodGet, "/api/bank_account_settings"},
		{http.MethodGet, "/api/transaction_images/123"},
		{http.MethodGet, "/api/transaction_images/123/456"},
		{http.MethodGet, "/api/tags"},
		{http.MethodGet, "/api/tags/summary"},
		{http.MethodGet, "/api/transaction_tags/123"},
		{http.MethodGet, "/api/transaction_links/123"},
		// Financial mutations.
		{http.MethodPost, "/api/transactions"},
		{http.MethodPut, "/api/transactions/123"},
		{http.MethodPatch, "/api/transactions/123"},
		{http.MethodDelete, "/api/transactions/123"},
		{http.MethodPost, "/api/credit_card_settings"},
		{http.MethodPost, "/api/bank_account_settings"},
		{http.MethodPost, "/api/transaction_images/123"},
		{http.MethodDelete, "/api/transaction_images/123/456"},
		{http.MethodPost, "/api/tags"},
		{http.MethodPost, "/api/tags/path"},
		{http.MethodPut, "/api/tags/123"},
		{http.MethodDelete, "/api/tags/123"},
		{http.MethodPost, "/api/transaction_tags/123"},
		{http.MethodDelete, "/api/transaction_tags/123/456"},
		{http.MethodPost, "/api/transaction_links/123"},
		{http.MethodDelete, "/api/transaction_links/123/456"},
	} {
		t.Run(target.method+" "+target.path, func(t *testing.T) {
			called := false
			handler := RecentAuthMiddleware(manager, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))

			request := httptest.NewRequest(target.method, "https://money.example"+target.path, nil)
			request = requestWithSession(request, session)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent || !called {
				t.Fatalf("stale auth status=%d called=%v, want 204 true", recorder.Code, called)
			}
		})
	}
}

func TestRecentAuthenticationDoesNotGuardOrdinaryOrWrongMethodRoutes(t *testing.T) {
	manager, session, clock := newCSRFTestFixture(t)
	*clock = session.ReauthenticatedAt.Add(manager.config.RecentAuthAge + time.Second)
	for _, target := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/auth/status"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/api/snapshots"},
		{http.MethodGet, "/api/import_csv"},
		{http.MethodPost, "/api/backup_csv"},
		{http.MethodGet, "/api/snapshots/restore"},
		{http.MethodGet, "/api/ai-console/analysis"},
		// Lookalike paths must not be classified as the exact high-impact route.
		// The real ServeMux also does not dispatch these to the sensitive handler.
		{http.MethodGet, "/api/backup_csv/"},
		{http.MethodPost, "/api/import_csv/"},
		{http.MethodPost, "/api/snapshots/"},
		{http.MethodPost, "/api/snapshots/restore/"},
		{http.MethodPost, "/api/auth/logout-all/"},
		{http.MethodPost, "/api/auth/password/"},
		{http.MethodPost, "/api/auth/recovery-code/"},
		{http.MethodPost, "/api/ai-console/transactions/"},
		{http.MethodPost, "/api/ai-console/analysis/"},
		{http.MethodDelete, "/api/transactions"},
		{http.MethodPut, "/api/transaction_links/123"},
		{http.MethodGet, "/api/transaction_notes/123"},
	} {
		t.Run(target.method+" "+target.path, func(t *testing.T) {
			called := false
			handler := RecentAuthMiddleware(manager, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(target.method, "https://money.example"+target.path, nil)
			request = requestWithSession(request, session)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent || !called {
				t.Fatalf("status=%d called=%v, want 204 true", recorder.Code, called)
			}
		})
	}
}

func TestRecentAuthenticationBoundaryAndMissingContext(t *testing.T) {
	manager, session, clock := newCSRFTestFixture(t)
	called := false
	handler := RecentAuthMiddleware(manager, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	*clock = session.ReauthenticatedAt.Add(manager.config.RecentAuthAge)
	request := httptest.NewRequest(http.MethodGet, "https://money.example/api/backup_csv", nil)
	request = requestWithSession(request, session)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || !called {
		t.Fatalf("exact boundary status=%d called=%v, want 204 true", recorder.Code, called)
	}

	called = false
	request = httptest.NewRequest(http.MethodGet, "https://money.example/api/backup_csv", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPreconditionRequired || called {
		t.Fatalf("missing context status=%d called=%v, want 428 false", recorder.Code, called)
	}
}
