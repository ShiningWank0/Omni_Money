package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

type recentAuthCheckContextKey struct{}

// RevalidateRecentAuthentication performs the second, post-spool check for a
// sensitive request. The boolean reports whether RecentAuthMiddleware
// installed a check; callers outside that middleware remain compatible.
func RevalidateRecentAuthentication(ctx context.Context) (checked, recent bool) {
	if ctx == nil {
		return false, true
	}
	check, ok := ctx.Value(recentAuthCheckContextKey{}).(func() bool)
	if !ok || check == nil {
		return false, true
	}
	return true, check()
}

// CSRFMiddleware combines a session-bound synchronizer token with strict
// Origin and Fetch Metadata checks. SameSite cookies remain a third layer.
func CSRFMiddleware(sessionManager *SessionManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isUnsafeMethod(r.Method) || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if !validBrowserRequestBoundary(r) {
			writeCSRFRejected(w)
			return
		}

		// Public account bootstrap/login/recovery calls have no session token.
		// Exact Origin/Fetch Metadata checks above still prevent browser CSRF;
		// possession of the setup/invitation/reset secret authorizes its flow.
		if isPublicServerAuthRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		session, ok := SessionFromContext(r.Context())
		if !ok {
			writeCSRFRejected(w)
			return
		}
		values := r.Header.Values(CSRFHeaderName)
		if len(values) != 1 || strings.Contains(values[0], ",") {
			writeCSRFRejected(w)
			return
		}
		token := values[0]
		if !sessionManager.ValidateCSRF(session.ID, token) {
			writeCSRFRejected(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func validBrowserRequestBoundary(r *http.Request) bool {
	fetchSite := r.Header.Values("Sec-Fetch-Site")
	if len(fetchSite) > 1 {
		return false
	}
	if len(fetchSite) == 1 {
		switch strings.ToLower(strings.TrimSpace(fetchSite[0])) {
		case "same-origin", "none":
		case "same-site", "cross-site", "":
			return false
		default:
			return false
		}
	}

	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		// Non-browser clients remain usable, but authenticated unsafe calls still
		// require the unguessable session-bound token.
		return true
	}
	if len(origins) != 1 || strings.Contains(origins[0], ",") {
		return false
	}
	origin, err := url.Parse(strings.TrimSpace(origins[0]))
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil ||
		origin.Opaque != "" || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return false
	}
	return strings.EqualFold(origin.Scheme, RequestProto(r)) && strings.EqualFold(origin.Host, r.Host)
}

func writeCSRFRejected(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":         "リクエストの検証に失敗しました",
		"csrf_rejected": true,
	})
}

// RecentAuthMiddleware requires newly verified Omni credentials only for
// explicitly high-impact operations. Ordinary financial reads and CRUD remain
// available to a valid session; high-impact export/import, snapshots, global
// logout, and AI-console operations require recent authentication.
func RecentAuthMiddleware(sessionManager *SessionManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresRecentAuthentication(r) {
			next.ServeHTTP(w, r)
			return
		}
		session, ok := SessionFromContext(r.Context())
		if !ok || !sessionManager.IsRecent(session.ID) {
			writeRecentAuthRequired(w)
			return
		}
		// The initial check protects the body spool. Install a callback so the
		// downstream handler can revalidate after a slow spool and immediately
		// before any destructive DB mutation.
		ctx := context.WithValue(r.Context(), recentAuthCheckContextKey{}, func() bool {
			return sessionManager.IsRecent(session.ID)
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeRecentAuthRequired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusPreconditionRequired)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":                "この操作には再認証が必要です",
		"recent_auth_required": true,
	})
}

func requiresRecentAuthentication(r *http.Request) bool {
	path := r.URL.Path
	method := r.Method
	switch {
	case path == "/api/backup_csv" && method == http.MethodGet:
		return true
	case path == "/api/import_csv" && method == http.MethodPost:
		return true
	case path == "/api/snapshots/restore" && method == http.MethodPost:
		return true
	case path == "/api/snapshots" && method == http.MethodPost:
		return true
	case path == "/api/auth/logout-all" && method == http.MethodPost:
		return true
	case (path == "/api/auth/password" || path == "/api/auth/recovery-code") && method == http.MethodPost:
		return true
	case strings.HasPrefix(path, "/api/auth/passkeys/") && method == http.MethodDelete:
		return true
	case strings.HasPrefix(path, "/api/admin/") && isUnsafeMethod(method):
		return true
	case (path == "/api/ai-console/transactions" || path == "/api/ai-console/analysis") && method == http.MethodPost:
		return true
	default:
		return false
	}
}

// NoStoreAPIMiddleware prevents browsers and intermediary caches from keeping
// authenticated financial or token-bearing API responses.
func NoStoreAPIMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Pragma", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}
