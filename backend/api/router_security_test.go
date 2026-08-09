package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/aicredentials"
	"omni_money/backend/database"
	"omni_money/backend/models"
	"omni_money/backend/validation"
)

const testAIToken = "0123456789abcdef0123456789abcdef0123456789A"
const testPasswordHash = "$2y$04$.OWNgfSMaTsdqHrwD6ydEeCs3dBUsAzNlpFzq3kJuK4BtUqU8E0WG"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestPublicRouterDoesNotAllowAITokenToBypassSession(t *testing.T) {
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
	handler := newTestAIRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("AI専用ポートの通常API status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestAIRouterRequiresBearerToken(t *testing.T) {
	handler := newTestAIRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("AI専用ポートの未認証アクセス status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestAIRouterRejectsWrongBearerToken(t *testing.T) {
	handler := newTestAIRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", nil)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdeg")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("AI専用ポートの不正トークン status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestAIRouterRejectsNonPOSTWithValidToken(t *testing.T) {
	handler := newTestAIRouter(t)

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

func TestPublicRouterPreventsCachingPrivateAndHTMLResponses(t *testing.T) {
	handler := NewRouter()

	apiRecorder := httptest.NewRecorder()
	handler.ServeHTTP(apiRecorder, httptest.NewRequest(http.MethodGet, "/api/accounts", nil))
	if apiRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API status = %d, want %d", apiRecorder.Code, http.StatusUnauthorized)
	}
	if got := apiRecorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("API Cache-Control = %q, want no-store", got)
	}
	if got := apiRecorder.Header().Get("Surrogate-Control"); got != "no-store" {
		t.Fatalf("API Surrogate-Control = %q, want no-store", got)
	}

	htmlRecorder := httptest.NewRecorder()
	handler.ServeHTTP(htmlRecorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	if got := htmlRecorder.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("HTML Cache-Control = %q, want no-cache", got)
	}
}

func TestLogoutClearsBrowserDataAndInvalidatesSession(t *testing.T) {
	t.Setenv("AUTH_PASSWORD_HASH", testPasswordHash)
	handler := NewRouter()

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"test-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, loginReq)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d; body=%s", loginRecorder.Code, http.StatusOK, loginRecorder.Body.String())
	}
	cookies := loginRecorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not issue a session cookie")
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	for _, cookie := range cookies {
		logoutReq.AddCookie(cookie)
	}
	logoutRecorder := httptest.NewRecorder()
	handler.ServeHTTP(logoutRecorder, logoutReq)
	if logoutRecorder.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want %d", logoutRecorder.Code, http.StatusOK)
	}
	if got := logoutRecorder.Header().Get("Clear-Site-Data"); got != `"cache", "cookies", "storage"` {
		t.Fatalf("Clear-Site-Data = %q", got)
	}
	if got := logoutRecorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("logout Cache-Control = %q, want no-store", got)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	statusReq.AddCookie(cookies[0])
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, statusReq)
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), `"authenticated":false`) {
		t.Fatalf("status after logout = %d body=%s", statusRecorder.Code, statusRecorder.Body.String())
	}
}

