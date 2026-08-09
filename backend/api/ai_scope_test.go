package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/aicredentials"
	"omni_money/backend/database"
	"omni_money/backend/models"
)

func TestValidateAndScopeAIAnalysisAppliesCredentialDefaults(t *testing.T) {
	now := time.Date(2026, time.August, 9, 15, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	credential := &aicredentials.Credential{
		Accounts:          []string{"cash"},
		MaxAnalysisDays:   7,
		MaxResults:        5,
		AnalysisStartDate: "2026-01-01",
		AnalysisEndDate:   "2026-12-31",
	}
	request, status, err := validateAndScopeAIAnalysis(models.AnalysisRequest{}, credential, now)
	if err != nil || status != http.StatusOK {
		t.Fatalf("validate status=%d err=%v", status, err)
	}
	if request.Account != "cash" || request.StartDate != "2026-08-03" || request.EndDate != "2026-08-09" {
		t.Fatalf("bounded defaults = %#v", request)
	}
	if request.IncludeTransactions || request.Limit != 0 || request.Cursor != "" {
		t.Fatalf("summary defaults unexpectedly expose details: %#v", request)
	}
}

func TestAIAnalysisEnforcesAccountAndDetailScopes(t *testing.T) {
	handler := newTestAIRouterWithCredential(t, aicredentials.Credential{
		ID:              "summary-only",
		Scopes:          []string{aicredentials.ScopeAnalysisSummary},
		Accounts:        []string{"cash"},
		MaxAnalysisDays: 30,
		MaxResults:      10,
	})
	today := time.Now().Format("2006-01-02")

	tests := []struct {
		name string
		body string
	}{
		{name: "forbidden account", body: fmt.Sprintf(`{"account":"bank","start_date":%q,"end_date":%q}`, today, today)},
		{name: "details without scope", body: fmt.Sprintf(`{"account":"cash","start_date":%q,"end_date":%q,"include_transactions":true}`, today, today)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", strings.NewReader(test.body))
			req.Header.Set("Authorization", "Bearer "+testAIToken)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestAIAnalysisEnforcesPeriodResultAndMemoScopes(t *testing.T) {
	credential := &aicredentials.Credential{
		Scopes:            []string{aicredentials.ScopeAnalysisSummary, aicredentials.ScopeAnalysisTransactions},
		Accounts:          []string{"cash"},
		MaxAnalysisDays:   7,
		MaxResults:        2,
		AnalysisStartDate: "2026-08-01",
		AnalysisEndDate:   "2026-08-31",
		AllowConsoleRelay: false,
	}
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		modify func(*models.AnalysisRequest)
		status int
	}{
		{name: "period", modify: func(r *models.AnalysisRequest) { r.StartDate = "2026-08-02" }, status: http.StatusForbidden},
		{name: "fixed credential window", modify: func(r *models.AnalysisRequest) { r.StartDate = "2026-07-31"; r.EndDate = "2026-08-06" }, status: http.StatusForbidden},
		{name: "results", modify: func(r *models.AnalysisRequest) { r.Limit = 3 }, status: http.StatusForbidden},
		{name: "memo", modify: func(r *models.AnalysisRequest) { r.IncludeMemo = true }, status: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := models.AnalysisRequest{
				Account:             "cash",
				StartDate:           "2026-08-03",
				EndDate:             "2026-08-09",
				IncludeTransactions: true,
				Limit:               2,
			}
			test.modify(&request)
			_, status, err := validateAndScopeAIAnalysis(request, credential, now)
			if err == nil || status != test.status {
				t.Fatalf("status=%d err=%v", status, err)
			}
		})
	}
}

func TestAIAnalysisStrictJSONAndSummaryDoNotExposeDetails(t *testing.T) {
	handler := newTestAIRouterWithCredential(t, aicredentials.Credential{
		ID:              "summary",
		Scopes:          []string{aicredentials.ScopeAnalysisSummary},
		Accounts:        []string{"cash"},
		MaxAnalysisDays: 30,
		MaxResults:      10,
	})

	unknown := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", strings.NewReader(`{"account":"cash","unexpected":true}`))
	unknown.Header.Set("Authorization", "Bearer "+testAIToken)
	unknownRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unknownRecorder, unknown)
	if unknownRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", unknownRecorder.Code, unknownRecorder.Body.String())
	}

	// The empty body is safely narrowed to the credential's sole account and
	// bounded date window before the core query. No detail scope is present.
	databaseForAIScopeTest(t)
	summary := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", strings.NewReader(`{}`))
	summary.Header.Set("Authorization", "Bearer "+testAIToken)
	summaryRecorder := httptest.NewRecorder()
	handler.ServeHTTP(summaryRecorder, summary)
	if summaryRecorder.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", summaryRecorder.Code, summaryRecorder.Body.String())
	}
	var response models.AnalysisResponse
	if err := json.Unmarshal(summaryRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Transactions) != 0 || response.ReturnedCount != 0 || response.NextCursor != "" {
		t.Fatalf("summary leaked details: %#v", response)
	}
}

func TestAITransactionAndConsoleRelayUseIndependentScopes(t *testing.T) {
	handler := newTestAIRouterWithCredential(t, aicredentials.Credential{
		ID: "scoped-writer",
		Scopes: []string{
			aicredentials.ScopeTransactionsCreate,
			aicredentials.ScopeAnalysisSummary,
		},
		Accounts:        []string{"cash"},
		MaxAnalysisDays: 30,
		MaxResults:      10,
	})
	today := time.Now().Format("2006-01-02")

	transaction := httptest.NewRequest(http.MethodPost, "/api/v1/ai/transactions", strings.NewReader(fmt.Sprintf(`{
		"account":"bank","date":%q,"item":"forbidden","type":"expense","amount":1
	}`, today)))
	transaction.Header.Set("Authorization", "Bearer "+testAIToken)
	transactionRecorder := httptest.NewRecorder()
	handler.ServeHTTP(transactionRecorder, transaction)
	if transactionRecorder.Code != http.StatusForbidden {
		t.Fatalf("forbidden account status=%d body=%s", transactionRecorder.Code, transactionRecorder.Body.String())
	}

	relay := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", strings.NewReader(`{}`))
	relay.Header.Set("Authorization", "Bearer "+testAIToken)
	relay.Header.Set("X-Omni-AI-Console-Relay", "1")
	relayRecorder := httptest.NewRecorder()
	handler.ServeHTTP(relayRecorder, relay)
	if relayRecorder.Code != http.StatusForbidden {
		t.Fatalf("console relay status=%d body=%s", relayRecorder.Code, relayRecorder.Body.String())
	}
}

func TestAIRouterFailsClosedForUnmappedAIPath(t *testing.T) {
	handler := newTestAIRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/future-endpoint", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unmapped AI path status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func databaseForAIScopeTest(t *testing.T) {
	t.Helper()
	if err := database.InitDB(filepath.Join(t.TempDir(), "ai-scope.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(database.CloseDB)
}
