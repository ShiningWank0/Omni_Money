package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"omni_money/backend/core"
	"omni_money/backend/database"
	"omni_money/backend/middleware"
)

func TestFinancialRoutesNeverFallBackToInitializedGlobalDatabase(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "poisoned-global.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	if _, err := database.GetDB().Exec(`
		INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES ('global-must-not-be-read', '2026-08-28', 'sentinel', 'expense', 1, -1, '')
	`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerFinancialRoutes(mux)
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/accounts", ""},
		{http.MethodGet, "/api/items", ""},
		{http.MethodGet, "/api/transactions", ""},
		{http.MethodPost, "/api/transactions", `{}`},
		{http.MethodPut, "/api/transactions/1", `{}`},
		{http.MethodPatch, "/api/transactions/1", `{}`},
		{http.MethodDelete, "/api/transactions/1", ""},
		{http.MethodGet, "/api/balance_history", ""},
		{http.MethodGet, "/api/balance_history_filtered", ""},
		{http.MethodGet, "/api/credit_card_settings", ""},
		{http.MethodPost, "/api/credit_card_settings", `{}`},
		{http.MethodGet, "/api/bank_account_settings", ""},
		{http.MethodPost, "/api/bank_account_settings", `{}`},
		{http.MethodGet, "/api/backup_csv", ""},
		{http.MethodPost, "/api/import_csv", `{}`},
		{http.MethodGet, "/api/transaction_images/1", ""},
		{http.MethodPost, "/api/transaction_images/1", `{}`},
		{http.MethodDelete, "/api/transaction_images/1/1", ""},
		{http.MethodGet, "/api/image_storage", ""},
		{http.MethodGet, "/api/tags", ""},
		{http.MethodPost, "/api/tags", `{}`},
		{http.MethodPost, "/api/tags/path", `{}`},
		{http.MethodPut, "/api/tags/1", `{}`},
		{http.MethodDelete, "/api/tags/1", ""},
		{http.MethodGet, "/api/tags/summary", ""},
		{http.MethodGet, "/api/transaction_tags/1", ""},
		{http.MethodPost, "/api/transaction_tags/1", `{}`},
		{http.MethodDelete, "/api/transaction_tags/1/1", ""},
		{http.MethodGet, "/api/transaction_links/1", ""},
		{http.MethodPost, "/api/transaction_links/1", `{}`},
		{http.MethodDelete, "/api/transaction_links/1/2", ""},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
			}
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error != financialServiceUnavailableMessage {
				t.Fatalf("error = %q, want fixed unavailable response", response.Error)
			}
		})
	}
}

func TestLegacyCoreServiceMiddlewareExplicitlyPreservesLegacyRouterBehavior(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "legacy-global.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	if _, err := database.GetDB().Exec(`
		INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES ('legacy-account', '2026-08-28', 'sentinel', 'expense', 1, -1, '')
	`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerFinancialRoutes(mux)
	handler := middleware.LegacyCoreServiceMiddleware(mux)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/accounts", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("legacy route status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var accounts []string
	if err := json.Unmarshal(recorder.Body.Bytes(), &accounts); err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0] != "legacy-account" {
		t.Fatalf("legacy route accounts = %v", accounts)
	}
}

func TestFinancialErrorResponsesNeverExposeInternalErrors(t *testing.T) {
	secret := "SQL failure at /private/vault/user-1/ledger.db"
	tests := []struct {
		name        string
		err         error
		status      int
		wantStatus  int
		wantMessage string
	}{
		{"validation", errors.New(secret), http.StatusBadRequest, http.StatusBadRequest, financialInvalidRequestMessage},
		{"internal", errors.New(secret), http.StatusInternalServerError, http.StatusInternalServerError, financialInternalErrorMessage},
		{"unavailable", errors.Join(core.ErrServiceUnavailable, errors.New(secret)), http.StatusBadRequest, http.StatusServiceUnavailable, financialServiceUnavailableMessage},
		{"legacy replace remediation", core.ErrCSVReplaceRequiresV3, http.StatusBadRequest, http.StatusBadRequest, "旧形式のCSVはappendで取り込めます。完全置換にはCSV v3を使用してください"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeFinancialError(recorder, test.err, test.status)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error != test.wantMessage || strings.Contains(recorder.Body.String(), secret) {
				t.Fatalf("error response leaked internal error: %s", recorder.Body.String())
			}
		})
	}
}
