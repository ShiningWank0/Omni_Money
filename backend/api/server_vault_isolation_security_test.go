package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
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

func isolationSecurityPNG(t *testing.T, marker byte) string {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	picture.SetNRGBA(0, 0, color.NRGBA{R: marker, G: 91, B: 123, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, picture); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(output.Bytes())
}

func isolationSecurityArchive(t *testing.T, service *core.Service) string {
	t.Helper()
	result, err := service.BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// Exercise the production router with equal numeric IDs in independently
// encrypted vaults. A user's IDs select only that user's objects, even for an
// admin; they must never select or mutate the other user's ledger.
func TestProductionServerSecurityFinancialIDsStayInsideSessionVault(t *testing.T) {
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
	var services [2]*core.Service
	var logins [2]*middleware.Session
	var transactionIDs [2][2]int64
	var tagIDs, imageIDs [2]int64
	markers := []string{"vault-a-private", "vault-b-private"}
	for i, user := range users {
		vaultID := "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M"
		if i == 1 {
			vaultID = "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4N"
		}
		root := acquireServerCSVVault(t, manager, user.ID, vaultID, byte(0x61+i))
		service, err := root.Service()
		if err != nil {
			root.Release()
			t.Fatal(err)
		}
		services[i] = service
		if err := service.SaveCreditCardSettings([]string{markers[i] + "-card"}); err != nil {
			root.Release()
			t.Fatal(err)
		}
		if err := service.SaveBankAccountSettings([]string{markers[i] + "-bank"}); err != nil {
			root.Release()
			t.Fatal(err)
		}
		for j, suffix := range []string{"card", "bank"} {
			transaction, err := service.AddTransaction(models.TransactionRequest{Account: markers[i] + "-" + suffix, Date: "2026-09-01", Item: markers[i] + "-" + suffix, Memo: markers[i], Type: "expense", Amount: 100})
			if err != nil {
				root.Release()
				t.Fatal(err)
			}
			transactionIDs[i][j] = transaction.ID
		}
		tag, err := service.CreateTag(markers[i]+"-tag", nil)
		if err != nil {
			root.Release()
			t.Fatal(err)
		}
		tagIDs[i] = tag.ID
		if err := service.AddTransactionTags(transactionIDs[i][0], []int64{tag.ID}); err != nil {
			root.Release()
			t.Fatal(err)
		}
		picture, err := service.AddTransactionImage(transactionIDs[i][0], models.TransactionImageRequest{Filename: markers[i] + ".png", MimeType: "image/png", Data: isolationSecurityPNG(t, byte(30+i))})
		if err != nil {
			root.Release()
			t.Fatal(err)
		}
		imageIDs[i] = picture.ID
		if err := service.AddTransactionLink(transactionIDs[i][0], transactionIDs[i][1]); err != nil {
			root.Release()
			t.Fatal(err)
		}
		logins[i], err = sessions.CreateVaultSession(user, root)
		if err != nil {
			root.Release()
			t.Fatal(err)
		}
	}
	if transactionIDs[0] != transactionIDs[1] || tagIDs[0] != tagIDs[1] || imageIDs[0] != imageIDs[1] {
		t.Fatalf("fixture IDs do not collide: transactions=%v tags=%v images=%v", transactionIDs, tagIDs, imageIDs)
	}
	handler, err := NewServerRouter(ServerDependencies{Accounts: &fakeServerAccounts{}, Sessions: sessions, Control: store})
	if err != nil {
		t.Fatal(err)
	}
	txID, bankID, tagID, imageID := transactionIDs[0][0], transactionIDs[0][1], tagIDs[0], imageIDs[0]
	readPaths := []string{
		"/api/accounts", "/api/items", "/api/transactions", "/api/balance_history", "/api/balance_history_filtered", "/api/tags",
		"/api/credit_card_settings", "/api/bank_account_settings",
		fmt.Sprintf("/api/transaction_images/%d", txID), fmt.Sprintf("/api/transaction_images/%d?paged=1&limit=1", txID),
		fmt.Sprintf("/api/transaction_tags/%d", txID), fmt.Sprintf("/api/transaction_links/%d", txID),
	}
	for i, session := range logins {
		for _, path := range readPaths {
			if path == "/api/balance_history_filtered" {
				path += "?fund_items=" + markers[i] + "-bank"
			}
			t.Run(fmt.Sprintf("read user %d %s", i, path), func(t *testing.T) {
				response := serveServerCSVRequest(t, handler, session, http.MethodGet, path, nil)
				body := response.Body.String()
				if response.Code != http.StatusOK || !strings.Contains(body, markers[i]) || strings.Contains(body, markers[1-i]) {
					t.Fatalf("status=%d body=%s; expected only %q", response.Code, body, markers[i])
				}
			})
		}
	}
	// Export all relational and image data to detect *any* change in the other
	// vault after each authorized mutation, including cascades and side effects.
	otherBefore := isolationSecurityArchive(t, services[1])
	mutate := func(name, method, path string, body any, status int, verify func(*testing.T, []byte)) {
		t.Helper()
		if !t.Run(name, func(t *testing.T) {
			before := isolationSecurityArchive(t, services[0])
			var payload []byte
			if body != nil {
				var err error
				payload, err = json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
			}
			response := serveServerCSVRequest(t, handler, logins[0], method, path, payload)
			if response.Code != status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
			}
			if before == isolationSecurityArchive(t, services[0]) {
				t.Fatal("authorized mutation did not change the session user's vault")
			}
			if got := isolationSecurityArchive(t, services[1]); got != otherBefore {
				t.Fatal("mutation changed the other user's encrypted vault")
			}
			if verify != nil {
				verify(t, response.Body.Bytes())
			}
		}) {
			t.FailNow()
		}
	}
	mutate("remove colliding image", http.MethodDelete, fmt.Sprintf("/api/transaction_images/%d/%d", txID, imageID), nil, http.StatusOK, func(t *testing.T, _ []byte) {
		pictures, err := services[0].GetTransactionImages(txID)
		if err != nil || len(pictures) != 0 {
			t.Fatalf("images=%v err=%v", pictures, err)
		}
	})
	mutate("upload image", http.MethodPost, fmt.Sprintf("/api/transaction_images/%d", txID), models.TransactionImageRequest{Filename: "new-a.png", MimeType: "image/png", Data: isolationSecurityPNG(t, 80)}, http.StatusCreated, func(t *testing.T, _ []byte) {
		pictures, err := services[0].GetTransactionImages(txID)
		if err != nil || len(pictures) != 1 || pictures[0].Filename != "new-a.png" {
			t.Fatalf("images=%v err=%v", pictures, err)
		}
	})
	mutate("remove colliding tag association", http.MethodDelete, fmt.Sprintf("/api/transaction_tags/%d/%d", txID, tagID), nil, http.StatusOK, func(t *testing.T, _ []byte) {
		tags, err := services[0].GetTransactionTags(txID)
		if err != nil || len(tags) != 0 {
			t.Fatalf("tags=%v err=%v", tags, err)
		}
	})
	mutate("add tag association", http.MethodPost, fmt.Sprintf("/api/transaction_tags/%d", txID), map[string]any{"tag_ids": []int64{tagID}}, http.StatusOK, func(t *testing.T, _ []byte) {
		tags, err := services[0].GetTransactionTags(txID)
		if err != nil || len(tags) != 1 || tags[0].ID != tagID {
			t.Fatalf("tags=%v err=%v", tags, err)
		}
	})
	mutate("remove colliding link", http.MethodDelete, fmt.Sprintf("/api/transaction_links/%d/%d", txID, bankID), nil, http.StatusOK, func(t *testing.T, _ []byte) {
		links, err := services[0].GetTransactionLinks(txID)
		if err != nil || len(links) != 0 {
			t.Fatalf("links=%v err=%v", links, err)
		}
	})
	mutate("add link", http.MethodPost, fmt.Sprintf("/api/transaction_links/%d", txID), map[string]any{"linked_id": bankID}, http.StatusOK, func(t *testing.T, _ []byte) {
		links, err := services[0].GetTransactionLinks(txID)
		if err != nil || len(links) != 1 {
			t.Fatalf("links=%v err=%v", links, err)
		}
	})
	mutate("rename colliding tag", http.MethodPut, fmt.Sprintf("/api/tags/%d", tagID), map[string]string{"name": "renamed-a"}, http.StatusOK, func(t *testing.T, _ []byte) {
		tags, err := services[0].GetTags()
		if err != nil || len(tags) != 1 || tags[0].Name != "renamed-a" {
			t.Fatalf("tags=%v err=%v", tags, err)
		}
	})
	var newTagID int64
	mutate("create tag", http.MethodPost, "/api/tags", map[string]string{"name": "new-a-tag"}, http.StatusCreated, func(t *testing.T, body []byte) {
		var tag models.Tag
		if err := json.Unmarshal(body, &tag); err != nil {
			t.Fatal(err)
		}
		if tag.ID == 0 || tag.Name != "new-a-tag" {
			t.Fatalf("tag=%+v", tag)
		}
		newTagID = tag.ID
	})
	mutate("delete created tag", http.MethodDelete, fmt.Sprintf("/api/tags/%d", newTagID), nil, http.StatusOK, nil)
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		item := "updated-a-" + method
		mutate("update colliding transaction "+method, method, fmt.Sprintf("/api/transactions/%d", txID), models.TransactionRequest{Account: markers[0] + "-card", Date: "2026-09-01", Item: item, Type: "expense", Amount: 200}, http.StatusOK, func(t *testing.T, _ []byte) {
			transactions, err := services[0].GetTransactions(markers[0]+"-card", "")
			if err != nil || len(transactions) != 1 || transactions[0].Item != item {
				t.Fatalf("transactions=%v err=%v", transactions, err)
			}
		})
	}
	var newTransactionID int64
	mutate("create transaction", http.MethodPost, "/api/transactions", models.TransactionRequest{Account: markers[0] + "-card", Date: "2026-09-02", Item: "new-a-transaction", Type: "expense", Amount: 300}, http.StatusCreated, func(t *testing.T, body []byte) {
		var result struct {
			Transaction models.TransactionResponse `json:"transaction"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if result.Transaction.ID == 0 || result.Transaction.Item != "new-a-transaction" {
			t.Fatalf("transaction=%+v", result.Transaction)
		}
		newTransactionID = result.Transaction.ID
	})
	mutate("delete created transaction", http.MethodDelete, fmt.Sprintf("/api/transactions/%d", newTransactionID), nil, http.StatusOK, nil)
	mutate("delete colliding tag", http.MethodDelete, fmt.Sprintf("/api/tags/%d", tagID), nil, http.StatusOK, nil)
	mutate("delete colliding transaction and cascade", http.MethodDelete, fmt.Sprintf("/api/transactions/%d", txID), nil, http.StatusOK, func(t *testing.T, _ []byte) {
		transactions, err := services[0].GetTransactions(markers[0]+"-card", "")
		if err != nil || len(transactions) != 0 {
			t.Fatalf("transactions=%v err=%v", transactions, err)
		}
		pictures, err := services[0].GetTransactionImages(txID)
		if err != nil || len(pictures) != 0 {
			t.Fatalf("images=%v err=%v", pictures, err)
		}
		links, err := services[0].GetTransactionLinks(bankID)
		if err != nil || len(links) != 0 {
			t.Fatalf("links=%v err=%v", links, err)
		}
	})
}
