// Package middleware は認証、AI用APIの接続制御を提供する
package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

type proxyContextKey string

const (
	clientIPKey     proxyContextKey = "client-ip"
	requestProtoKey proxyContextKey = "request-proto"
)

// ProxyConfig はリバースプロキシ関連設定
type ProxyConfig struct {
	trustedCIDRs []*net.IPNet
	forceHTTPS   bool
	allowedHosts map[string]struct{}
	redirectHost string
	// configErr makes malformed security-sensitive configuration fail closed.
	// Keeping this on the config preserves the existing constructor API while
	// allowing the router to surface a useful startup error in a later change.
	configErr error
}

// NewProxyConfigFromEnv は環境変数からProxyConfigを作成する
func NewProxyConfigFromEnv() *ProxyConfig {
	trustedCIDRs, trustedErr := parseTrustedProxiesStrict(os.Getenv("TRUSTED_PROXIES"))
	allowedHosts, allowedErr := parseAllowedHostsStrict(os.Getenv("ALLOWED_HOSTS"))
	redirectHost := normalizeHost(os.Getenv("HTTPS_REDIRECT_HOST"))
	forceHTTPS, forceErr := parseStrictBoolEnv("FORCE_HTTPS")
	var configErr error
	if trustedErr != nil {
		configErr = trustedErr
	} else if allowedErr != nil {
		configErr = allowedErr
	} else if forceErr != nil {
		configErr = forceErr
	} else if rawRedirect := strings.TrimSpace(os.Getenv("HTTPS_REDIRECT_HOST")); rawRedirect != "" && redirectHost == "" {
		configErr = fmt.Errorf("invalid HTTPS_REDIRECT_HOST")
	} else if redirectHost != "" && len(allowedHosts) > 0 && !hostInSet(redirectHost, allowedHosts) {
		configErr = fmt.Errorf("HTTPS_REDIRECT_HOST is not present in ALLOWED_HOSTS")
	} else if forceHTTPS && redirectHost == "" && len(allowedHosts) == 0 {
		configErr = fmt.Errorf("FORCE_HTTPS requires HTTPS_REDIRECT_HOST or ALLOWED_HOSTS")
	} else if len(trustedCIDRs) > 0 && len(allowedHosts) == 0 {
		configErr = fmt.Errorf("ALLOWED_HOSTS is required when TRUSTED_PROXIES is configured")
	}

	cfg := &ProxyConfig{
		trustedCIDRs: trustedCIDRs,
		forceHTTPS:   forceHTTPS,
		allowedHosts: allowedHosts,
		redirectHost: redirectHost,
		configErr:    configErr,
	}
	return cfg
}

// Validate exposes startup configuration errors so the server can refuse to
// listen instead of serving with weakened proxy assumptions.
func (c *ProxyConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("proxy configuration is required")
	}
	return c.configErr
}

func parseStrictBoolEnv(name string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	switch raw {
	case "":
		return false, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", name)
	}
}

func parseTrustedProxies(raw string) []*net.IPNet {
	result, err := parseTrustedProxiesStrict(raw)
	if err != nil {
		return nil
	}
	return result
}

func parseTrustedProxiesStrict(raw string) ([]*net.IPNet, error) {
	var result []*net.IPNet
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		// CIDR
		if strings.Contains(token, "/") {
			_, ipNet, err := net.ParseCIDR(token)
			if err != nil {
				return nil, fmt.Errorf("invalid TRUSTED_PROXIES entry %q", token)
			}
			ones, bits := ipNet.Mask.Size()
			if ones == 0 || (bits != 32 && bits != 128) {
				return nil, fmt.Errorf("TRUSTED_PROXIES entry %q is too broad", token)
			}
			result = append(result, ipNet)
			continue
		}

		// 単一IPをCIDRに変換
		ip := net.ParseIP(token)
		if ip == nil {
			return nil, fmt.Errorf("invalid TRUSTED_PROXIES entry %q", token)
		}
		maskBits := 32
		if ip.To4() == nil {
			maskBits = 128
		}
		result = append(result, &net.IPNet{
			IP:   ip,
			Mask: net.CIDRMask(maskBits, maskBits),
		})
	}
	return result, nil
}

