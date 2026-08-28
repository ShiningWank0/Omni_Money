package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/database"
	"omni_money/backend/middleware"
	"omni_money/backend/models"
	"omni_money/backend/securedb"
	"omni_money/backend/vault"
)

type serverCSVControlStore struct {
	users map[string]control.UserSummary
}

func (*serverCSVControlStore) IsBootstrapped(context.Context) (bool, error) {
	return true, nil
}

func (store *serverCSVControlStore) GetUser(_ context.Context, userID string) (control.UserSummary, error) {
	user, ok := store.users[userID]
	if !ok {
		return control.UserSummary{}, control.ErrNotFound
	}
	return user, nil
}

func (store *serverCSVControlStore) ListUsers(context.Context) ([]control.UserSummary, error) {
	users := make([]control.UserSummary, 0, len(store.users))
	for _, user := range store.users {
		users = append(users, user)
	}
	return users, nil
}

func TestProductionServerCSVStaysInsideAuthenticatedVault(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "money.example.test")
	t.Setenv("FORCE_HTTPS", "false")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("HTTPS_REDIRECT_HOST", "")

	// Poison the legacy package-global database. Any accidental fallback would
	// make the sentinel observable in a CSV response and fail the assertions.
	if err := database.InitDB(filepath.Join(t.TempDir(), "poisoned-global.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	insertServerCSVSentinel(t, database.GetDB(), "global-poison")

	vaultManager, err := vault.NewManager(filepath.Join(t.TempDir(), "vaults"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := vaultManager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})

	users := []control.UserSummary{
		{
			ID: "user_01HZX7CYK3XPSJ0HE8P2RQ7V4M", Email: "admin-a@example.test",
			DisplayName: "Admin A", Role: control.RoleAdmin, State: control.UserActive,
		},
		{
			ID: "user_01HZX7CYK3XPSJ0HE8P2RQ7V4N", Email: "user-b@example.test",
			DisplayName: "User B", Role: control.RoleUser, State: control.UserActive,
		},
	}
	store := &serverCSVControlStore{users: map[string]control.UserSummary{
		users[0].ID: users[0],
		users[1].ID: users[1],
	}}

	rootA := acquireServerCSVVault(t, vaultManager, users[0].ID, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M", 0x31)
	t.Cleanup(rootA.Release)
	serviceA, err := rootA.Service()
	if err != nil {
		rootA.Release()
		t.Fatal(err)
	}
	insertServerCSVTransaction(t, serviceA, "only-admin-a")

	rootB := acquireServerCSVVault(t, vaultManager, users[1].ID, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4N", 0x42)
	t.Cleanup(rootB.Release)
	serviceB, err := rootB.Service()
	if err != nil {
		rootA.Release()
		rootB.Release()
		t.Fatal(err)
	}
	insertServerCSVTransaction(t, serviceB, "only-user-b")

	sessions := middleware.NewSessionManagerWithConfig(middleware.SessionConfig{
		MaxAge: time.Hour, IdleTimeout: 30 * time.Minute, RecentAuthAge: 10 * time.Minute, MaxConcurrent: 3,
	})
	t.Cleanup(sessions.Close)
	sessionA, err := sessions.CreateVaultSession(users[0], rootA)
	if err != nil {
		rootA.Release()
		rootB.Release()
		t.Fatal(err)
	}
	sessionB, err := sessions.CreateVaultSession(users[1], rootB)
	if err != nil {
		rootB.Release()
		t.Fatal(err)
	}

	handler, err := NewServerRouter(ServerDependencies{
		Accounts: &fakeServerAccounts{}, Sessions: sessions, Control: store,
	})
	if err != nil {
		t.Fatal(err)
	}

	assertServerCSVAccounts(t, serveServerCSVRequest(t, handler, sessionA, http.MethodGet, "/api/backup_csv", nil), "only-admin-a")
	assertServerCSVAccounts(t, serveServerCSVRequest(t, handler, sessionB, http.MethodGet, "/api/backup_csv", nil), "only-user-b")

	importDocument := "id,account,date,item,type,amount,balance,memo,omni_money_csv_version\n" +
		"1,imported-only-admin-a,2026-08-28,boundary,income,700,700,isolated,2\n"
	importBody, err := json.Marshal(map[string]string{"content": importDocument, "mode": "replace"})
	if err != nil {
		t.Fatal(err)
	}
	importResponse := serveServerCSVRequest(t, handler, sessionA, http.MethodPost, "/api/import_csv", importBody)
	if importResponse.Code != http.StatusOK {
		t.Fatalf("admin import status = %d, want %d", importResponse.Code, http.StatusOK)
	}
	var importResult struct {
		ImportedCount int `json:"imported_count"`
	}
	if err := json.Unmarshal(importResponse.Body.Bytes(), &importResult); err != nil {
		t.Fatal(err)
	}
	if importResult.ImportedCount != 1 {
		t.Fatalf("imported count = %d, want 1", importResult.ImportedCount)
	}

	assertServerCSVAccounts(t, serveServerCSVRequest(t, handler, sessionA, http.MethodGet, "/api/backup_csv", nil), "imported-only-admin-a")
	assertServerCSVAccounts(t, serveServerCSVRequest(t, handler, sessionB, http.MethodGet, "/api/backup_csv", nil), "only-user-b")

	t.Run("server feature boundaries", func(t *testing.T) {
		statusResponse := serveServerCSVRequest(t, handler, sessionA, http.MethodGet, "/api/auth/status", nil)
		if statusResponse.Code != http.StatusOK {
			t.Fatalf("auth status = %d, want %d", statusResponse.Code, http.StatusOK)
		}
		var status struct {
			Authenticated bool `json:"authenticated"`
			Features      struct {
				Admin     bool `json:"admin"`
				AI        bool `json:"ai"`
				Snapshots bool `json:"snapshots"`
			} `json:"features"`
		}
		if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if !status.Authenticated || !status.Features.Admin || status.Features.AI || status.Features.Snapshots {
			t.Fatalf("unexpected authenticated server features: %+v", status)
		}

		for name, test := range map[string]struct {
			path string
			want int
		}{
			"snapshots unavailable": {path: "/api/snapshots", want: http.StatusServiceUnavailable},
			"AI absent":             {path: "/api/ai-console/transactions", want: http.StatusNotFound},
		} {
			t.Run(name, func(t *testing.T) {
				response := serveServerCSVRequest(t, handler, sessionA, http.MethodGet, test.path, nil)
				if response.Code != test.want {
					t.Fatalf("status = %d, want %d", response.Code, test.want)
				}
			})
		}
	})

	t.Run("stale recent authentication blocks export", func(t *testing.T) {
		staleRoot := acquireServerCSVVault(t, vaultManager, users[0].ID, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M", 0x31)
		t.Cleanup(staleRoot.Release)
		staleSessions := middleware.NewSessionManagerWithConfig(middleware.SessionConfig{
			MaxAge: time.Hour, IdleTimeout: 30 * time.Minute, RecentAuthAge: time.Nanosecond, MaxConcurrent: 1,
		})
		t.Cleanup(staleSessions.Close)
		staleSession, err := staleSessions.CreateVaultSession(users[0], staleRoot)
		if err != nil {
			staleRoot.Release()
			t.Fatal(err)
		}
		staleHandler, err := NewServerRouter(ServerDependencies{
			Accounts: &fakeServerAccounts{}, Sessions: staleSessions, Control: store,
		})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
		response := serveServerCSVRequest(t, staleHandler, staleSession, http.MethodGet, "/api/backup_csv", nil)
		if response.Code != http.StatusPreconditionRequired {
			t.Fatalf("stale export status = %d, want %d", response.Code, http.StatusPreconditionRequired)
		}
		var body struct {
			RecentAuthRequired bool `json:"recent_auth_required"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !body.RecentAuthRequired {
			t.Fatal("stale export did not return the recent-auth precondition marker")
		}
	})
}

func acquireServerCSVVault(t *testing.T, manager *vault.Manager, userID, vaultID string, fill byte) *vault.Lease {
	t.Helper()
	var key securedb.RawKey
	for index := range key {
		key[index] = fill
	}
	lease, err := manager.Acquire(userID, vaultID, key)
	if err != nil {
		if errors.Is(err, securedb.ErrCipherUnavailable) || errors.Is(err, securedb.ErrCipherVersion) || errors.Is(err, securedb.ErrCipherProvider) {
			if os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" {
				t.Skip("SQLCipher-linked integration build is required")
			}
		}
		t.Fatal(err)
	}
	return lease
}

func insertServerCSVTransaction(t *testing.T, service interface {
	AddTransaction(models.TransactionRequest) (*models.TransactionResponse, error)
}, account string) {
	t.Helper()
	if _, err := service.AddTransaction(models.TransactionRequest{
		Account: account, Date: "2026-08-28", Item: "boundary", Type: "income", Amount: 100,
	}); err != nil {
		t.Fatal(err)
	}
}

func insertServerCSVSentinel(t *testing.T, db *sql.DB, account string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES (?, '2026-08-28', 'boundary', 'income', 100, 100, '')
	`, account); err != nil {
		t.Fatal(err)
	}
}

func serveServerCSVRequest(t *testing.T, handler http.Handler, session *middleware.Session, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "https://money.example.test"+path, bytes.NewReader(body))
	request.AddCookie(&http.Cookie{Name: middleware.SecureSessionCookieName, Value: session.ID})
	request.Header.Set("Origin", "https://money.example.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(middleware.CSRFHeaderName, session.CSRFToken)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func assertServerCSVAccounts(t *testing.T, response *httptest.ResponseRecorder, want string) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("CSV export status = %d, want %d", response.Code, http.StatusOK)
	}
	reader := csv.NewReader(bytes.NewReader(response.Body.Bytes()))
	header, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	accountColumn := -1
	for index, name := range header {
		if name == "account" {
			accountColumn = index
			break
		}
	}
	if accountColumn < 0 {
		t.Fatal("CSV response has no account column")
	}
	var accounts []string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if accountColumn >= len(record) {
			t.Fatal("CSV response row has no account value")
		}
		accounts = append(accounts, record[accountColumn])
	}
	if len(accounts) != 1 || accounts[0] != want {
		t.Fatalf("CSV account boundary mismatch: got %d rows", len(accounts))
	}
}
