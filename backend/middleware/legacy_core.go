package middleware

import (
	"context"
	"net/http"

	"omni_money/backend/core"
)

// LegacyCoreServiceMiddleware explicitly binds the historical Desktop/global
// database to a request. It exists only for NewRouterWithError compatibility;
// multi-user server routers must use VaultSessionAuthMiddleware instead.
//
// If the legacy database is unavailable, no service is installed. Financial
// handlers then fail closed with their fixed unavailable response, while auth
// and health endpoints remain usable.
func LegacyCoreServiceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service, err := core.NewLegacyService()
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), coreServiceContextKey{}, service)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