func (c *ProxyConfig) isTrustedProxy(ip net.IP) bool {
	if ip == nil || len(c.trustedCIDRs) == 0 {
		return false
	}
	for _, ipNet := range c.trustedCIDRs {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

func (c *ProxyConfig) isAllowedHost(host string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	_, ok := c.allowedHosts[host]
	return ok
}

func parseAllowedHosts(raw string) map[string]struct{} {
	hosts, _ := parseAllowedHostsStrict(raw)
	return hosts
}

func parseAllowedHostsStrict(raw string) (map[string]struct{}, error) {
	hosts := make(map[string]struct{})
	for _, token := range strings.Split(raw, ",") {
		if strings.TrimSpace(token) == "" {
			continue
		}
		host := normalizeHost(token)
		if host == "" {
			return nil, fmt.Errorf("invalid ALLOWED_HOSTS entry %q", strings.TrimSpace(token))
		}
		hosts[host] = struct{}{}
	}
	return hosts, nil
}

func hostInSet(host string, hosts map[string]struct{}) bool {
	_, ok := hosts[host]
	return ok
}

// ProxyMiddleware は信頼プロキシ経由時のみ Forwarded ヘッダーを反映し、
// FORCE_HTTPS=true かつ http 判定時は https へ301リダイレクトする
func ProxyMiddleware(config *ProxyConfig, next http.Handler) http.Handler {
	if config == nil {
		config = NewProxyConfigFromEnv()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if config.configErr != nil {
			jsonError(w, "Proxy configuration is invalid", http.StatusServiceUnavailable)
			return
		}

		// Host validation is an authentication boundary when ALLOWED_HOSTS is
		// configured. It must happen for every request, including HTTPS requests
		// and requests that are not redirected.
		if r.URL.Path != "/healthz" && len(config.allowedHosts) > 0 && !config.isAllowedHost(r.Host) {
			jsonError(w, "Invalid Host header", http.StatusBadRequest)
			return
		}

		remoteIP := parseRemoteIP(r.RemoteAddr)
		clientIP := ""
		if remoteIP != nil {
			clientIP = remoteIP.String()
		}

		proto := "http"
		if r.TLS != nil {
			proto = "https"
		}

		if config.isTrustedProxy(remoteIP) {
			resolved, err := config.resolveForwardedForStrict(r.Header.Values("X-Forwarded-For"), remoteIP)
			if err != nil {
				jsonError(w, "Invalid X-Forwarded-For header", http.StatusBadRequest)
				return
			}
			if resolved != nil {
				clientIP = resolved.String()
			} else if realIP, err := parseSingleIPHeader(r.Header.Values("X-Real-IP")); err != nil {
				jsonError(w, "Invalid X-Real-IP header", http.StatusBadRequest)
				return
			} else if realIP != nil {
				clientIP = realIP.String()
			}

			forwardedProto, err := parseForwardedProto(r.Header.Values("X-Forwarded-Proto"))
			if err != nil {
				jsonError(w, "Invalid X-Forwarded-Proto header", http.StatusBadRequest)
				return
			}
			if forwardedProto != "" {
				proto = forwardedProto
			}
		} else {
			// Do not leave attacker-controlled forwarding headers for downstream
			// middleware to accidentally trust.
			r.Header.Del("Forwarded")
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-Forwarded-Proto")
			r.Header.Del("X-Real-IP")
		}
		// The application does not consume RFC 7239 Forwarded. Remove it even
		// from trusted requests to prevent ambiguity in downstream handlers.
		r.Header.Del("Forwarded")

		if config.forceHTTPS && proto == "http" && r.URL.Path != "/healthz" {
			redirectHost := config.redirectTargetHost(r.Host)
			if redirectHost == "" {
				jsonError(w, "Bad Request", http.StatusBadRequest)
				return
			}
			targetURL := "https://" + redirectHost + r.URL.RequestURI()
			// #nosec G710 -- redirectHost is either the fixed configured target or
			// an exact member of the startup-validated ALLOWED_HOSTS set.
			http.Redirect(w, r, targetURL, http.StatusMovedPermanently)
			return
		}

		ctx := context.WithValue(r.Context(), clientIPKey, clientIP)
		ctx = context.WithValue(ctx, requestProtoKey, proto)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (c *ProxyConfig) resolveForwardedFor(header string, remoteIP net.IP) net.IP {
	resolved, _ := c.resolveForwardedForStrict([]string{header}, remoteIP)
	return resolved
}

func (c *ProxyConfig) resolveForwardedForStrict(headers []string, remoteIP net.IP) (net.IP, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	if len(headers) != 1 {
		return nil, fmt.Errorf("multiple X-Forwarded-For header values")
	}
	parts := strings.Split(headers[0], ",")
	hops := make([]net.IP, 0, len(parts)+1)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty X-Forwarded-For hop")
		}
		parsed := net.ParseIP(part)
		if parsed == nil {
			return nil, fmt.Errorf("invalid X-Forwarded-For hop")
		}
		hops = append(hops, parsed)
	}
	if remoteIP != nil {
		hops = append(hops, remoteIP)
	}

	for i := len(hops) - 1; i >= 0; i-- {
		if !c.isTrustedProxy(hops[i]) {
			return hops[i], nil
		}
	}
	if len(hops) > 0 {
		return hops[0], nil
	}
	return nil, nil
}

func parseSingleIPHeader(values []string) (net.IP, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("multiple header values")
	}
	ip := net.ParseIP(strings.TrimSpace(values[0]))
	if ip == nil {
		return nil, fmt.Errorf("invalid IP")
	}
	return ip, nil
}

func parseForwardedProto(values []string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("multiple header values")
	}
	p := strings.ToLower(strings.TrimSpace(values[0]))
	if strings.Contains(p, ",") || (p != "http" && p != "https") {
		return "", fmt.Errorf("invalid protocol")
	}
	return p, nil
}

