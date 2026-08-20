package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyMiddlewareResolvesForwardedForFromRight(t *testing.T) {
	cfg := &ProxyConfig{
		trustedCIDRs: parseTrustedProxies("10.0.0.0/24"),
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
		trustedCIDRs: parseTrustedProxies("10.0.0.0/24"),
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

func TestParseTrustedProxiesRejectsBroadAndMalformedEntries(t *testing.T) {
	for _, raw := range []string{"0.0.0.0/0", "10.0.0.0/23", "::/0", "2001:db8::/119", "not-an-ip", "10.0.0.0/33"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseTrustedProxiesStrict(raw); err == nil {
				t.Fatalf("parseTrustedProxiesStrict(%q) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestNewProxyConfigFromEnvFailsClosedForInvalidTrustedProxy(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "0.0.0.0/0")
	cfg := NewProxyConfigFromEnv()
	if cfg.configErr == nil {
		t.Fatal("invalid trusted proxy was not recorded")
	}
	handler := ProxyMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://money.local/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid config status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestNewProxyConfigFromEnvRequiresCanonicalBoolean(t *testing.T) {
	for _, raw := range []string{"1", "TRUE", "t", "yes"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("FORCE_HTTPS", raw)
			cfg := NewProxyConfigFromEnv()
			if cfg.configErr == nil {
				t.Fatalf("FORCE_HTTPS=%q was accepted", raw)
			}
		})
	}
}

func TestNewProxyConfigFromEnvRequiresAllowedHostsWithTrustedProxy(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/24")
	t.Setenv("ALLOWED_HOSTS", "")
	if cfg := NewProxyConfigFromEnv(); cfg.configErr == nil {
		t.Fatal("trusted proxy without ALLOWED_HOSTS was accepted")
	}
}

func TestNewProxyConfigFromEnvAlwaysRequiresAllowedHosts(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("ALLOWED_HOSTS", "")
	if cfg := NewProxyConfigFromEnv(); cfg.configErr == nil {
		t.Fatal("empty ALLOWED_HOSTS was accepted")
	}
}

func TestParseTrustedProxiesAcceptsOnlySmallCIDRsOrIndividualIPs(t *testing.T) {
	for _, raw := range []string{"10.0.0.0/24", "10.0.0.3", "2001:db8::/120", "2001:db8::3"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseTrustedProxiesStrict(raw); err != nil {
				t.Fatalf("parseTrustedProxiesStrict(%q): %v", raw, err)
			}
		})
	}
}

func TestProxyMiddlewareRejectsAmbiguousForwardedHeaders(t *testing.T) {
	cfg := &ProxyConfig{trustedCIDRs: parseTrustedProxies("10.0.0.0/24")}
	handler := ProxyMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	cases := []struct {
		name   string
		header string
		value  string
	}{
		{name: "xff", header: "X-Forwarded-For", value: "198.51.100.1, not-an-ip"},
		{name: "xff-empty-hop", header: "X-Forwarded-For", value: "198.51.100.1, "},
		{name: "proto-list", header: "X-Forwarded-Proto", value: "https, http"},
		{name: "proto-invalid", header: "X-Forwarded-Proto", value: "ftp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://money.local/", nil)
			req.RemoteAddr = "10.0.0.1:12345"
			req.Header.Set(tc.header, tc.value)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestProxyMiddlewareRejectsMultipleForwardedHeaderValues(t *testing.T) {
	cfg := &ProxyConfig{trustedCIDRs: parseTrustedProxies("10.0.0.0/24")}
	handler := ProxyMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://money.local/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header["X-Forwarded-Proto"] = []string{"https", "http"}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestProxyMiddlewareRemovesForwardedHeadersFromUntrustedRemote(t *testing.T) {
	cfg := &ProxyConfig{trustedCIDRs: parseTrustedProxies("10.0.0.0/24")}
	var got http.Header
	handler := ProxyMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://money.local/", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	for _, name := range forwardingHeaders {
		req.Header.Set(name, "198.51.100.23")
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	for _, name := range forwardingHeaders {
		if got.Get(name) != "" {
			t.Errorf("untrusted %s header remained: %q", name, got.Get(name))
		}
	}
}

func TestProxyMiddlewareRemovesForwardingHeadersAfterTrustedResolution(t *testing.T) {
	cfg := &ProxyConfig{trustedCIDRs: parseTrustedProxies("10.0.0.0/24")}
	var got http.Header
	handler := ProxyMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if clientIP := ClientIPFromRequest(r); clientIP != "198.51.100.23" {
			t.Errorf("client IP = %q", clientIP)
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://money.local/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.23")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	req.Header.Set("X-Forwarded-Port", "444")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	for _, name := range forwardingHeaders {
		if got.Get(name) != "" {
			t.Errorf("trusted %s header remained: %q", name, got.Get(name))
		}
	}
}

func TestValidatePublicListenerSecurity(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "money.example.com")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("FORCE_HTTPS", "false")
	t.Setenv("ALLOW_INSECURE_HTTP", "false")

	if err := ValidatePublicListenerSecurity("127.0.0.1", false); err != nil {
		t.Fatalf("loopback HTTP rejected: %v", err)
	}
	if err := ValidatePublicListenerSecurity("0.0.0.0", true); err != nil {
		t.Fatalf("direct TLS rejected: %v", err)
	}
	if err := ValidatePublicListenerSecurity("0.0.0.0", false); err == nil {
		t.Fatal("non-loopback clear-text listener was accepted")
	}

	t.Setenv("ALLOW_INSECURE_HTTP", "true")
	if err := ValidatePublicListenerSecurity("0.0.0.0", false); err != nil {
		t.Fatalf("explicit insecure local override rejected: %v", err)
	}

	t.Setenv("ALLOW_INSECURE_HTTP", "false")
	t.Setenv("FORCE_HTTPS", "true")
	t.Setenv("TRUSTED_PROXIES", "10.0.0.3")
	if err := ValidatePublicListenerSecurity("0.0.0.0", false); err != nil {
		t.Fatalf("trusted HTTPS proxy listener rejected: %v", err)
	}
}

func TestProxyMiddlewareValidatesAllowedHostOnHTTPSRequests(t *testing.T) {
	cfg := &ProxyConfig{allowedHosts: parseAllowedHosts("money.example.com")}
	handler := ProxyMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "https://evil.example/", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestProxyMiddlewareRejectsInvalidRedirectHostConfiguration(t *testing.T) {
	t.Setenv("HTTPS_REDIRECT_HOST", "https://evil.example")
	cfg := NewProxyConfigFromEnv()
	if cfg.configErr == nil {
		t.Fatal("invalid redirect host was not recorded")
	}
}
