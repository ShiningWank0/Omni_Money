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

// RecentAuthMiddleware requires newly verified Omni credentials for all
// financial data access and mutations, as well as bulk export/import,
// destructive restore, AI-console access, and global logout. A stolen but
// otherwise valid session therefore cannot be used indefinitely to read or
// modify the household's financial record without another password (and,
// when configured, TOTP) check.
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
	path := r.URL.Path
	method := r.Method
	switch {
	case path == "/api/backup_csv" && method == http.MethodGet:
		return true
	case path == "/api/import_csv" && method == http.MethodPost:
		return true
	case path == "/api/snapshots/restore" && method == http.MethodPost:
		return true
	case path == "/api/snapshots" && (method == http.MethodGet || method == http.MethodPost):
		return true
	case path == "/api/auth/logout-all" && method == http.MethodPost:
		return true
	case (path == "/api/ai-console/transactions" || path == "/api/ai-console/analysis") && method == http.MethodPost:
		return true
	case (path == "/api/accounts" || path == "/api/items" ||
		path == "/api/balance_history" || path == "/api/balance_history_filtered") && method == http.MethodGet:
		return true
	case path == "/api/transactions" && (method == http.MethodGet || method == http.MethodPost):
		return true
	case hasRoutePrefix(path, "/api/transactions/") &&
		(method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete):
		return true
	case (path == "/api/credit_card_settings" || path == "/api/bank_account_settings") &&
		(method == http.MethodGet || method == http.MethodPost):
		return true
	case hasRoutePrefix(path, "/api/transaction_images/") &&
		(method == http.MethodGet || method == http.MethodPost || method == http.MethodDelete):
		return true
	case path == "/api/tags" && (method == http.MethodGet || method == http.MethodPost):
		return true
	case path == "/api/tags/path" && method == http.MethodPost:
		return true
	case path == "/api/tags/summary" && method == http.MethodGet:
		return true
	case hasRoutePrefix(path, "/api/tags/") &&
		(method == http.MethodPut || method == http.MethodDelete):
		return true
	case hasRoutePrefix(path, "/api/transaction_tags/") &&
		(method == http.MethodGet || method == http.MethodPost || method == http.MethodDelete):
		return true
	case hasRoutePrefix(path, "/api/transaction_links/") &&
		(method == http.MethodGet || method == http.MethodPost || method == http.MethodDelete):
		return true
	default:
		return false
	}
}

// hasRoutePrefix matches a path parameter route while avoiding lookalike
// endpoints such as /api/transactions (which have their own method matrix).
func hasRoutePrefix(path, prefix string) bool {
	return strings.HasPrefix(path, prefix) && len(path) > len(prefix)
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
