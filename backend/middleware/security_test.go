package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"omni_money/backend/core"
)

type countingRequestBody struct {
	reader io.Reader
	reads  int
}

type expiringRequestBody struct {
	reader io.Reader
	onRead func()
	once   sync.Once
}

func (body *expiringRequestBody) Read(p []byte) (int, error) {
	body.once.Do(body.onRead)
	return body.reader.Read(p)
}

func (body *expiringRequestBody) Close() error { return nil }

func (body *countingRequestBody) Read(p []byte) (int, error) {
	body.reads++
	return body.reader.Read(p)
}

func (body *countingRequestBody) Close() error { return nil }

func TestSecurityHeadersMiddlewareHSTSOnlyForHTTPS(t *testing.T) {
	handler := SecurityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, tt := range []struct {
		name     string
		url      string
		wantHSTS bool
	}{
		{name: "http", url: "http://example.test/", wantHSTS: false},
		{name: "https", url: "https://example.test/", wantHSTS: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			got := recorder.Header().Get("Strict-Transport-Security")
			if tt.wantHSTS && !strings.HasPrefix(got, "max-age=31536000") {
				t.Fatalf("Strict-Transport-Security = %q", got)
			}
			if !tt.wantHSTS && got != "" {
				t.Fatalf("HTTP response unexpectedly has HSTS: %q", got)
			}
		})
	}
}

func TestMaxBodySizeMiddlewareLimitsConcurrentCSVImports(t *testing.T) {
	release, ok := core.TryAcquireCSVImportSlot()
	if !ok {
		t.Fatal("failed to reserve CSV import slot for test")
	}
	t.Cleanup(release)

	nextCalled := false
	handler := MaxBodySizeMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}))
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/import_csv", strings.NewReader(`{"content":""}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if nextCalled {
		t.Fatal("next handler was called while CSV import slots were exhausted")
	}
}

func TestMaxBodySizeMiddlewareReleasesCSVSlotAfterHandlerError(t *testing.T) {
	calls := 0
	handler := MaxBodySizeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "invalid import", http.StatusBadRequest)
	}))
	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/import_csv", strings.NewReader(`{"content":""}`))
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("request %d status = %d, want %d", i+1, recorder.Code, http.StatusBadRequest)
		}
	}
	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2", calls)
	}
}

func TestMaxBodySizeMiddlewareOnlyUsesLargeCSVPathForRegisteredPost(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		ctype  string
	}{
		{name: "get registered path", method: http.MethodGet, path: "/api/import_csv", ctype: "text/csv"},
		{name: "post trailing slash", method: http.MethodPost, path: "/api/import_csv/", ctype: "text/csv"},
		{name: "post other route", method: http.MethodPost, path: "/api/ai/import_csv", ctype: "text/csv"},
		{name: "get json", method: http.MethodGet, path: "/api/import_csv", ctype: "application/json"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			body := &countingRequestBody{reader: strings.NewReader("small body")}
			nextCalled := false
			handler := MaxBodySizeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				nextCalled = true
				_, _ = io.ReadAll(req.Body)
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(test.method, test.path, body)
			req.Header.Set("Content-Type", test.ctype)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusNoContent || !nextCalled {
				t.Fatalf("status=%d next=%v, want successful non-spooled pass-through", recorder.Code, nextCalled)
			}
			if body.reads == 0 {
				t.Fatal("pass-through request body was not read")
			}
		})
	}
}

func TestMaxBodySizeMiddlewareDoesNotAdmitUnsupportedContentTypeAsCSV(t *testing.T) {
	// An import-looking path is not sufficient. Unsupported media types must
	// stay on the ordinary 10 MiB body limit and must not reserve a large CSV
	// spool while an unregistered handler reads them.
	body := strings.NewReader(strings.Repeat("x", maxRequestBodySize+1))
	req := httptest.NewRequest(http.MethodPost, "/api/import_csv", body)
	req.Header.Set("Content-Type", "text/plain")
	recorder := httptest.NewRecorder()
	MaxBodySizeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err == nil {
			t.Error("ordinary body limit did not reject oversized unsupported content")
		}
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	})).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestCSVBodyIsNotReadBeforeAuthenticationAndCSRFChecks(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	t.Cleanup(manager.Close)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler was called")
	})
	protected := []struct {
		name string
		h    http.Handler
		want int
	}{
		{
			name: "session authentication",
			h:    SessionAuthMiddleware(manager, MaxBodySizeMiddleware(next)),
			want: http.StatusUnauthorized,
		},
		{
			name: "csrf",
			h:    CSRFMiddleware(manager, MaxBodySizeMiddleware(next)),
			want: http.StatusForbidden,
		},
		{
			name: "recent authentication",
			h:    RecentAuthMiddleware(manager, MaxBodySizeMiddleware(next)),
			want: http.StatusPreconditionRequired,
		},
	}
	for _, test := range protected {
		t.Run(test.name, func(t *testing.T) {
			body := &countingRequestBody{reader: strings.NewReader(`{"content":"must not be read"}`)}
			req := httptest.NewRequest(http.MethodPost, "/api/import_csv", body)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			test.h.ServeHTTP(recorder, req)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			if body.reads != 0 {
				t.Fatalf("request body was read %d times before %s", body.reads, test.name)
			}
		})
	}
}

func TestCSVImportRevalidatesRecentAuthAfterSpooling(t *testing.T) {
	manager, session, clock := newCSRFTestFixture(t)
	t.Cleanup(manager.Close)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	handler := RecentAuthMiddleware(manager, MaxBodySizeMiddleware(next))
	body := &expiringRequestBody{
		reader: strings.NewReader("account,date,item,type,amount\ncash,2026-01-01,item,income,1\n"),
		onRead: func() {
			*clock = session.ReauthenticatedAt.Add(manager.config.RecentAuthAge + time.Second)
		},
	}
	req := httptest.NewRequest(http.MethodPost, "https://money.example/api/import_csv", body)
	req = requestWithSession(req, session)
	req.Header.Set("Content-Type", "text/csv")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusPreconditionRequired || called {
		t.Fatalf("post-spool stale auth status=%d called=%v, want 428 false", recorder.Code, called)
	}
}
