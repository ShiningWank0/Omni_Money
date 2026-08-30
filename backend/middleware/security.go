// Package middleware は認証、AI用APIの接続制御を提供する
package middleware

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"omni_money/backend/core"
)

const (
	cspHeaderValue = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:"

	// maxRequestBodySize はリクエストボディの最大サイズ（10MB）
	maxRequestBodySize = 10 * 1024 * 1024
	// CSV v3 may carry the complete image quota as base64. Keep the larger
	// allowance scoped to the authenticated import endpoint; every other route
	// keeps the normal small request budget. This is a fixed wire-size cap; it is
	// not an estimate of JSON escaping overhead (decoded CSV is checked again by
	// the API/core layer).
	maxCSVRequestBodySize = core.MaxCSVImportWireBytes
)

// SecurityHeadersMiddleware はセキュリティヘッダーを全レスポンスに付与する
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", cspHeaderValue)
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		if RequestProto(r) == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// MaxBodySizeMiddleware はリクエストボディサイズを制限しDoS攻撃を緩和する
func MaxBodySizeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isCSVImport := r.URL.Path == "/api/import_csv"
		if isCSVImport {
			release, ok := core.TryAcquireCSVImportSlot()
			if !ok {
				http.Error(w, "CSV import is busy", http.StatusTooManyRequests)
				return
			}
			defer release()
			r = r.WithContext(core.WithCSVImportReservation(r.Context()))
		}
		if r.Body != nil {
			limit := int64(maxRequestBodySize)
			if isCSVImport {
				limit = maxCSVRequestBodySize
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// CORSMiddleware は環境変数CORS_ALLOWED_ORIGINSに基づきCORSを制御する
func CORSMiddleware(next http.Handler) http.Handler {
	allowedOrigins := parseAllowedOrigins(os.Getenv("CORS_ALLOWED_ORIGINS"))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			if !isOriginAllowed(origin, r, allowedOrigins) {
				w.WriteHeader(http.StatusForbidden)
				return
			}

			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-CSRF-Token")
			w.Header().Set("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func parseAllowedOrigins(raw string) map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" || token == "*" {
			continue
		}
		allowed[token] = struct{}{}
	}
	return allowed
}

func isOriginAllowed(origin string, r *http.Request, allowedOrigins map[string]struct{}) bool {
	if len(allowedOrigins) > 0 {
		_, ok := allowedOrigins[origin]
		return ok
	}

	originURL, err := url.Parse(origin)
	if err != nil {
		return false
	}

	if !strings.EqualFold(originURL.Host, r.Host) {
		return false
	}

	return strings.EqualFold(originURL.Scheme, RequestProto(r))
}
