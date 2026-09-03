package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/middleware"
	"omni_money/backend/serverauth"
	"omni_money/backend/vault"
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

type ServerPasskeyService interface {
	BeginPasskeyRegistration(context.Context, string, string) (serverauth.PasskeyRegistrationBegin, error)
	FinishPasskeyRegistration(context.Context, string, serverauth.FinishPasskeyRegistrationInput, time.Time) (control.PasskeySummary, error)
	BeginPasskeyLogin(context.Context, string, string) (serverauth.PasskeyLoginBegin, error)
	FinishPasskeyLogin(context.Context, serverauth.FinishPasskeyLoginInput, time.Time) (*middleware.Session, error)
	BeginPasskeyReauthentication(context.Context, string, string) (serverauth.PasskeyLoginBegin, error)
	FinishPasskeyReauthentication(context.Context, string, serverauth.FinishPasskeyLoginInput, time.Time) error
	ListPasskeys(context.Context, string) ([]control.PasskeySummary, error)
	DeletePasskey(context.Context, string, []byte) error
}

// ServerSnapshotService is the session-bound root operation used only by the
// exact restore route.  Snapshot create/list use the request lease installed
// by VaultSessionAuthMiddleware.
type ServerSnapshotService interface {
	BeginRestore(string) (*vault.RestoreOperation, string, error)
}

var _ ServerPasskeyService = (*serverauth.Service)(nil)

type ServerDependencies struct {
	Accounts  ServerAccountService
	Sessions  *middleware.SessionManager
	Control   ServerControlStore
	Now       func() time.Time
	Snapshots ServerSnapshotService
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
	passkeys, passkeysAvailable := dependencies.Accounts.(ServerPasskeyService)
	if passkeysAvailable {
		mux.HandleFunc("/api/auth/passkeys/login/begin", handlePasskeyLoginBegin(dependencies, passkeys))
		mux.HandleFunc("/api/auth/passkeys/login/finish", handlePasskeyLoginFinish(dependencies, passkeys))
		mux.HandleFunc("/api/auth/passkeys/register/begin", handlePasskeyRegistrationBegin(dependencies, passkeys))
		mux.HandleFunc("/api/auth/passkeys/register/finish", handlePasskeyRegistrationFinish(dependencies, passkeys))
		mux.HandleFunc("/api/auth/passkeys/reauth/begin", handlePasskeyReauthenticationBegin(dependencies, passkeys))
		mux.HandleFunc("/api/auth/passkeys/reauth/finish", handlePasskeyReauthenticationFinish(dependencies, passkeys))
		mux.HandleFunc("/api/auth/passkeys", handlePasskeyList(passkeys))
		mux.HandleFunc("/api/auth/passkeys/", handlePasskeyDelete(passkeys))
	}

	mux.HandleFunc("/api/admin/users", handleServerUsers(dependencies))
	mux.HandleFunc("/api/admin/users/", handleServerUserAction(dependencies))
	mux.HandleFunc("/api/admin/invitations", handleServerInvitationCreation(dependencies))
	mux.HandleFunc("/api/admin/password-resets", handleServerPasswordResetCreation(dependencies))

	registerFinancialRoutes(mux)
	if dependencies.Snapshots == nil {
		// Keep a fail-closed boundary for test/degraded configurations that do
		// not provide the root-only manager capability.
		mux.HandleFunc("/api/snapshots", handleServerFeatureUnavailable)
		mux.HandleFunc("/api/snapshots/restore", handleServerFeatureUnavailable)
	} else {
		mux.HandleFunc("/api/snapshots", handleServerSnapshots)
		mux.HandleFunc("/api/snapshots/restore", handleServerSnapshotRestore(dependencies))
	}
	// Static AI credentials are not bound to a UserID/VaultID/DEK. The
	// multi-user server keeps both the console relay and AI listener absent.
	mux.HandleFunc("/api/ai-console/", http.NotFound)
	mux.HandleFunc("/api/v1/ai/", http.NotFound)

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if _, err := dependencies.Control.IsBootstrapped(r.Context()); err != nil {
			jsonResponse(w, map[string]string{"status": "unavailable"}, http.StatusServiceUnavailable)
			return
		}
		jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
	})

	var handler http.Handler = mux
	handler = middleware.MaxBodySizeMiddleware(handler)
	// VaultSessionAuthMiddleware is the outermost layer and installs only
	// identity/current-user context for exact restore. CSRF and recent-auth
	// then run, and ordinary requests borrow a child lease inside that layer.
	handler = middleware.RecentAuthMiddleware(dependencies.Sessions, handler)
	handler = middleware.CSRFMiddleware(dependencies.Sessions, handler)
	handler = middleware.VaultSessionAuthMiddleware(dependencies.Sessions, dependencies.Control, handler)
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
