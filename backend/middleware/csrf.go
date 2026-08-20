package middleware

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

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

		// Login has no authenticated session yet. Exact Origin/Fetch Metadata
		// checks above prevent browser login-CSRF; global/IP limits handle abuse.
		if r.URL.Path == "/api/auth/login" && r.Method == http.MethodPost {
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

// RecentAuthMiddleware requires newly verified Omni credentials for bulk
// export/import, destructive restore, AI-console access, and global logout.
func RecentAuthMiddleware(sessionManager *SessionManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresRecentAuthentication(r) {
			next.ServeHTTP(w, r)
			return
		}
		session, ok := SessionFromContext(r.Context())
		if !ok || !sessionManager.IsRecent(session.ID) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusPreconditionRequired)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error":                "この操作には再認証が必要です",
				"recent_auth_required": true,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requiresRecentAuthentication(r *http.Request) bool {
	switch {
	case r.URL.Path == "/api/backup_csv" && r.Method == http.MethodGet:
		return true
	case r.URL.Path == "/api/import_csv" && r.Method == http.MethodPost:
		return true
	case r.URL.Path == "/api/snapshots/restore" && r.Method == http.MethodPost:
		return true
	case r.URL.Path == "/api/snapshots" && r.Method == http.MethodPost:
		return true
	case r.URL.Path == "/api/auth/logout-all" && r.Method == http.MethodPost:
		return true
	case (r.URL.Path == "/api/ai-console/transactions" || r.URL.Path == "/api/ai-console/analysis") && r.Method == http.MethodPost:
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
