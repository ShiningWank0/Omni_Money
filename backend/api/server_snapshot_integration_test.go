package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/database"
	"omni_money/backend/middleware"
	"omni_money/backend/models"
	"omni_money/backend/securedb"
	"omni_money/backend/vault"
)

func TestServerSnapshotsStayBoundToUserAndRestoreRevokesSessions(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "money.example.test")
	t.Setenv("FORCE_HTTPS", "false")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("HTTPS_REDIRECT_HOST", "")
	vaultRoot := filepath.Join(t.TempDir(), "vaults")
	manager, err := vault.NewManager(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	users := []control.UserSummary{
		{ID: "user_01HZX7CYK3XPSJ0HE8P2RQ7V4M", Email: "snapshot-a@example.test", Role: control.RoleAdmin, State: control.UserActive},
		{ID: "user_01HZX7CYK3XPSJ0HE8P2RQ7V4N", Email: "snapshot-b@example.test", Role: control.RoleUser, State: control.UserActive},
	}
	store := &serverCSVControlStore{users: map[string]control.UserSummary{users[0].ID: users[0], users[1].ID: users[1]}}
	vaultAID := "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M"
	vaultBID := "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4N"
	rootA := acquireServerCSVVault(t, manager, users[0].ID, vaultAID, 0x52)
	rootB := acquireServerCSVVault(t, manager, users[1].ID, vaultBID, 0x62)
	svcA, err := rootA.Service()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcA.AddTransaction(models.TransactionRequest{Account: "snap-a", Date: "2026-08-30", Item: "before", Type: "income", Amount: 1}); err != nil {
		t.Fatal(err)
	}
	svcB, err := rootB.Service()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcB.AddTransaction(models.TransactionRequest{Account: "snap-b", Date: "2026-08-30", Item: "other-user", Type: "income", Amount: 1}); err != nil {
		t.Fatal(err)
	}
	sessions := middleware.NewSessionManagerWithConfig(middleware.SessionConfig{MaxAge: time.Hour, IdleTimeout: 30 * time.Minute, RecentAuthAge: 10 * time.Minute, MaxConcurrent: 3})
	t.Cleanup(sessions.Close)
	sessionA, err := sessions.CreateVaultSession(users[0], rootA)
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := sessions.CreateVaultSession(users[1], rootB)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewServerRouter(ServerDependencies{Accounts: &fakeServerAccounts{}, Sessions: sessions, Control: store, Snapshots: sessions})
	if err != nil {
		t.Fatal(err)
	}
	statusResponse := serveServerCSVRequest(t, handler, sessionA, http.MethodGet, "/api/auth/status", nil)
	var statusBody struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	if statusResponse.Code != http.StatusOK || !statusBody.Features["snapshots"] {
		t.Fatalf("server snapshot feature status = %d %#v", statusResponse.Code, statusBody.Features)
	}
	created := serveServerCSVRequest(t, handler, sessionA, http.MethodPost, "/api/snapshots", nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("snapshot create status = %d, body=%s", created.Code, created.Body.String())
	}
	var createdBody map[string]string
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	if createdBody["path"] == "" || filepath.Base(createdBody["path"]) != createdBody["path"] || filepath.IsAbs(createdBody["path"]) {
		t.Fatalf("snapshot response leaked a path: %#v", createdBody)
	}
	snapshotOnDisk := filepath.Join(vaultRoot, vaultAID, "snapshots", createdBody["name"])
	if err := securedb.RequireEncryptedHeader(snapshotOnDisk); err != nil {
		t.Fatalf("server snapshot is not encrypted: %v", err)
	}
	if info, err := os.Stat(snapshotOnDisk); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("server snapshot permissions = %v, err=%v; want 0600", info.Mode().Perm(), err)
	}
	verifiedSnapshot, err := database.OpenExistingEncryptedInstance(snapshotOnDisk, testServerSnapshotKey(0x52))
	if err != nil {
		t.Fatalf("server snapshot failed same-opener integrity/open verification: %v", err)
	}
	if err := verifiedSnapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := svcA.AddTransaction(models.TransactionRequest{Account: "snap-a", Date: "2026-08-30", Item: "after", Type: "income", Amount: 2}); err != nil {
		t.Fatal(err)
	}
	invalidRestore := serveServerCSVRequest(t, handler, sessionA, http.MethodPost, "/api/snapshots/restore", []byte(`{"name":"../other-user.db"}`))
	if invalidRestore.Code != http.StatusBadRequest {
		t.Fatalf("unsafe restore name status = %d, body=%s", invalidRestore.Code, invalidRestore.Body.String())
	}
	if response := serveServerCSVRequest(t, handler, sessionA, http.MethodGet, "/api/snapshots", nil); response.Code != http.StatusOK {
		t.Fatalf("unsafe restore name drained the session: %d", response.Code)
	}
	noCSRF := httptest.NewRequest(http.MethodPost, "https://money.example.test/api/snapshots/restore", bytes.NewReader([]byte(`{"name":"missing.db"}`)))
	noCSRF.AddCookie(&http.Cookie{Name: middleware.SecureSessionCookieName, Value: sessionA.ID})
	noCSRF.Header.Set("Origin", "https://money.example.test")
	noCSRF.Header.Set("Sec-Fetch-Site", "same-origin")
	noCSRF.Header.Set("Content-Type", "application/json")
	noCSRFResponse := httptest.NewRecorder()
	handler.ServeHTTP(noCSRFResponse, noCSRF)
	if noCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("restore without CSRF status = %d, want %d", noCSRFResponse.Code, http.StatusForbidden)
	}
	if response := serveServerCSVRequest(t, handler, sessionA, http.MethodGet, "/api/snapshots", nil); response.Code != http.StatusOK {
		t.Fatalf("CSRF failure drained the session: %d", response.Code)
	}
	listA := serveServerCSVRequest(t, handler, sessionA, http.MethodGet, "/api/snapshots", nil)
	if listA.Code != http.StatusOK || !bytes.Contains(listA.Body.Bytes(), []byte(createdBody["path"])) {
		t.Fatalf("snapshot list response = %d %s", listA.Code, listA.Body.String())
	}
	restoreBody, _ := json.Marshal(map[string]string{"name": createdBody["path"]})
	restored := serveServerCSVRequest(t, handler, sessionA, http.MethodPost, "/api/snapshots/restore", restoreBody)
	if restored.Code != http.StatusOK {
		t.Fatalf("snapshot restore status = %d, body=%s", restored.Code, restored.Body.String())
	}
	var restoredBody map[string]interface{}
	if err := json.Unmarshal(restored.Body.Bytes(), &restoredBody); err != nil {
		t.Fatal(err)
	}
	if restoredBody["login_required"] != true {
		t.Fatalf("restore response login_required = %#v", restoredBody["login_required"])
	}
	if !strings.Contains(restored.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("restore response did not clear the session cookie: %q", restored.Header().Get("Set-Cookie"))
	}
	if response := serveServerCSVRequest(t, handler, sessionA, http.MethodGet, "/api/snapshots", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("restored user's stale session status = %d, want unauthorized", response.Code)
	}
	if response := serveServerCSVRequest(t, handler, sessionB, http.MethodGet, "/api/backup_csv", nil); response.Code != http.StatusOK {
		t.Fatalf("other user's session status = %d, want usable", response.Code)
	}
	listB := serveServerCSVRequest(t, handler, sessionB, http.MethodGet, "/api/snapshots", nil)
	if listB.Code != http.StatusOK || bytes.Contains(listB.Body.Bytes(), []byte(createdBody["name"])) {
		t.Fatalf("other user's snapshot list exposed user A's valid snapshot: %d %s", listB.Code, listB.Body.String())
	}
	foreignRestoreBody, _ := json.Marshal(map[string]string{"name": createdBody["name"]})
	foreignRestore := serveServerCSVRequest(t, handler, sessionB, http.MethodPost, "/api/snapshots/restore", foreignRestoreBody)
	if foreignRestore.Code != http.StatusInternalServerError {
		t.Fatalf("other user's restore of user A snapshot status = %d, body=%s", foreignRestore.Code, foreignRestore.Body.String())
	}
	var foreignBody map[string]interface{}
	if err := json.Unmarshal(foreignRestore.Body.Bytes(), &foreignBody); err != nil {
		t.Fatal(err)
	}
	if foreignBody["login_required"] != true {
		t.Fatalf("failed restore response login_required = %#v", foreignBody["login_required"])
	}
}

func testServerSnapshotKey(fill byte) securedb.RawKey {
	var key securedb.RawKey
	for index := range key {
		key[index] = fill
	}
	return key
}
