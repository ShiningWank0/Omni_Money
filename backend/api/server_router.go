package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/middleware"
	"omni_money/backend/serverauth"
)

// ServerAccountService is the account-plane capability exposed to the HTTP
// boundary. It deliberately has no method that returns a vault key or a
// financial database handle.
type ServerAccountService interface {
	Bootstrap(context.Context, serverauth.BootstrapInput, time.Time) (control.UserSummary, error)
	AcceptInvitation(context.Context, serverauth.AcceptInvitationInput, time.Time) (control.UserSummary, error)
	Login(context.Context, string, []byte, time.Time) (*middleware.Session, error)
	Reauthenticate(context.Context, string, []byte, time.Time) error
	CreateInvitation(context.Context, string, string, control.Role, time.Time, time.Time) (control.Invitation, string, error)
	CreatePasswordReset(context.Context, string, string, time.Time, time.Time) (control.PasswordResetTicket, string, error)
	CompletePasswordReset(context.Context, serverauth.CompletePasswordResetInput, time.Time) (control.PasswordResetTicket, error)
	DisableUser(context.Context, string, string, time.Time) error
}

// ServerControlStore is intentionally limited to non-secret control-plane
// projections needed by HTTP. UserSummary contains no vault path, financial
// metadata, credential envelope, or password material.
type ServerControlStore interface {
	middleware.CurrentUserStore
	IsBootstrapped(context.Context) (bool, error)
	ListUsers(context.Context) ([]control.UserSummary, error)
}

type ServerDependencies struct {
	Accounts ServerAccountService
	Sessions *middleware.SessionManager
	Control  ServerControlStore
	Now      func() time.Time
}

func (d ServerDependencies) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

// NewServerRouter constructs the production multi-user router. Unlike the
// legacy NewRouterWithError constructor, it never reads AUTH_PASSWORD_HASH and
// never installs a package-global database service.
func NewServerRouter(dependencies ServerDependencies) (http.Handler, error) {
	if dependencies.Accounts == nil || dependencies.Sessions == nil || dependencies.Control == nil {
		return nil, errors.New("server router dependencies are required")
	}
	proxyConfig := middleware.NewProxyConfigFromEnv()
	if err := proxyConfig.Validate(); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/login", handleLoginPage)
	mux.HandleFunc("/login/", handleLoginPage)
	mux.Handle("/", http.FileServer(http.Dir("frontend/dist")))

	mux.HandleFunc("/api/auth/setup", handleServerBootstrap(dependencies))
	mux.HandleFunc("/api/auth/login", handleServerLogin(dependencies))
	mux.HandleFunc("/api/auth/status", handleServerAuthStatus(dependencies))
	mux.HandleFunc("/api/auth/invitations/accept", handleServerInvitationAcceptance(dependencies))
	mux.HandleFunc("/api/auth/password-reset/complete", handleServerPasswordResetCompletion(dependencies))
	mux.HandleFunc("/api/auth/logout", handleServerLogout(dependencies))
	mux.HandleFunc("/api/auth/logout-all", handleServerLogoutAll(dependencies))
	mux.HandleFunc("/api/auth/reauth", handleServerReauthentication(dependencies))
	mux.HandleFunc("/api/auth/keepalive", handleAuthKeepalive)

	mux.HandleFunc("/api/admin/users", handleServerUsers(dependencies))
	mux.HandleFunc("/api/admin/users/", handleServerUserAction(dependencies))
	mux.HandleFunc("/api/admin/invitations", handleServerInvitationCreation(dependencies))
	mux.HandleFunc("/api/admin/password-resets", handleServerPasswordResetCreation(dependencies))

	registerFinancialRoutes(mux)
	// Snapshot restore needs a manager-exclusive operation that does not exist
	// yet. Never route it through the legacy global database in server mode.
	mux.HandleFunc("/api/snapshots", handleServerFeatureUnavailable)
	mux.HandleFunc("/api/snapshots/restore", handleServerFeatureUnavailable)
	// Static AI credentials are not bound to a UserID/VaultID/DEK. The
	// multi-user server keeps both the console relay and AI listener absent.
	mux.HandleFunc("/api/ai-console/", http.NotFound)
	mux.HandleFunc("/api/v1/ai/", http.NotFound)

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
	})

	var handler http.Handler = mux
	handler = middleware.RecentAuthMiddleware(dependencies.Sessions, handler)
	handler = middleware.CSRFMiddleware(dependencies.Sessions, handler)
	handler = middleware.VaultSessionAuthMiddleware(dependencies.Sessions, dependencies.Control, handler)
	handler = middleware.MaxBodySizeMiddleware(handler)
	handler = middleware.RateLimitMiddleware(handler)
	handler = middleware.CORSMiddleware(handler)
	handler = middleware.NoStoreAPIMiddleware(handler)
	handler = middleware.SecurityHeadersMiddleware(handler)
	handler = middleware.ProxyMiddleware(proxyConfig, handler)
	handler = middleware.CacheControlMiddleware(handler)
	return handler, nil
}

func handleServerFeatureUnavailable(w http.ResponseWriter, _ *http.Request) {
	jsonError(w, "この機能はサーバーモードではまだ利用できません", http.StatusServiceUnavailable)
}
