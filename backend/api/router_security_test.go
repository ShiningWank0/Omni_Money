package api

import (
	"encoding/json"
	"fmt"
	"io"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

func TestAIConsoleRequiresWebSession(t *testing.T) {
	handler := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/ai-console/analysis", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("AI console without session status = %d, want %d", recorder.Code, http.StatusUnauthorized)
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
	today := time.Now().Format("2006-01-02")
	postAITransaction(t, handler, fmt.Sprintf(`{
		"account":"cash",
		"date":%q,
		"item":"PR54動作確認",
		"type":"expense",
		"amount":123,
		"memo":"AI専用APIの正常系"
	}`, today))
	postAITransaction(t, handler, fmt.Sprintf(`{
		"account":"bank",
		"date":%q,
		"item":"対象外取引",
		"type":"expense",
		"amount":456
	}`, today))

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

func TestAITransactionDateWindow(t *testing.T) {
	location := time.FixedZone("JST", 9*60*60)
	now := time.Date(2026, time.July, 2, 15, 30, 0, 0, location)
	base := models.TransactionRequest{
		Account: " cash ",
		Item:    " food ",
		Type:    " EXPENSE ",
		Amount:  100,
	}

	tests := []struct {
		name    string
		date    string
		wantErr bool
	}{
		{name: "one year ago boundary", date: "2025-07-02"},
		{name: "today", date: "2026-07-02"},
		{name: "two days later boundary", date: "2026-07-04"},
		{name: "before lower boundary", date: "2025-07-01", wantErr: true},
		{name: "after upper boundary", date: "2026-07-05", wantErr: true},
		{name: "invalid format", date: "2026/07/02", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := base
			req.Date = tt.date
			got, err := normalizeAndValidateAITransaction(req, now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && (got.Account != "cash" || got.Item != "food" || got.Type != "expense") {
				t.Fatalf("normalized request = %#v", got)
			}
		})
	}
}

func TestAITransactionRequiresFields(t *testing.T) {
	now := time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC)
	valid := models.TransactionRequest{Account: "cash", Date: "2026-07-02", Item: "food", Type: "expense", Amount: 1}

	tests := []struct {
		name   string
		mutate func(*models.TransactionRequest)
	}{
		{name: "account", mutate: func(req *models.TransactionRequest) { req.Account = " " }},
		{name: "date", mutate: func(req *models.TransactionRequest) { req.Date = "" }},
		{name: "item", mutate: func(req *models.TransactionRequest) { req.Item = " " }},
		{name: "type", mutate: func(req *models.TransactionRequest) { req.Type = "other" }},
		{name: "amount", mutate: func(req *models.TransactionRequest) { req.Amount = 0 }},
		{name: "amount上限超過", mutate: func(req *models.TransactionRequest) { req.Amount = maxAITransactionAmount + 1 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			tt.mutate(&req)
			if _, err := normalizeAndValidateAITransaction(req, now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestAIConsoleRelayHostHonorsLoopbackAIHostIP(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "未設定", value: "", want: "127.0.0.1"},
		{name: "IPv6ループバック", value: "::1", want: "::1"},
		{name: "非ループバックはフォールバック", value: "0.0.0.0", want: "127.0.0.1"},
		{name: "外部アドレスはフォールバック", value: "192.168.1.10", want: "127.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AI_HOST_IP", tt.value)
			if got := aiConsoleRelayHost(); got != tt.want {
				t.Fatalf("aiConsoleRelayHost() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAITransactionRejectsInvalidTagsAndImages(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omni_money_test.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(database.CloseDB)

	valid := models.TransactionRequest{
		Account: "cash",
		Date:    time.Now().Format("2006-01-02"),
		Item:    "food",
		Type:    "expense",
		Amount:  100,
	}

	unknownTag := valid
	unknownTag.Tags = []int64{999999}
	if _, err := validateAITransactionReferences(unknownTag); err == nil {
		t.Fatal("expected unknown tag validation error")
	}

	invalidImage := valid
	invalidImage.Images = []models.TransactionImageRequest{{Filename: "receipt.png", Data: "not-base64", MimeType: "image/png"}}
	if _, err := validateAITransactionReferences(invalidImage); err == nil {
		t.Fatal("expected invalid image validation error")
	}

	unsafeFilename := valid
	unsafeFilename.Images = []models.TransactionImageRequest{{Filename: "../receipt.png", Data: "aGVsbG8=", MimeType: "image/png"}}
	if _, err := validateAITransactionReferences(unsafeFilename); err == nil {
		t.Fatal("expected unsafe filename validation error")
	}
}

func TestAIAnalysisRejectsInvalidFilters(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid type", body: `{"type":"other"}`},
		{name: "invalid start date", body: `{"start_date":"2026/01/01"}`},
		{name: "invalid end date", body: `{"end_date":"2026-02-30"}`},
		{name: "reversed date range", body: `{"start_date":"2026-02-01","end_date":"2026-01-31"}`},
		{name: "zero tag id", body: `{"tag_ids":[0]}`},
		{name: "negative tag id", body: `{"tag_ids":[-1]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewAIRouter(testAIToken)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer "+testAIToken)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"error"`) {
				t.Fatalf("response does not contain Japanese validation error: %s", recorder.Body.String())
			}
		})
	}
}

func TestDeleteTransactionImageRequiresMatchingTransaction(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omni_money_test.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(database.CloseDB)

	tx1 := insertAPITestTransaction(t, "cash")
	tx2 := insertAPITestTransaction(t, "bank")
	result, err := database.GetDB().Exec(
		"INSERT INTO transaction_images (transaction_id, filename, data, mime_type) VALUES (?, ?, ?, ?)",
		tx1, "receipt.png", []byte("image"), "image/png",
	)
	if err != nil {
		t.Fatalf("image insert failed: %v", err)
	}
	imageID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("image LastInsertId failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/transaction_images/%d/%d", tx2, imageID), nil)
	recorder := httptest.NewRecorder()
	handleTransactionImages(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("mismatched delete status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transaction_images WHERE id = ?", imageID).Scan(&count); err != nil {
		t.Fatalf("image count query failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("image count after mismatched delete = %d, want 1", count)
	}

	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/transaction_images/%d/%d", tx1, imageID), nil)
	recorder = httptest.NewRecorder()
	handleTransactionImages(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("matching delete status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
}

func insertAPITestTransaction(t *testing.T, account string) int64 {
	t.Helper()
	result, err := database.GetDB().Exec(
		"INSERT INTO transactions (account, date, item, type, amount, balance) VALUES (?, '2026-01-01', 'test', 'expense', 1, -1)",
		account,
	)
	if err != nil {
		t.Fatalf("transaction insert failed: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("transaction LastInsertId failed: %v", err)
	}
	return id
}

func TestPublicTransactionDoesNotUseAIDateWindow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omni_money_test.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(database.CloseDB)
	t.Setenv("AUTH_PASSWORD_HASH", testPasswordHash)
	handler := NewRouter()

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"test-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, loginReq)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login status = %d; body=%s", loginRecorder.Code, loginRecorder.Body.String())
	}

	transactionReq := httptest.NewRequest(http.MethodPost, "/api/transactions", strings.NewReader(`{
		"account":"cash",
		"date":"1000-01-01",
		"item":"human historical entry",
		"type":"expense",
		"amount":1
	}`))
	transactionReq.Header.Set("Content-Type", "application/json")
	for _, cookie := range loginRecorder.Result().Cookies() {
		transactionReq.AddCookie(cookie)
	}
	transactionRecorder := httptest.NewRecorder()
	handler.ServeHTTP(transactionRecorder, transactionReq)

	if transactionRecorder.Code != http.StatusCreated {
		t.Fatalf("human transaction status = %d, want %d; body=%s", transactionRecorder.Code, http.StatusCreated, transactionRecorder.Body.String())
	}
	waitForAPISnapshot(t)
}

func TestAIConsoleProxyKeepsTokenServerSide(t *testing.T) {
	var gotAuthorization string
	var gotHost string
	var gotPath string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotAuthorization = r.Header.Get("Authorization")
		gotHost = r.URL.Host
		gotPath = r.URL.Path
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    r,
		}, nil
	})}
	originalClient := aiConsoleHTTPClient
	aiConsoleHTTPClient = client
	t.Cleanup(func() { aiConsoleHTTPClient = originalClient })
	t.Setenv("AI_API_TOKEN", testAIToken)
	t.Setenv("AI_PORT", "43123")

	req := httptest.NewRequest(http.MethodPost, "/api/ai-console/transactions", strings.NewReader(`{"amount":100}`))
	recorder := httptest.NewRecorder()
	handleAIConsoleProxy("/api/v1/ai/transactions").ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated || recorder.Body.String() != `{"ok":true}` {
		t.Fatalf("proxy response status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if gotAuthorization != "Bearer "+testAIToken {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if gotHost != "127.0.0.1:43123" {
		t.Fatalf("host = %q, want fixed loopback target", gotHost)
	}
	if gotPath != "/api/v1/ai/transactions" {
		t.Fatalf("path = %q", gotPath)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
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
