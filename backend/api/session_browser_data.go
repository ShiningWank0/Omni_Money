package api

import (
	"net/http"

	"omni_money/backend/middleware"
)

// clearSessionBrowserData expires only Omni Money's own session cookies.
// Clear-Site-Data's "cookies" directive also clears cookies on sibling
// subdomains of the same registered domain, including an authenticating
// reverse proxy's cookies. That can block logout confirmation and navigation.
// Keep the origin's cache/storage cleanup; server-side revocation remains the
// caller's responsibility and must happen before this browser cleanup.
func clearSessionBrowserData(w http.ResponseWriter, r *http.Request, sessions *middleware.SessionManager) {
	sessions.ClearSessionCookie(w, r)
	w.Header().Set("Clear-Site-Data", `"cache", "storage"`)
}