func (c *ProxyConfig) redirectTargetHost(requestHost string) string {
	if c.redirectHost != "" {
		return c.redirectHost
	}
	host := normalizeHost(requestHost)
	if !c.isAllowedHost(host) {
		return ""
	}
	return host
}

func normalizeHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || strings.Contains(raw, "/") || strings.Contains(raw, "@") {
		return ""
	}
	host := raw
	if h, p, err := net.SplitHostPort(raw); err == nil {
		if h == "" || p == "" {
			return ""
		}
		host = h
	} else if strings.Contains(raw, ":") {
		if net.ParseIP(strings.Trim(raw, "[]")) == nil {
			return ""
		}
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return ""
	}
	if strings.Trim(host, "[]") == "" {
		return ""
	}
	return raw
}

func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return net.ParseIP(strings.TrimSpace(host))
}

func jsonError(w http.ResponseWriter, message string, status int) {
	http.Error(w, fmt.Sprintf(`{"error":%q}`, message), status)
}

// ClientIPFromRequest はミドルウェアで解決済みのクライアントIPを返す
func ClientIPFromRequest(r *http.Request) string {
	if v := r.Context().Value(clientIPKey); v != nil {
		if ip, ok := v.(string); ok && strings.TrimSpace(ip) != "" {
			return ip
		}
	}

	ip := ""
	if parsed := parseRemoteIP(r.RemoteAddr); parsed != nil {
		ip = parsed.String()
	}
	if ip == "" {
		ip = "unknown"
	}
	return ip
}

// RequestProto はリクエストのプロトコル（http/https）を返す
func RequestProto(r *http.Request) string {
	if v := r.Context().Value(requestProtoKey); v != nil {
		if proto, ok := v.(string); ok && (proto == "http" || proto == "https") {
			return proto
		}
	}

	if r.TLS != nil {
		return "https"
	}
	return "http"
}
