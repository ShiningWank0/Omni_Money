package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyMiddlewareResolvesForwardedForFromRight(t *testing.T) {
	cfg := &ProxyConfig{
		trustedCIDRs: parseTrustedProxies("10.0.0.0/8"),
	}
	handler := ProxyMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(ClientIPFromRequest(r)))
	}))

	req := httptest.NewRequest(http.MethodGet, "http://money.local/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.23, 10.0.0.2")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if got, want := rr.Body.String(), "198.51.100.23"; got != want {
		t.Fatalf("client IP = %q, want %q", got, want)
	}
}

func TestProxyMiddlewareIgnoresForwardedHeadersFromUntrustedRemote(t *testing.T) {
	cfg := &ProxyConfig{
		trustedCIDRs: parseTrustedProxies("10.0.0.0/8"),
	}
	handler := ProxyMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(ClientIPFromRequest(r) + "," + RequestProto(r)))
	}))

	req := httptest.NewRequest(http.MethodGet, "http://money.local/", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.23")
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if got, want := rr.Body.String(), "203.0.113.10,http"; got != want {
		t.Fatalf("forwarded result = %q, want %q", got, want)
	}
}

func TestProxyMiddlewareForceHTTPSRequiresAllowedHost(t *testing.T) {
	cfg := &ProxyConfig{
		forceHTTPS:   true,
		allowedHosts: parseAllowedHosts("money.example.com"),
	}
	handler := ProxyMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	allowedReq := httptest.NewRequest(http.MethodGet, "http://money.example.com/path?q=1", nil)
	allowedReq.RemoteAddr = "203.0.113.10:12345"
	allowedRR := httptest.NewRecorder()
	handler.ServeHTTP(allowedRR, allowedReq)

	if allowedRR.Code != http.StatusMovedPermanently {
		t.Fatalf("allowed host status = %d, want %d", allowedRR.Code, http.StatusMovedPermanently)
	}
	if got, want := allowedRR.Header().Get("Location"), "https://money.example.com/path?q=1"; got != want {
		t.Fatalf("redirect location = %q, want %q", got, want)
	}

	blockedReq := httptest.NewRequest(http.MethodGet, "http://evil.example/path", nil)
	blockedReq.RemoteAddr = "203.0.113.10:12345"
	blockedRR := httptest.NewRecorder()
	handler.ServeHTTP(blockedRR, blockedReq)

	if blockedRR.Code != http.StatusBadRequest {
		t.Fatalf("blocked host status = %d, want %d", blockedRR.Code, http.StatusBadRequest)
	}
}

func TestProxyMiddlewareForceHTTPSCanUseConfiguredRedirectHost(t *testing.T) {
	cfg := &ProxyConfig{
		forceHTTPS:   true,
		redirectHost: "money.example.com",
	}
	handler := ProxyMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://untrusted.example/path?q=1", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got, want := rr.Header().Get("Location"), "https://money.example.com/path?q=1"; got != want {
		t.Fatalf("redirect location = %q, want %q", got, want)
	}
}
