package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

const testAIToken = "0123456789abcdef0123456789abcdef"
const testPasswordHash = "$2y$04$.OWNgfSMaTsdqHrwD6ydEeCs3dBUsAzNlpFzq3kJuK4BtUqU8E0WG"

func TestPublicRouterDoesNotAllowAITokenToBypassSession(t *testing.T) {
	t.Setenv("AI_API_TOKEN", testAIToken)
	handler := NewRouter()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", nil)
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("公開WebのAIパス status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestPublicRouterDoesNotRegisterAIEndpoints(t *testing.T) {
	t.Setenv("AUTH_PASSWORD_HASH", testPasswordHash)
	handler := NewRouter()

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"test-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, loginReq)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("test login status = %d, want %d; body=%s", loginRecorder.Code, http.StatusOK, loginRecorder.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	for _, cookie := range loginRecorder.Result().Cookies() {
		req.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("認証済み公開WebのAIパス status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestAIRouterDoesNotExposeRegularAPIs(t *testing.T) {
	handler := NewAIRouter(testAIToken)

	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("AI専用ポートの通常API status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestAIRouterRequiresBearerToken(t *testing.T) {
	handler := NewAIRouter(testAIToken)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("AI専用ポートの未認証アクセス status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestAIRouterRejectsWrongBearerToken(t *testing.T) {
	handler := NewAIRouter(testAIToken)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", nil)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdeg")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("AI専用ポートの不正トークン status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestAIRouterRejectsNonPOSTWithValidToken(t *testing.T) {
	handler := NewAIRouter(testAIToken)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ai/analysis", nil)
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("AI専用ポートのGET status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestHealthEndpointDoesNotExposeData(t *testing.T) {
	handler := NewRouter()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Fatalf("healthz body = %q", got)
	}
}

func TestAIRouterAuthorizedTransactionAndAnalysis(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omni_money_test.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(database.CloseDB)

	handler := NewAIRouter(testAIToken)
	postAITransaction(t, handler, `{
		"account":"cash",
		"date":"2026-07-02",
		"item":"PR54動作確認",
		"type":"expense",
		"amount":123,
		"memo":"AI専用APIの正常系"
	}`)
	postAITransaction(t, handler, `{
		"account":"bank",
		"date":"2026-07-02",
		"item":"対象外取引",
		"type":"expense",
		"amount":456
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", strings.NewReader(`{"account":"cash"}`))
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("AI analysis status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var response models.AnalysisResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("analysis response decode failed: %v", err)
	}
	if response.Count != 1 || response.TotalExpense != 123 || response.NetAmount != -123 {
		t.Fatalf("analysis summary = count:%d expense:%d net:%d, want 1,123,-123", response.Count, response.TotalExpense, response.NetAmount)
	}
	if len(response.Transactions) != 1 || response.Transactions[0].Account != "cash" || response.Transactions[0].Memo != "AI専用APIの正常系" {
		t.Fatalf("analysis transactions = %#v", response.Transactions)
	}

	waitForAPISnapshot(t)
}

func postAITransaction(t *testing.T, handler http.Handler, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/transactions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("AI transaction status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
}

func waitForAPISnapshot(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshots, err := database.ListSnapshots("")
		if err == nil && len(snapshots) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("AI transaction snapshot was not created")
}
