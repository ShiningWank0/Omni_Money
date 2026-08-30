// Package middleware は認証、AI用APIの接続制御を提供する
package middleware

import (
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"omni_money/backend/core"
	"omni_money/backend/fileprivacy"
)

func cleanupCSVSpoolFile(temp *fileprivacy.PrivateTempFile) {
	if temp == nil {
		return
	}
	path := temp.Path
	if err := temp.Cleanup(); err != nil {
		log.Printf("security_event=csv_spool_cleanup_failed path=%q error=%v", path, err)
	}
}

const (
	cspHeaderValue = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:"

	// maxRequestBodySize はリクエストボディの最大サイズ（10MB）
	maxRequestBodySize = 10 * 1024 * 1024
	// Raw CSV v3 may carry the complete image quota as base64. Keep the larger
	// allowance scoped to the authenticated import endpoint; JSON compatibility
	// requests use the smaller cap below. These are fixed wire-size caps, not
	// estimates of JSON escaping overhead (decoded CSV is checked again by the
	// API/core layer).
	maxCSVRequestBodySize = core.MaxCSVImportWireBytes
	// Spooling is intentionally tolerant of slow, authenticated clients. The
	// body is written to a bounded private file before the heavy core slot is
	// acquired; a 15-second deadline would make a valid 512 MiB upload require
	// an unrealistic 34 MiB/s minimum sustained rate.
	csvUploadReadTimeout = 10 * time.Minute
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
		limit := int64(maxRequestBodySize)
		csvBody := false
		if r.Body != nil {
			if isCSVImport {
				// Raw CSV is the primary streaming format.  The JSON shape is a
				// deliberately small compatibility path, so reject a large JSON
				// envelope before json.Decoder allocates its string value.
				mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				switch mediaType {
				case "text/csv", "application/csv", "application/octet-stream":
					limit = maxCSVRequestBodySize
					csvBody = true
				case "application/json":
					limit = core.MaxCSVJSONWireBytes
					csvBody = true
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		if isCSVImport && csvBody && r.Body != nil {
			if r.ContentLength > limit {
				http.Error(w, "CSV request is too large", http.StatusRequestEntityTooLarge)
				return
			}
			// Reserve the full possible private-file size before reading the
			// body. The reservation is shared with image import spools and
			// exports, and is held until cleanup after the downstream handler.
			tempRelease, available := core.TryAcquireCSVTempBudget(limit)
			if !available {
				http.Error(w, "CSV upload spool is busy", http.StatusTooManyRequests)
				return
			}
			defer tempRelease()
			// Large uploads get a route-local read deadline. The server-wide
			// ReadTimeout remains unchanged for ordinary API requests.
			responseController := http.NewResponseController(w)
			_ = responseController.SetReadDeadline(time.Now().Add(csvUploadReadTimeout))
			defer responseController.SetReadDeadline(time.Time{})
			// Authentication middleware wraps this middleware in the production
			// chain. Spool only after authentication, so a slow client consumes
			// bounded private disk space but cannot hold the processing slot.
			temp, err := fileprivacy.CreatePrivateTempFile("omni-money-csv-upload-")
			if err != nil {
				http.Error(w, "CSV upload spool is unavailable", http.StatusInsufficientStorage)
				return
			}
			tmp := temp.File
			cleanup := func() {
				cleanupCSVSpoolFile(temp)
			}
			info, statErr := tmp.Stat()
			if statErr != nil || !fileprivacy.IsPrivate(tmp, info) {
				cleanup()
				http.Error(w, "CSV upload spool is unavailable", http.StatusInsufficientStorage)
				return
			}
			written, copyErr := io.Copy(tmp, r.Body)
			if copyErr != nil {
				cleanup()
				if written >= limit {
					http.Error(w, "CSV request is too large", http.StatusRequestEntityTooLarge)
				} else {
					http.Error(w, "CSV upload failed", http.StatusBadRequest)
				}
				return
			}
			if err := tmp.Sync(); err != nil {
				cleanup()
				http.Error(w, "CSV upload spool failed", http.StatusInsufficientStorage)
				return
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				cleanup()
				http.Error(w, "CSV upload spool failed", http.StatusInsufficientStorage)
				return
			}
			oldBody := r.Body
			r.Body = tmp
			_ = oldBody.Close()
			// Keep the weighted reservation while the private spool remains
			// on disk and while parsed image files may be created.
			defer cleanup()
			release, ok := core.TryAcquireCSVImportSlot()
			if !ok {
				http.Error(w, "CSV import is busy", http.StatusTooManyRequests)
				return
			}
			defer release()
			ctx := core.WithCSVImportReservation(r.Context())
			ctx = core.WithCSVTempReservation(ctx)
			r = r.WithContext(ctx)
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
