package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
