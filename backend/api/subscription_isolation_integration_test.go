package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/core"
	"omni_money/backend/middleware"
	"omni_money/backend/models"
	"omni_money/backend/vault"
)

func TestProductionSubscriptionsStayInTheAuthenticatedEncryptedVault(t *testing.T) {
	setAuthorizationSecurityEnv(t)
	manager, err := vault.NewManager(filepath.Join(t.TempDir(), "vaults"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	sessions := middleware.NewSessionManagerWithConfig(middleware.SessionConfig{MaxAge: time.Hour, IdleTimeout: 30 * time.Minute, RecentAuthAge: 10 * time.Minute, MaxConcurrent: 3})
	t.Cleanup(sessions.Close)
	users := []control.UserSummary{
		{ID: "user_01HZX7CYK3XPSJ0HE8P2RQ7V4M", Email: "a@example.test", Role: control.RoleAdmin, State: control.UserActive},
		{ID: "user_01HZX7CYK3XPSJ0HE8P2RQ7V4N", Email: "b@example.test", Role: control.RoleUser, State: control.UserActive},
	}
	store := &serverCSVControlStore{users: map[string]control.UserSummary{users[0].ID: users[0], users[1].ID: users[1]}}
	var logins [2]*middleware.Session
	var rules [2]*core.SubscriptionRule
	date := time.Now().UTC().AddDate(1, 0, 0)
	for i, user := range users {
		vaultID := strings.Replace(user.ID, "user_", "vault_", 1)
		root := acquireServerCSVVault(t, manager, user.ID, vaultID, byte(0x71+i))
		t.Cleanup(root.Release)
		service, err := root.Service()
		if err != nil {
			t.Fatal(err)
		}
		item := []string{"only-user-a", "only-user-b"}[i]
		source, err := service.AddTransaction(models.TransactionRequest{Account: "same-account", Item: item, Date: "2026-01-01", Type: "expense", Amount: 100})
		if err != nil {
			t.Fatal(err)
		}
		rules[i], err = service.SaveSubscription("", core.SubscriptionInput{SourceTransactionID: source.ID, Account: "same-account", Item: item, Currency: "JPY", OriginalAmount: "100", Interval: "monthly", AnchorDay: date.Day(), AnchorMonth: int(date.Month()), NextDate: date.Format(time.DateOnly), Timezone: "UTC", Enabled: true, Tags: []int64{}})
		if err != nil {
			t.Fatal(err)
		}
		logins[i], err = sessions.CreateVaultSession(user, root)
		if err != nil {
			t.Fatal(err)
		}
	}
	handler, err := NewServerRouter(ServerDependencies{Accounts: &fakeServerAccounts{}, Sessions: sessions, Control: store})
	if err != nil {
		t.Fatal(err)
	}
	for i, session := range logins {
		response := serveServerCSVRequest(t, handler, session, http.MethodGet, "/api/subscriptions", nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), rules[i].Item) || strings.Contains(response.Body.String(), rules[1-i].Item) {
			t.Fatalf("vault isolation=%d %s", response.Code, response.Body.String())
		}
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			path := "/api/subscriptions/" + rules[1-i].ID
			var body []byte
			if method == http.MethodGet {
				path += "/transactions"
			} else {
				body, _ = json.Marshal(rules[1-i].SubscriptionInput)
			}
			response = serveServerCSVRequest(t, handler, session, method, path, body)
			if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), rules[1-i].Item) {
				t.Fatalf("cross-vault %s=%d %s", method, response.Code, response.Body.String())
			}
		}
	}
	// The production CSRF boundary also applies to subscription generation.
	request := httptest.NewRequest(http.MethodPost, "https://money.example.test/api/subscription-sync", nil)
	request.AddCookie(&http.Cookie{Name: middleware.SecureSessionCookieName, Value: logins[0].ID})
	request.Header.Set("Origin", "https://money.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("generation without CSRF=%d %s", response.Code, response.Body.String())
	}
	unauth := httptest.NewRecorder()
	handler.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "https://money.example.test/api/subscriptions", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated subscriptions=%d", unauth.Code)
	}
}
