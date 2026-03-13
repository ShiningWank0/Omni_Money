// Package middleware は認証、AI用APIの接続制御を提供する
package middleware

import (
	"context"
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
}

// NewProxyConfigFromEnv は環境変数からProxyConfigを作成する
func NewProxyConfigFromEnv() *ProxyConfig {
	cfg := &ProxyConfig{
		trustedCIDRs: parseTrustedProxies(os.Getenv("TRUSTED_PROXIES")),
		forceHTTPS:   strings.EqualFold(strings.TrimSpace(os.Getenv("FORCE_HTTPS")), "true"),
	}
	return cfg
}

func parseTrustedProxies(raw string) []*net.IPNet {
	var result []*net.IPNet
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		// CIDR
		if strings.Contains(token, "/") {
			_, ipNet, err := net.ParseCIDR(token)
			if err == nil {
				result = append(result, ipNet)
			}
			continue
		}

		// 単一IPをCIDRに変換
		ip := net.ParseIP(token)
		if ip == nil {
			continue
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
	return result
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

// ProxyMiddleware は信頼プロキシ経由時のみ Forwarded ヘッダーを反映し、
// FORCE_HTTPS=true かつ http 判定時は https へ301リダイレクトする
func ProxyMiddleware(config *ProxyConfig, next http.Handler) http.Handler {
	if config == nil {
		config = NewProxyConfigFromEnv()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
				parts := strings.Split(forwarded, ",")
				for _, part := range parts {
					candidate := strings.TrimSpace(part)
					if parsed := net.ParseIP(candidate); parsed != nil {
						clientIP = parsed.String()
						break
					}
				}
			} else if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
				if parsed := net.ParseIP(realIP); parsed != nil {
					clientIP = parsed.String()
				}
			}

			if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
				parts := strings.Split(forwardedProto, ",")
				p := strings.ToLower(strings.TrimSpace(parts[0]))
				if p == "http" || p == "https" {
					proto = p
				}
			}
		}

		if config.forceHTTPS && proto == "http" {
			targetURL := "https://" + r.Host + r.URL.RequestURI()
			http.Redirect(w, r, targetURL, http.StatusMovedPermanently)
			return
		}

		ctx := context.WithValue(r.Context(), clientIPKey, clientIP)
		ctx = context.WithValue(ctx, requestProtoKey, proto)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return net.ParseIP(strings.TrimSpace(host))
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
