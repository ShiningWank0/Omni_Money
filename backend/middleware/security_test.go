package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"omni_money/backend/core"
)

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
