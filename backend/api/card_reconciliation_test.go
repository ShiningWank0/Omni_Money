package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/middleware"
	"omni_money/backend/models"
)

func TestCardReconciliationHTTPReportsMismatchAndEnforcesAssignment(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "ledger.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	if _, err := database.GetDB().Exec(`
		INSERT INTO settings(key, value) VALUES ('credit_card_items', '["card"]'), ('bank_account_items', '["bank"]');
		INSERT INTO transactions(id, account, date, item, type, amount, balance) VALUES
		(1, 'bank', '2026-01-01', 'bank payment', 'expense', 50, 0),
		(2, 'card', '2026-01-01', 'card purchase', 'expense', 100, 0),
		(3, 'bank', '2026-01-01', 'other payment', 'expense', 100, 0),
		(4, 'card', '2026-01-01', 'other purchase', 'expense', 50, 0);
		INSERT INTO transaction_links(parent_id, child_id) VALUES (1, 2);
	`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerFinancialRoutes(mux)
	handler := middleware.LegacyCoreServiceMiddleware(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}
	read := request(http.MethodGet, "/api/transaction_links/1/reconciliation", "")
	var result models.CardReconciliationResponse
	if err := json.Unmarshal(read.Body.Bytes(), &result); err != nil || read.Code != http.StatusOK || result.Matched || result.DifferenceExact != "50" {
		t.Fatalf("mismatch response: status=%d body=%s err=%v", read.Code, read.Body.String(), err)
	}
	rejected := request(http.MethodPost, "/api/transaction_links/3", `{"linked_id":2}`)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("second bank accepted: %d %s", rejected.Code, rejected.Body.String())
	}
	accepted := request(http.MethodPost, "/api/transaction_links/1", `{"linked_id":4}`)
	if accepted.Code != http.StatusOK {
		t.Fatalf("mismatch blocked a valid link: %d %s", accepted.Code, accepted.Body.String())
	}
	read = request(http.MethodGet, "/api/transaction_links/1/reconciliation", "")
	if err := json.Unmarshal(read.Body.Bytes(), &result); err != nil || result.CardTotalExact != "150" || result.DifferenceExact != "100" {
		t.Fatalf("updated reconciliation: %s, %v", read.Body.String(), err)
	}
	if response := request(http.MethodPost, "/api/transaction_links/1/reconciliation", `{"linked_id":3}`); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("reconciliation endpoint accepted a mutation: %d", response.Code)
	}
}