func TestAIRouterAuthorizedTransactionAndAnalysis(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omni_money_test.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(database.CloseDB)

	handler := newTestAIRouter(t)
	today := time.Now().Format("2006-01-02")
	postAITransaction(t, handler, fmt.Sprintf(`{
		"account":"cash",
		"date":%q,
		"item":"PR54動作確認",
		"type":"expense",
		"amount":123,
		"memo":"AI専用APIの正常系"
	}`, today))
	if _, err := database.GetDB().Exec(`
		INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES (?, ?, ?, ?, ?, ?, '')
	`, "bank", today, "対象外取引", "expense", 456, -456); err != nil {
		t.Fatalf("insert out-of-scope transaction: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", strings.NewReader(fmt.Sprintf(`{
		"account":"cash",
		"start_date":%q,
		"end_date":%q,
		"include_transactions":true,
		"include_memo":true,
		"limit":100
	}`, today, today)))
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("AI analysis status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
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
		{name: "amount上限超過", mutate: func(req *models.TransactionRequest) { req.Amount = validation.MaxTransactionAmount + 1 }},
		{name: "account length", mutate: func(req *models.TransactionRequest) { req.Account = strings.Repeat("a", maxAIAccountBytes+1) }},
		{name: "item length", mutate: func(req *models.TransactionRequest) { req.Item = strings.Repeat("i", maxAIItemBytes+1) }},
		{name: "memo length", mutate: func(req *models.TransactionRequest) { req.Memo = strings.Repeat("m", maxAIMemoBytes+1) }},
		{name: "item control", mutate: func(req *models.TransactionRequest) { req.Item = "food\nforged" }},
		{name: "memo format rune", mutate: func(req *models.TransactionRequest) { req.Memo = "hidden\u200btext" }},
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

	tooManyTags := valid
	tooManyTags.Tags = make([]int64, maxAITagIDs+1)
	if _, err := validateAITransactionReferences(tooManyTags); err == nil {
		t.Fatal("expected tag count validation error")
	}

	invalidImage := valid
	invalidImage.Images = []models.TransactionImageRequest{{Filename: "receipt.png", Data: "not-base64", MimeType: "image/png"}}
	body, err := json.Marshal(invalidImage)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/transactions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	newTestAIRouter(t).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid AI image status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestTransactionImageAPIRejectsInvalidContentAndReportsUsage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omni_money_test.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(database.CloseDB)
	result, err := database.GetDB().Exec(
		"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, 0, '')",
		"cash", "2026-01-01", "receipt", "expense", 100,
	)
	if err != nil {
		t.Fatal(err)
	}
	transactionID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("AUTH_PASSWORD_HASH", testPasswordHash)
	handler := NewRouter()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"test-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, loginReq)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", loginRecorder.Code, loginRecorder.Body.String())
	}

	imageReq := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/api/transaction_images/%d", transactionID),
		strings.NewReader(`{"filename":"fake.png","mime_type":"image/png","data":"bm90IGFuIGltYWdl"}`),
	)
	imageReq.Header.Set("Content-Type", "application/json")
	for _, cookie := range loginRecorder.Result().Cookies() {
		imageReq.AddCookie(cookie)
	}
	imageRecorder := httptest.NewRecorder()
	handler.ServeHTTP(imageRecorder, imageReq)
	if imageRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid image status = %d, want %d; body=%s", imageRecorder.Code, http.StatusBadRequest, imageRecorder.Body.String())
	}
	var imageCount int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transaction_images").Scan(&imageCount); err != nil {
		t.Fatal(err)
	}
	if imageCount != 0 {
		t.Fatalf("stored invalid images = %d, want 0", imageCount)
	}

	usageReq := httptest.NewRequest(http.MethodGet, "/api/image_storage", nil)
	for _, cookie := range loginRecorder.Result().Cookies() {
		usageReq.AddCookie(cookie)
	}
	usageRecorder := httptest.NewRecorder()
	handler.ServeHTTP(usageRecorder, usageReq)
	if usageRecorder.Code != http.StatusOK {
		t.Fatalf("image storage status = %d body=%s", usageRecorder.Code, usageRecorder.Body.String())
	}
	if !strings.Contains(usageRecorder.Body.String(), `"max_image_bytes":5242880`) {
		t.Fatalf("image storage response does not expose enforced quotas: %s", usageRecorder.Body.String())
	}
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
	var gotRelayMarker string
	var gotHost string
	var gotPath string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotAuthorization = r.Header.Get("Authorization")
		gotRelayMarker = r.Header.Get("X-Omni-AI-Console-Relay")
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
	tokenFile := filepath.Join(t.TempDir(), "ai-console-token")
	if err := os.WriteFile(tokenFile, []byte(testAIToken+"\n"), 0o600); err != nil {
		t.Fatalf("write console token: %v", err)
	}
	t.Setenv("AI_CONSOLE_TOKEN_FILE", tokenFile)
	t.Setenv("AI_PORT", "43123")
	var consoleAudit bytes.Buffer
	originalLogOutput := log.Writer()
	log.SetOutput(&consoleAudit)
	t.Cleanup(func() { log.SetOutput(originalLogOutput) })

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
	if gotRelayMarker != "1" {
		t.Fatalf("relay marker = %q, want 1", gotRelayMarker)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if !strings.Contains(consoleAudit.String(), "AI_CONSOLE_AUDIT") || !strings.Contains(consoleAudit.String(), `"client_ip":"192.0.2.1"`) {
		t.Fatalf("console audit missing: %q", consoleAudit.String())
	}
	if strings.Contains(consoleAudit.String(), testAIToken) || strings.Contains(consoleAudit.String(), `"amount":100`) {
		t.Fatalf("console audit leaked request data: %q", consoleAudit.String())
	}
}

