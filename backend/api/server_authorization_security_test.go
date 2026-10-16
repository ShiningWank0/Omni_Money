package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/middleware"
	"omni_money/backend/serverauth"
	"omni_money/backend/vault"
)

// Embedding the passkey interface exposes the production passkey routes. Only
// DeletePasskey is expected in this matrix; an unexpected dispatch fails loudly.
type authorizationSecurityAccounts struct {
	fakeServerAccounts
	ServerPasskeyService
	calls  int
	actor  string
	target string
}

func (a *authorizationSecurityAccounts) record(actor, target string) {
	a.calls++
	a.actor = actor
	a.target = target
}
func (a *authorizationSecurityAccounts) CreateInvitation(_ context.Context, actor, email string, role control.Role, expiry, now time.Time) (control.Invitation, string, error) {
	a.record(actor, email)
	return control.Invitation{Email: email, Role: role, ExpiresAt: expiry}, "test-invitation-secret", nil
}
func (a *authorizationSecurityAccounts) CreatePasswordReset(_ context.Context, actor, target string, expiry, now time.Time) (control.PasswordResetTicket, string, error) {
	a.record(actor, target)
	return control.PasswordResetTicket{UserID: target, ExpiresAt: expiry}, "test-reset-secret", nil
}
func (a *authorizationSecurityAccounts) DisableUser(_ context.Context, actor, target string, _ time.Time) error {
	a.record(actor, target)
	return nil
}
func (a *authorizationSecurityAccounts) ChangePassword(_ context.Context, user string, _, _ []byte, _ bool, _ time.Time) (int, error) {
	a.record(user, user)
	return 0, nil
}
func (a *authorizationSecurityAccounts) RotateRecoveryCode(_ context.Context, user string, _, _ []byte, _ time.Time) error {
	a.record(user, user)
	return nil
}
func (*authorizationSecurityAccounts) ListCredentials(context.Context, string) (serverauth.CredentialInventory, error) {
	return serverauth.CredentialInventory{}, nil
}
func (a *authorizationSecurityAccounts) DeletePasskey(_ context.Context, user string, credential []byte) error {
	a.record(user, string(credential))
	return nil
}

var authorizationSecurityRoutes = []struct {
	name, method, path, body, target string
	admin                            bool
	status                           int
}{
	{"invitation", http.MethodPost, "/api/admin/invitations", `{"email":"invitee@example.test","role":"user"}`, "invitee@example.test", true, http.StatusCreated},
	{"password reset", http.MethodPost, "/api/admin/password-resets", `{"target_user_id":"target-user"}`, "target-user", true, http.StatusCreated},
	{"disable user", http.MethodPost, "/api/admin/users/target-user/disable", `{}`, "target-user", true, http.StatusOK},
	{"change password", http.MethodPost, "/api/auth/password", `{"current_password_b64":"Y3VycmVudA==","new_password_b64":"cmVwbGFjZW1lbnQ="}`, "self", false, http.StatusOK},
	{"rotate recovery", http.MethodPost, "/api/auth/recovery-code", `{"current_password_b64":"Y3VycmVudA==","new_recovery_secret_b64":"cmVwbGFjZW1lbnQ="}`, "self", false, http.StatusOK},
	{"delete passkey", http.MethodDelete, "/api/auth/passkeys/Y3JlZGVudGlhbA", ``, "credential", false, http.StatusOK},
}

func TestProductionServerSecurityRejectsAnonymousMutationsBeforeService(t *testing.T) {
	for _, route := range authorizationSecurityRoutes {
		t.Run(route.name, func(t *testing.T) {
			accounts := &authorizationSecurityAccounts{}
			setAuthorizationSecurityEnv(t)
			sessions := middleware.NewSessionManagerWithConfig(middleware.DefaultSessionConfig())
			t.Cleanup(sessions.Close)
			handler, err := NewServerRouter(ServerDependencies{Accounts: accounts, Sessions: sessions, Control: &fakeServerControl{bootstrapped: true}})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, serverJSONRequest(t, route.method, route.path, route.body))
			if response.Code != http.StatusUnauthorized || accounts.calls != 0 {
				t.Fatalf("status=%d service calls=%d body=%s", response.Code, accounts.calls, response.Body.String())
			}
		})
	}
}

func setAuthorizationSecurityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ALLOWED_HOSTS", "money.example.test")
	t.Setenv("FORCE_HTTPS", "false")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("HTTPS_REDIRECT_HOST", "")
}

func TestProductionServerSecurityAuthorizationMatrix(t *testing.T) {
	cases := []struct {
		name                                               string
		role                                               control.Role
		demoted, disabled, invalidCSRF, stale, wrongMethod bool
	}{
		{name: "admin", role: control.RoleAdmin},
		{name: "ordinary user", role: control.RoleUser},
		{name: "demoted admin", role: control.RoleAdmin, demoted: true},
		{name: "disabled admin", role: control.RoleAdmin, disabled: true},
		{name: "invalid CSRF", role: control.RoleAdmin, invalidCSRF: true},
		{name: "stale recent authentication", role: control.RoleAdmin, stale: true},
		{name: "wrong method", role: control.RoleAdmin, wrongMethod: true},
	}
	for _, route := range authorizationSecurityRoutes {
		t.Run(route.name, func(t *testing.T) {
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					setAuthorizationSecurityEnv(t)
					user := control.UserSummary{ID: "user_01HZX7CYK3XPSJ0HE8P2RQ7V4M", Email: "actor@example.test", Role: test.role, State: control.UserActive}
					store := &serverCSVControlStore{users: map[string]control.UserSummary{user.ID: user}}
					manager, err := vault.NewManager(filepath.Join(t.TempDir(), "vaults"))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := manager.Close(context.Background()); err != nil {
							t.Error(err)
						}
					})
					root := acquireServerCSVVault(t, manager, user.ID, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M", 0x48)
					recentAge := 10 * time.Minute
					if test.stale {
						// Expire only the step-up window without a wall-clock sleep.
						recentAge = time.Nanosecond
					}
					sessions := middleware.NewSessionManagerWithConfig(middleware.SessionConfig{MaxAge: time.Hour, IdleTimeout: 30 * time.Minute, RecentAuthAge: recentAge, MaxConcurrent: 3})
					t.Cleanup(sessions.Close)
					session, err := sessions.CreateVaultSession(user, root)
					if err != nil {
						root.Release()
						t.Fatal(err)
					}
					if test.demoted {
						current := user
						current.Role = control.RoleUser
						store.users[user.ID] = current
					}
					if test.disabled {
						current := user
						current.State = control.UserDisabled
						store.users[user.ID] = current
					}
					accounts := &authorizationSecurityAccounts{}
					handler, err := NewServerRouter(ServerDependencies{Accounts: accounts, Sessions: sessions, Control: store})
					if err != nil {
						t.Fatal(err)
					}
					method := route.method
					if test.wrongMethod {
						method = http.MethodPut
					}
					request := serverJSONRequest(t, method, route.path, route.body)
					request.AddCookie(&http.Cookie{Name: middleware.SecureSessionCookieName, Value: session.ID})
					request.Header.Set(middleware.CSRFHeaderName, session.CSRFToken)
					if test.invalidCSRF {
						request.Header.Set(middleware.CSRFHeaderName, "invalid")
					}
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					want := route.status
					switch {
					case test.disabled:
						want = http.StatusUnauthorized
					case test.invalidCSRF:
						want = http.StatusForbidden
					case test.stale:
						want = http.StatusPreconditionRequired
					case test.wrongMethod:
						want = http.StatusMethodNotAllowed
					case route.admin && (test.role == control.RoleUser || test.demoted):
						want = http.StatusForbidden
					}
					if response.Code != want {
						t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
					}
					if want >= 400 {
						if test.disabled {
							if _, exists := sessions.GetSession(session.ID); exists {
								t.Fatal("disabled user session was not revoked")
							}
						}
						if accounts.calls != 0 {
							t.Fatalf("rejected request invoked %d mutations", accounts.calls)
						}
						return
					}
					target := route.target
					if target == "self" {
						target = user.ID
					}
					if accounts.calls != 1 || accounts.actor != user.ID || accounts.target != target {
						t.Fatalf("service calls=%d actor=%q target=%q; want one call for %q / %q", accounts.calls, accounts.actor, accounts.target, user.ID, target)
					}
					if test.demoted && session.Role != control.RoleAdmin {
						t.Fatal("fixture lost the stale admin session role")
					}
				})
			}
		})
	}
}
