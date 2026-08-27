package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCacheControlMiddleware(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		wantCache     string
		wantPragma    string
		wantSurrogate string
	}{
		{
			name:          "financial API",
			path:          "/api/transactions",
			wantCache:     "no-store, private, max-age=0",
			wantPragma:    "no-cache",
			wantSurrogate: "no-store",
		},
		{
			name:      "fingerprinted asset",
			path:      "/assets/index-abc123.js",
			wantCache: "public, max-age=31536000, immutable",
		},
		{
			name:      "HTML shell",
			path:      "/",
			wantCache: "no-cache",
		},
		{
			name: "health check",
			path: "/healthz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := CacheControlMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "test", http.StatusUnauthorized)
			}))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if got := recorder.Header().Get("Cache-Control"); got != tt.wantCache {
				t.Fatalf("Cache-Control = %q, want %q", got, tt.wantCache)
			}
			if got := recorder.Header().Get("Pragma"); got != tt.wantPragma {
				t.Fatalf("Pragma = %q, want %q", got, tt.wantPragma)
			}
			if got := recorder.Header().Get("Surrogate-Control"); got != tt.wantSurrogate {
				t.Fatalf("Surrogate-Control = %q, want %q", got, tt.wantSurrogate)
			}
		})
	}
}