func TestAIConsoleTokenPermissionsDistinguishHostAndDockerSecrets(t *testing.T) {
	if safeAIConsoleTokenPermissions("/tmp/ai-token", 0o644) {
		t.Fatal("host token with group/other read bits was accepted")
	}
	if !safeAIConsoleTokenPermissions("/tmp/ai-token", 0o600) {
		t.Fatal("host token mode 0600 was rejected")
	}
	if !safeAIConsoleTokenPermissions("/run/secrets/omni_ai_console_token", 0o444) {
		t.Fatal("Docker secret mode 0444 was rejected")
	}
	if safeAIConsoleTokenPermissions("/run/secrets/omni_ai_console_token", 0o464) {
		t.Fatal("writable Docker secret was accepted")
	}
}

func newTestAIRouter(t *testing.T) http.Handler {
	t.Helper()
	return newTestAIRouterWithCredential(t, aicredentials.Credential{
		ID: "test-credential",
		Scopes: []string{
			aicredentials.ScopeTransactionsCreate,
			aicredentials.ScopeAnalysisSummary,
			aicredentials.ScopeAnalysisTransactions,
			aicredentials.ScopeAnalysisMemo,
			aicredentials.ScopeConsoleRelay,
		},
		Accounts:        []string{"cash", "bank"},
		MaxAnalysisDays: aicredentials.MaxAnalysisDays,
		MaxResults:      aicredentials.MaxResults,
	})
}

func newTestAIRouterWithCredential(t *testing.T, credential aicredentials.Credential) http.Handler {
	t.Helper()
	credentialFile := filepath.Join(t.TempDir(), "ai-credentials.json")
	now := time.Now().UTC()
	credential.TokenSHA256 = aicredentials.HashToken(testAIToken)
	if credential.NotBefore.IsZero() {
		credential.NotBefore = now.Add(-time.Hour)
	}
	if credential.ExpiresAt.IsZero() {
		credential.ExpiresAt = now.Add(24 * time.Hour)
	}
	if credential.HasScope(aicredentials.ScopeAnalysisSummary) && credential.AnalysisStartDate == "" {
		credential.AnalysisStartDate = "2020-01-01"
		credential.AnalysisEndDate = "2030-12-31"
	}
	document := &aicredentials.File{
		Version:     aicredentials.CurrentVersion,
		Credentials: []aicredentials.Credential{credential},
	}
	if err := aicredentials.WriteFileAtomic(credentialFile, document); err != nil {
		t.Fatalf("write AI credentials: %v", err)
	}
	store, err := aicredentials.NewStore(credentialFile)
	if err != nil {
		t.Fatalf("load AI credentials: %v", err)
	}
	return NewAIRouter(store)
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
	for _, forbiddenField := range []string{`"balance"`, `"memo"`, `"tags"`, `"images"`, `"amount"`, `"item"`} {
		if strings.Contains(recorder.Body.String(), forbiddenField) {
			t.Fatalf("AI write-only response exposed %s: %s", forbiddenField, recorder.Body.String())
		}
	}
}

func waitForAPISnapshot(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	stableSince := time.Time{}
	lastCount := -1
	for time.Now().Before(deadline) {
		snapshots, err := database.ListSnapshots("")
		if err == nil && len(snapshots) > 0 {
			if len(snapshots) != lastCount {
				lastCount = len(snapshots)
				stableSince = time.Now()
			} else if time.Since(stableSince) >= 250*time.Millisecond {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("AI transaction snapshot was not created")
}
