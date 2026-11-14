package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/core"
	"omni_money/backend/database"
	"omni_money/backend/middleware"
	"omni_money/backend/models"
)

func TestSubscriptionRoutesValidateInputAndKeepReadsSideEffectFree(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "ledger.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	service, err := core.NewLegacyService()
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.AddTransaction(models.TransactionRequest{Account: "cash", Item: "Music", Date: "2026-01-01", Type: "expense", Amount: 1000})
	if err != nil {
		t.Fatal(err)
	}
	date := time.Now().UTC().AddDate(1, 0, 0)
	input := core.SubscriptionInput{SourceTransactionID: source.ID, Account: "cash", Item: "Music", Currency: "JPY", OriginalAmount: "1000", Interval: "monthly", AnchorDay: date.Day(), AnchorMonth: int(date.Month()), NextDate: date.Format(time.DateOnly), Timezone: "UTC", Enabled: true, Tags: []int64{}}
	payload, _ := json.Marshal(input)
	mux := http.NewServeMux()
	registerFinancialRoutes(mux)
	handler := middleware.LegacyCoreServiceMiddleware(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}
	for _, body := range []string{string(payload) + `{}`, `{"unknown":true}`, strings.Repeat(" ", 32769) + string(payload)} {
		if response := request(http.MethodPost, "/api/subscriptions", body); response.Code != http.StatusBadRequest {
			t.Fatalf("malformed input status=%d body=%s", response.Code, response.Body.String())
		}
	}
	created := request(http.MethodPost, "/api/subscriptions", string(payload))
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	var rule core.SubscriptionRule
	if err := json.Unmarshal(created.Body.Bytes(), &rule); err != nil {
		t.Fatal(err)
	}
	if rule.ID == "" || rule.Revision != 1 {
		t.Fatalf("created=%+v", rule)
	}
	for _, path := range []string{"/api/subscriptions", "/api/subscription-calendar?month=" + date.Format("2006-01"), "/api/subscription-candidates", "/api/subscriptions/" + rule.ID + "/transactions"} {
		if response := request(http.MethodGet, path, ""); response.Code != http.StatusOK {
			t.Fatalf("read %s=%d %s", path, response.Code, response.Body.String())
		}
	}
	transactions, _ := service.GetTransactions("", "")
	if len(transactions) != 1 || transactions[0].Subscription != nil {
		t.Fatal("viewing subscriptions generated or relabeled a transaction")
	}
	if response := request(http.MethodGet, "/api/subscription-sync", ""); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET sync=%d", response.Code)
	}
	if response := request(http.MethodGet, "/api/subscription-calendar?month=2026-13", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid month=%d", response.Code)
	}
	if response := request(http.MethodPut, "/api/subscriptions/"+rule.ID, string(payload)); response.Code != http.StatusConflict {
		t.Fatalf("stale write=%d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodGet, "/api/subscriptions/6425a5b7-bf0d-4a2f-bb61-5ef6dd4196a3/transactions", ""); response.Code != http.StatusNotFound {
		t.Fatalf("missing rule=%d", response.Code)
	}
}
