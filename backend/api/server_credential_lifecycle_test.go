package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/middleware"
	"omni_money/backend/serverauth"
	"omni_money/backend/vault"
)

type lifecycleServerAccounts struct {
	fakeServerAccounts
	currentPassword []byte
	newPassword     []byte
	revokePasskeys  bool
	newRecovery     []byte
}

func (a *lifecycleServerAccounts) ChangePassword(_ context.Context, _ string, current, replacement []byte, revoke bool, _ time.Time) (int, error) {
	a.currentPassword = append([]byte(nil), current...)
	a.newPassword = append([]byte(nil), replacement...)
	a.revokePasskeys = revoke
	return 3, nil
}

func (a *lifecycleServerAccounts) RotateRecoveryCode(_ context.Context, _ string, current, recovery []byte, _ time.Time) error {
	a.currentPassword = append([]byte(nil), current...)
	a.newRecovery = append([]byte(nil), recovery...)
	return nil
}

func (*lifecycleServerAccounts) ListCredentials(context.Context, string) (serverauth.CredentialInventory, error) {
	now := time.Date(2026, 8, 31, 1, 2, 3, 0, time.UTC)
	return serverauth.CredentialInventory{
		PasswordUpdatedAt: now,
		RecoveryCreatedAt: now.Add(-time.Hour),
		Passkeys:          []control.PasskeySummary{{ID: "public-id", Name: "Laptop", CreatedAt: now.Add(-2 * time.Hour)}},
	}, nil
}

func TestServerCredentialLifecycleRoutesUseAuthenticatedUserAndNeverExposeSecrets(t *testing.T) {
	var auditOutput bytes.Buffer
	originalLogOutput := log.Writer()
	log.SetOutput(&auditOutput)
	t.Cleanup(func() { log.SetOutput(originalLogOutput) })

	t.Setenv("ALLOWED_HOSTS", "money.example.test")
	t.Setenv("FORCE_HTTPS", "false")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("HTTPS_REDIRECT_HOST", "")
	user := control.UserSummary{ID: "user_01HZX7CYK3XPSJ0HE8P2RQ7V4M", Email: "user@example.test", DisplayName: "User", Role: control.RoleUser, State: control.UserActive}
	store := &serverCSVControlStore{users: map[string]control.UserSummary{user.ID: user}}
	manager, err := vault.NewManager(filepath.Join(t.TempDir(), "vaults"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	root := acquireServerCSVVault(t, manager, user.ID, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M", 0x54)
	sessions := middleware.NewSessionManagerWithConfig(middleware.SessionConfig{MaxAge: time.Hour, IdleTimeout: 30 * time.Minute, RecentAuthAge: 10 * time.Minute, MaxConcurrent: 3})
	t.Cleanup(sessions.Close)
	session, err := sessions.CreateVaultSession(user, root)
	if err != nil {
		root.Release()
		t.Fatal(err)
	}
	accounts := &lifecycleServerAccounts{}
	handler, err := NewServerRouter(ServerDependencies{Accounts: accounts, Sessions: sessions, Control: store, Now: func() time.Time { return time.Date(2026, 8, 31, 2, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}

	passwordBody, _ := json.Marshal(map[string]interface{}{"current_password_b64": []byte("current-password"), "new_password_b64": []byte("replacement-password"), "revoke_passkeys": true})
	response := serveServerCSVRequest(t, handler, session, http.MethodPost, "/api/auth/password", passwordBody)
	if response.Code != http.StatusOK {
		t.Fatalf("password status=%d body=%s", response.Code, response.Body.String())
	}
	if !bytes.Equal(accounts.currentPassword, []byte("current-password")) || !bytes.Equal(accounts.newPassword, []byte("replacement-password")) || !accounts.revokePasskeys {
		t.Fatal("password lifecycle payload changed")
	}
	if !stringsContain(response.Header().Values("Set-Cookie"), "Max-Age=0") {
		t.Fatal("password change did not clear the session cookie")
	}
	audit := auditOutput.String()
	for _, forbidden := range []string{"current-password", "replacement-password", user.ID, user.Email} {
		if bytes.Contains([]byte(audit), []byte(forbidden)) {
			t.Fatalf("credential route audit exposed %q: %s", forbidden, audit)
		}
	}
	for _, required := range []string{
		"security_event=server_password_changed",
		"actor_ref=\"" + auditAccountReference(user.ID) + "\"",
		"target_ref=\"" + auditAccountReference(user.ID) + "\"",
	} {
		if !bytes.Contains([]byte(audit), []byte(required)) {
			t.Fatalf("credential route audit missing %q: %s", required, audit)
		}
	}

	inventory := serveServerCSVRequest(t, handler, session, http.MethodGet, "/api/auth/credentials", nil)
	if inventory.Code != http.StatusOK {
		t.Fatalf("inventory status=%d body=%s", inventory.Code, inventory.Body.String())
	}
	for _, forbidden := range []string{"verifier", "envelope", "salt", "public_key", "vault"} {
		if bytes.Contains(bytes.ToLower(inventory.Body.Bytes()), []byte(forbidden)) {
			t.Fatalf("credential inventory exposed %q: %s", forbidden, inventory.Body.String())
		}
	}
}

func TestCredentialMutationAuditUsesSafeReferencesAndNoSecrets(t *testing.T) {
	var output bytes.Buffer
	original := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(original) })

	actor := "user_actor_01HZX7CYK3XPSJ0HE8P2RQ7V4M"
	target := "user_target_01HZX7CYK3XPSJ0HE8P2RQ7V4M"
	auditCredentialMutation("server_password_reset_completed", "192.0.2.10", actor, target)

	audit := output.String()
	for _, forbidden := range []string{actor, target} {
		if bytes.Contains([]byte(audit), []byte(forbidden)) {
			t.Fatalf("credential audit exposed %q: %s", forbidden, audit)
		}
	}
	for _, required := range []string{
		"security_event=server_password_reset_completed",
		"actor_ref=\"" + auditAccountReference(actor) + "\"",
		"target_ref=\"" + auditAccountReference(target) + "\"",
	} {
		if !bytes.Contains([]byte(audit), []byte(required)) {
			t.Fatalf("credential audit missing %q: %s", required, audit)
		}
	}
}

func stringsContain(values []string, part string) bool {
	for _, value := range values {
		if bytes.Contains([]byte(value), []byte(part)) {
			return true
		}
	}
	return false
}
