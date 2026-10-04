package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/core"
	"omni_money/backend/middleware"
	"omni_money/backend/models"
	"omni_money/backend/vault"
)

type cancelSaveBody struct {
	*bytes.Reader
	cancel context.CancelFunc
}

func (reader *cancelSaveBody) Read(data []byte) (int, error) {
	n, err := reader.Reader.Read(data)
	if reader.Len() == 0 {
		reader.cancel()
	}
	return n, err
}
func (*cancelSaveBody) Close() error { return nil }

func TestServerSaveReceiptSurvivesDisconnectLogoutAndFreshLogin(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "money.example.test")
	t.Setenv("FORCE_HTTPS", "false")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("HTTPS_REDIRECT_HOST", "")
	manager, err := vault.NewManager(filepath.Join(t.TempDir(), "vaults"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	sessions := middleware.NewSessionManagerWithConfig(middleware.SessionConfig{MaxAge: time.Hour, IdleTimeout: 30 * time.Minute, RecentAuthAge: 10 * time.Minute, MaxConcurrent: 3})
	defer sessions.Close()
	owner := control.UserSummary{ID: "user_01HZX7CYK3XPSJ0HE8P2RQ7V4M", Email: "owner@example.test", DisplayName: "Owner", Role: control.RoleUser, State: control.UserActive}
	other := owner
	other.ID = "user_01HZX7CYK3XPSJ0HE8P2RQ7V4N"
	other.Email = "other@example.test"
	store := &serverCSVControlStore{users: map[string]control.UserSummary{owner.ID: owner, other.ID: other}}
	login := func(user control.UserSummary, vaultID string, key byte) *middleware.Session {
		root := acquireServerCSVVault(t, manager, user.ID, vaultID, key)
		session, err := sessions.CreateVaultSession(user, root)
		if err != nil {
			root.Release()
			t.Fatal(err)
		}
		return session
	}
	session := login(owner, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M", 0x31)
	otherSession := login(other, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4N", 0x42)
	handler, err := NewServerRouter(ServerDependencies{Accounts: &fakeServerAccounts{}, Sessions: sessions, Control: store})
	if err != nil {
		t.Fatal(err)
	}
	id := "7616712d-b63e-4dfa-9fa2-707b6b3c0d9a"
	payload := transactionSaveRequest{UserID: owner.ID, RequestID: id, Transaction: models.TransactionRequest{Account: "cash", Date: "2026-10-04", Item: "durable", Type: "income", Amount: 100}}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "https://money.example.test/api/transaction-saves", nil).WithContext(ctx)
	r.Body = &cancelSaveBody{Reader: bytes.NewReader(data), cancel: cancel}
	r.AddCookie(&http.Cookie{Name: middleware.SecureSessionCookieName, Value: session.ID})
	r.Header.Set("Origin", "https://money.example.test")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(middleware.CSRFHeaderName, session.CSRFToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if ctx.Err() == nil || w.Code != http.StatusAccepted {
		t.Fatalf("fully received request lost on disconnect: %d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		path := "/api/transaction-saves/" + id
		var body []byte
		if method == http.MethodPost {
			path = "/api/transaction-saves"
			body = data
		}
		response := serveServerCSVRequest(t, handler, otherSession, method, path, body)
		want := http.StatusNotFound
		if method == http.MethodPost {
			want = http.StatusForbidden
		}
		if response.Code != want {
			t.Fatalf("foreign user save access: %d %s", response.Code, response.Body.String())
		}
	}
	logout := serveServerCSVRequest(t, handler, session, http.MethodPost, "/api/auth/logout", nil)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout=%d %s", logout.Code, logout.Body.String())
	}
	if response := serveServerCSVRequest(t, handler, session, http.MethodGet, "/api/transactions", nil); response.Code != http.StatusUnauthorized {
		t.Fatal("old login retained authority")
	}
	fresh := login(owner, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M", 0x31)
	deadline := time.Now().Add(3 * time.Second)
	for {
		response := serveServerCSVRequest(t, handler, fresh, http.MethodGet, "/api/transaction-saves/"+id, nil)
		var status core.TransactionSave
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil {
			t.Fatalf("new login cannot access own receipt: %d %s", response.Code, response.Body.String())
		}
		if status.State == "completed" {
			break
		}
		if status.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("accepted save not completed: %+v", status)
		}
		time.Sleep(time.Millisecond)
	}
	if replay := serveServerCSVRequest(t, handler, fresh, http.MethodPost, "/api/transaction-saves", data); replay.Code != http.StatusAccepted {
		t.Fatalf("receipt replay: %d %s", replay.Code, replay.Body.String())
	}
	response := serveServerCSVRequest(t, handler, fresh, http.MethodGet, "/api/transactions", nil)
	var transactions []models.TransactionResponse
	if json.Unmarshal(response.Body.Bytes(), &transactions) != nil || len(transactions) != 1 || transactions[0].Item != "durable" {
		t.Fatalf("missing or duplicated ledger row: %s", response.Body.String())
	}
}

var _ io.ReadCloser = (*cancelSaveBody)(nil)
