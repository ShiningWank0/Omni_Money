package middleware

import (
	"net/http"
	"strings"
)

const (
	privateNoStore  = "no-store, private, max-age=0"
	staticImmutable = "public, max-age=31536000, immutable"
)

// CacheControlMiddleware prevents financial and authentication responses from
// being retained by browsers or intermediary caches. Fingerprinted frontend
// assets remain cacheable, while HTML is always revalidated.
func CacheControlMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		applyPolicy := cachePolicyForPath(r.URL.Path)
		applyPolicy(w.Header())
		next.ServeHTTP(&cacheResponseWriter{
			ResponseWriter: w,
			applyPolicy:    applyPolicy,
		}, r)
	})
}

func cachePolicyForPath(path string) func(http.Header) {
	return func(header http.Header) {
		switch {
		case strings.HasPrefix(path, "/api/"):
			header.Set("Cache-Control", privateNoStore)
			header.Set("Pragma", "no-cache")
			header.Set("Expires", "0")
			header.Set("Surrogate-Control", "no-store")
		case strings.HasPrefix(path, "/assets/"):
			header.Set("Cache-Control", staticImmutable)
		case path != "/healthz":
			header.Set("Cache-Control", "no-cache")
		}
	}
}

// cacheResponseWriter reapplies the authoritative policy immediately before
// headers are committed. This keeps helpers such as http.ServeFile from
// accidentally weakening or deleting the policy.
type cacheResponseWriter struct {
	http.ResponseWriter
	applyPolicy func(http.Header)
	wroteHeader bool
}

func (w *cacheResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.applyPolicy(w.Header())
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *cacheResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *cacheResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
