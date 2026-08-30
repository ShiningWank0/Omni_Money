// Package api はAPIの接続口定義と通信経路（ルーティング）を提供する
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"omni_money/backend/aicredentials"
	"omni_money/backend/audithmac"
	"omni_money/backend/authn"
	"omni_money/backend/core"
	"omni_money/backend/database"
	"omni_money/backend/middleware"
	"omni_money/backend/models"
)

// NewRouter は公開Web用ルーターを作成する。
//
// AI API は意図的にこのルーターへ登録しない。公開WebとAI APIの認証境界を
// ポート単位で分離し、AIトークンが通常のセッション認証を迂回する経路を防ぐ。
func NewRouter() http.Handler {
	handler, err := NewRouterWithError()
	if err == nil {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		jsonError(w, "サーバーのセキュリティ設定が無効です", http.StatusServiceUnavailable)
	})
}

// NewRouterWithError validates security-sensitive configuration before a
// public listener starts. NewRouter remains as a fail-closed test-compatible
// wrapper.
func NewRouterWithError() (http.Handler, error) {
	passwordHash := strings.TrimSpace(os.Getenv("AUTH_PASSWORD_HASH"))
	if err := middleware.ValidatePasswordHash(passwordHash); err != nil {
		return nil, err
	}
	sessionConfig, err := middleware.SessionConfigFromEnv()
	if err != nil {
		return nil, err
	}
	totpVerifier, err := totpVerifierFromEnv()
	if err != nil {
		return nil, err
	}
	proxyConfig := middleware.NewProxyConfigFromEnv()
	if err := proxyConfig.Validate(); err != nil {
		return nil, err
	}

	sessionManager := middleware.NewSessionManagerWithConfig(sessionConfig)
	authManager := middleware.NewAuthSessionManager(sessionManager, passwordHash, totpVerifier)

	mux := http.NewServeMux()

	// 静的ファイル配信（フロントエンドのビルド成果物）
	mux.HandleFunc("/login", handleLoginPage)
	mux.HandleFunc("/login/", handleLoginPage)
	mux.Handle("/", http.FileServer(http.Dir("frontend/dist")))

	// 認証API（Agent.md §6.4.1）
	mux.HandleFunc("/api/auth/login", handleAuthLogin(authManager))
	mux.HandleFunc("/api/auth/logout", handleAuthLogout(authManager))
	mux.HandleFunc("/api/auth/logout-all", handleAuthLogoutAll(authManager))
	mux.HandleFunc("/api/auth/reauth", handleAuthReauthenticate(authManager))
	mux.HandleFunc("/api/auth/keepalive", handleAuthKeepalive)
	mux.HandleFunc("/api/auth/status", handleAuthStatus(authManager))

	// 認証済み管理UIから固定loopbackのAI専用リスナーへ中継する。
	// Bearer tokenと接続先はブラウザへ公開しない。
	mux.HandleFunc("/api/ai-console/transactions", methodGuard(http.MethodPost, handleAIConsoleProxy("/api/v1/ai/transactions")))
	mux.HandleFunc("/api/ai-console/analysis", methodGuard(http.MethodPost, handleAIConsoleProxy("/api/v1/ai/analysis")))

	registerFinancialRoutes(mux)

	// Snapshot lifecycle remains on the legacy router until the dedicated
	// per-vault restore coordinator is introduced.
	mux.HandleFunc("/api/snapshots", handleSnapshots)
	mux.HandleFunc("/api/snapshots/restore", methodGuard(http.MethodPost, handleSnapshotRestore))

	// 公開WebポートではAI APIを提供しない。認証済みセッションからアクセスしても404。
	mux.HandleFunc("/api/v1/ai/", http.NotFound)

	// Docker等の死活監視用。家計簿データは返さない。
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
	})

	// サーバーモード用ミドルウェアの適用
	var handler http.Handler = middleware.LegacyCoreServiceMiddleware(mux)
	handler = middleware.RecentAuthMiddleware(sessionManager, handler)
	handler = middleware.CSRFMiddleware(sessionManager, handler)
	handler = middleware.SessionAuthMiddleware(sessionManager, handler)
	handler = middleware.MaxBodySizeMiddleware(handler)
	handler = middleware.RateLimitMiddleware(handler)
	handler = middleware.CORSMiddleware(handler)
	handler = middleware.NoStoreAPIMiddleware(handler)
	handler = middleware.SecurityHeadersMiddleware(handler)
	handler = middleware.ProxyMiddleware(proxyConfig, handler)
	handler = middleware.CacheControlMiddleware(handler)
	return handler, nil
}

// registerFinancialRoutes installs browser routes that must always operate on
// the request-scoped core Service. The legacy router explicitly installs one;
// multi-user server routers obtain one only from VaultSessionAuthMiddleware.
func registerFinancialRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/accounts", methodGuard(http.MethodGet, handleAccounts))
	mux.HandleFunc("/api/items", methodGuard(http.MethodGet, handleItems))
	mux.HandleFunc("/api/transactions", handleTransactions)
	mux.HandleFunc("/api/transactions/", handleTransactionByID)
	mux.HandleFunc("/api/balance_history", methodGuard(http.MethodGet, handleBalanceHistory))
	mux.HandleFunc("/api/balance_history_filtered", methodGuard(http.MethodGet, handleBalanceHistoryFiltered))
	mux.HandleFunc("/api/credit_card_settings", handleCreditCardSettings)
	mux.HandleFunc("/api/bank_account_settings", handleBankAccountSettings)
	mux.HandleFunc("/api/backup_csv", methodGuard(http.MethodGet, handleBackupCSV))
	mux.HandleFunc("/api/import_csv", methodGuard(http.MethodPost, handleImportCSV))
	mux.HandleFunc("/api/transaction_images/", handleTransactionImages)
	mux.HandleFunc("/api/image_storage", methodGuard(http.MethodGet, handleImageStorageUsage))
	mux.HandleFunc("/api/tags", handleTags)
	mux.HandleFunc("/api/tags/", handleTagByID)
	mux.HandleFunc("/api/tags/path", handleCreateTagByPath)
	mux.HandleFunc("/api/tags/summary", handleTagSummary)
	mux.HandleFunc("/api/transaction_tags/", handleTransactionTagsAPI)
	mux.HandleFunc("/api/transaction_links/", handleTransactionLinksAPI)
}

func totpVerifierFromEnv() (middleware.OneTimeCodeVerifier, error) {
	requireRaw := strings.TrimSpace(os.Getenv("AUTH_REQUIRE_TOTP"))
	requireTOTP := false
	switch requireRaw {
	case "":
	case "true":
		requireTOTP = true
	case "false":
	default:
		return nil, fmt.Errorf("AUTH_REQUIRE_TOTP must be true or false")
	}
	secretPath := strings.TrimSpace(os.Getenv("AUTH_TOTP_SECRET_FILE"))
	if secretPath == "" {
		if requireTOTP {
			return nil, fmt.Errorf("AUTH_TOTP_SECRET_FILE is required when AUTH_REQUIRE_TOTP=true")
		}
		return nil, nil
	}
	verifier, err := authn.LoadTOTPVerifier(secretPath)
	if err != nil {
		return nil, fmt.Errorf("load independent Omni TOTP secret: %w", err)
	}
	return verifier, nil
}

// NewAIRouter はAI専用リスナー用のルーターを作成する。
// 通常の家計簿API、静的ファイル、ユーザー認証APIは一切登録しない。
func NewAIRouter(credentialStore *aicredentials.Store, auditStore *audithmac.Store) http.Handler {
	aiMux := http.NewServeMux()
	aiMux.HandleFunc("/api/v1/ai/transactions", handleAITransactions)
	aiMux.HandleFunc("/api/v1/ai/analysis", handleAIAnalysis)

	var handler http.Handler = middleware.MaxBodySizeMiddleware(aiMux)
	handler = middleware.AIAPIMiddleware(credentialStore, auditStore, handler)
	handler = middleware.SecurityHeadersMiddleware(handler)
	handler = middleware.CacheControlMiddleware(handler)
	return handler
}

func handleLoginPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "frontend/dist/index.html")
}

// methodGuard は特定のHTTPメソッドのみ許可するラッパー
func methodGuard(method string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		handler(w, r)
	}
}

const (
	financialServiceUnavailableMessage = "ユーザーデータを安全に開けません"
	financialInvalidRequestMessage     = "リクエストデータが無効です"
	financialInternalErrorMessage      = "サーバー内部でエラーが発生しました"
)

func financialService(w http.ResponseWriter, r *http.Request) (*core.Service, bool) {
	service, ok := middleware.CoreServiceFromContext(r.Context())
	if !ok {
		jsonError(w, financialServiceUnavailableMessage, http.StatusServiceUnavailable)
		return nil, false
	}
	return service, true
}

func writeFinancialError(w http.ResponseWriter, err error, status int) {
	if errors.Is(err, core.ErrServiceUnavailable) {
		jsonError(w, financialServiceUnavailableMessage, http.StatusServiceUnavailable)
		return
	}
	if status >= http.StatusInternalServerError {
		jsonError(w, financialInternalErrorMessage, status)
		return
	}
	jsonError(w, financialInvalidRequestMessage, status)
}

func handleAccounts(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	accounts, err := service.GetAccounts()
	if err != nil {
		writeFinancialError(w, err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, accounts, http.StatusOK)
}

func handleItems(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	account := r.URL.Query().Get("account")
	items, err := service.GetItems(account)
	if err != nil {
		writeFinancialError(w, err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, items, http.StatusOK)
}

func handleTransactions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		search := r.URL.Query().Get("search")
		account := r.URL.Query().Get("account")
		transactions, err := service.GetTransactions(account, search)
		if err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, transactions, http.StatusOK)

	case http.MethodPost:
		var req models.TransactionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		resp, err := service.AddTransactionContext(r.Context(), req)
		if err != nil {
			writeFinancialError(w, err, http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"message":     "取引が正常に追加されました",
			"transaction": resp,
		}, http.StatusCreated)

	}
}

func handleTransactionByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	// "/api/transactions/123" からIDを抽出
	path := strings.TrimPrefix(r.URL.Path, "/api/transactions/")
	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		jsonError(w, "無効なIDです", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut, http.MethodPatch:
		var req models.TransactionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		resp, err := service.UpdateTransactionContext(r.Context(), id, req)
		if err != nil {
			writeFinancialError(w, err, http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"message":     "取引が更新されました",
			"transaction": resp,
		}, http.StatusOK)

	case http.MethodDelete:
		if err := service.DeleteTransaction(id); err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"message": "取引が削除されました"}, http.StatusOK)
	}
}

func handleBalanceHistory(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	resp, err := service.GetBalanceHistory()
	if err != nil {
		writeFinancialError(w, err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, resp, http.StatusOK)
}

func handleBalanceHistoryFiltered(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	fundItems := r.URL.Query()["fund_items"]
	resp, err := service.GetBalanceHistoryFiltered(fundItems)
	if err != nil {
		writeFinancialError(w, err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, resp, http.StatusOK)
}

func handleCreditCardSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := service.GetCreditCardSettings()
		if err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, items, http.StatusOK)

	case http.MethodPost:
		var body struct {
			CreditCardItems []string `json:"credit_card_items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		if err := service.SaveCreditCardSettings(body.CreditCardItems); err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"message":           "クレジットカード設定を保存しました",
			"credit_card_items": body.CreditCardItems,
		}, http.StatusOK)

	}
}

func handleBankAccountSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := service.GetBankAccountSettings()
		if err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, items, http.StatusOK)

	case http.MethodPost:
		var body struct {
			BankAccountItems []string `json:"bank_account_items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		if err := service.SaveBankAccountSettings(body.BankAccountItems); err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"message":            "銀行口座設定を保存しました",
			"bank_account_items": body.BankAccountItems,
		}, http.StatusOK)

	}
}

func handleBackupCSV(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	csvContent, err := service.BackupToCSV()
	if err != nil {
		writeFinancialError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=transactions_backup_%s.csv",
			time.Now().Format("20060102_150405")))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(csvContent))
}

func handleImportCSV(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return
	}
	if body.Mode == "" {
		body.Mode = "append"
	}

	count, err := service.ImportCSV(body.Content, body.Mode)
	if err != nil {
		if errors.Is(err, core.ErrAIIdempotencyConflict) {
			jsonError(w, "Idempotency-Keyは別のリクエストで使用済みです", http.StatusConflict)
			return
		}
		var quotaError *core.AIDailyQuotaExceededError
		if errors.As(err, &quotaError) {
			w.Header().Set("Retry-After", strconv.Itoa(quotaError.RetryAfterSeconds))
			jsonError(w, "AI経由の取引追加が日次上限に達しました", http.StatusTooManyRequests)
			return
		}
		writeFinancialError(w, err, http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"message":        fmt.Sprintf("CSVインポート完了: %d件", count),
		"imported_count": count,
		"mode":           body.Mode,
	}, http.StatusOK)
}

func handleAITransactions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	credential, ok := middleware.AICredentialFromContext(r.Context())
	if !ok {
		jsonError(w, "認証が必要です", http.StatusUnauthorized)
		return
	}

	var req models.TransactionRequest
	if err := decodeStrictAIJSON(r, &req); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return
	}
	req, err := normalizeAndValidateAITransaction(req, time.Now())
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !credential.AllowsAccount(req.Account) {
		jsonError(w, "指定した口座への追加権限がありません", http.StatusForbidden)
		return
	}
	middleware.RecordAIRequestAudit(r.Context(), req.Account, req.Date, req.Date, false, false, nil, nil)
	req, err = validateAITransactionReferences(req, credential)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errAITagNotAllowed) {
			status = http.StatusForbidden
		}
		jsonError(w, err.Error(), status)
		return
	}
	idempotencyKeyHash, err := aiIdempotencyKeyHash(r.Header)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := core.AddAITransaction(r.Context(), req, core.AITransactionIdentity{
		CredentialID:          credential.ID,
		IdempotencyKeySHA256:  idempotencyKeyHash,
		RequestSHA256:         canonicalAITransactionDigest(req),
		MaxTransactionsPerDay: credential.MaxTransactionsPerDay,
		Now:                   time.Now(),
	})
	if err != nil {
		if errors.Is(err, core.ErrAIIdempotencyConflict) {
			jsonError(w, "Idempotency-Keyは別のリクエストで使用済みです", http.StatusConflict)
			return
		}
		var quotaError *core.AIDailyQuotaExceededError
		if errors.As(err, &quotaError) {
			w.Header().Set("Retry-After", strconv.Itoa(quotaError.RetryAfterSeconds))
			jsonError(w, "AI経由の取引追加が日次上限に達しました", http.StatusTooManyRequests)
			return
		}
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	createdCount := 1
	middleware.RecordAIRequestAudit(r.Context(), req.Account, req.Date, req.Date, false, false, nil, &createdCount)
	if result.Replayed {
		middleware.RecordAIIdempotencyReplay(r.Context())
		w.Header().Set("Idempotency-Replayed", "true")
	}

	jsonResponse(w, map[string]interface{}{
		"message": "取引が正常に追加されました (AI API)",
		"transaction": struct {
			ID      int64  `json:"id"`
			Account string `json:"account"`
			Date    string `json:"date"`
		}{ID: result.Transaction.ID, Account: result.Transaction.Account, Date: result.Transaction.Date},
	}, http.StatusCreated)
}

func handleSnapshots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		snapshots, err := database.ListSnapshots("")
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if snapshots == nil {
			snapshots = []string{}
		}
		jsonResponse(w, snapshots, http.StatusOK)

	case http.MethodPost:
		path, err := database.CreateSnapshot("")
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Manual snapshots use the same bounded retention as automatic ones so
		// an authenticated browser cannot consume storage without limit.
		if err := database.CleanOldSnapshots("", 30); err != nil {
			jsonError(w, "スナップショットの世代管理に失敗しました", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"path": path, "message": "スナップショットを作成しました"}, http.StatusCreated)

	default:
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		jsonError(w, "スナップショット名が必要です", http.StatusBadRequest)
		return
	}
	if err := database.RestoreSnapshot("", body.Name); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"message": "スナップショットから復元しました"}, http.StatusOK)
}

// --- ヘルパー ---

func jsonResponse(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, message string, status int) {
	jsonResponse(w, map[string]string{"error": message}, status)
}

// --- 画像API ハンドラー (Agent.md §6.5) ---

func handleTransactionImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	// /api/transaction_images/{txId} or /api/transaction_images/{txId}/{imgId}
	path := strings.TrimPrefix(r.URL.Path, "/api/transaction_images/")
	parts := strings.SplitN(path, "/", 2)

	txID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		jsonError(w, "無効な取引IDです", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		images, err := service.GetTransactionImagesContext(r.Context(), txID)
		if err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, images, http.StatusOK)

	case http.MethodPost:
		var img models.TransactionImageRequest
		if err := json.NewDecoder(r.Body).Decode(&img); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		resp, err := service.AddTransactionImageContext(r.Context(), txID, img)
		if err != nil {
			writeFinancialError(w, err, http.StatusBadRequest)
			return
		}
		jsonResponse(w, resp, http.StatusCreated)

	case http.MethodDelete:
		if len(parts) < 2 {
			jsonError(w, "画像IDが必要です", http.StatusBadRequest)
			return
		}
		imgID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			jsonError(w, "無効な画像IDです", http.StatusBadRequest)
			return
		}
		if err := service.DeleteTransactionImageForTransaction(txID, imgID); err != nil {
			writeFinancialError(w, err, http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]string{"message": "画像を削除しました"}, http.StatusOK)

	}
}

func handleImageStorageUsage(w http.ResponseWriter, r *http.Request) {
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	usage, err := service.GetImageStorageUsage()
	if err != nil {
		writeFinancialError(w, err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, usage, http.StatusOK)
}

// --- タグAPI ハンドラー (Agent.md §6.6) ---

func handleTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		tags, err := service.GetTags()
		if err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, tags, http.StatusOK)

	case http.MethodPost:
		var body struct {
			Name     string `json:"name"`
			ParentID *int64 `json:"parent_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		tag, err := service.CreateTag(body.Name, body.ParentID)
		if err != nil {
			writeFinancialError(w, err, http.StatusBadRequest)
			return
		}
		jsonResponse(w, tag, http.StatusCreated)

	}
}

func handleCreateTagByPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return
	}
	tag, err := service.CreateTagByPath(body.Path)
	if err != nil {
		writeFinancialError(w, err, http.StatusBadRequest)
		return
	}
	jsonResponse(w, tag, http.StatusCreated)
}

func handleTagByID(w http.ResponseWriter, r *http.Request) {
	// /api/tags/summary は別ハンドラーで処理
	path := strings.TrimPrefix(r.URL.Path, "/api/tags/")
	if path == "summary" {
		handleTagSummary(w, r)
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}

	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		jsonError(w, "無効なタグIDです", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		if err := service.UpdateTag(id, body.Name); err != nil {
			writeFinancialError(w, err, http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]string{"message": "タグを更新しました"}, http.StatusOK)

	case http.MethodDelete:
		if err := service.DeleteTag(id); err != nil {
			writeFinancialError(w, err, http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]string{"message": "タグを削除しました"}, http.StatusOK)

	}
}

func handleTagSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}

	txType := r.URL.Query().Get("type")
	startDate := r.URL.Query().Get("start_date")
	endDate := r.URL.Query().Get("end_date")

	summaries, err := service.GetTagSummary(txType, startDate, endDate)
	if err != nil {
		writeFinancialError(w, err, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, summaries, http.StatusOK)
}

func handleTransactionTagsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	// /api/transaction_tags/{txId} or /api/transaction_tags/{txId}/{tagId}
	path := strings.TrimPrefix(r.URL.Path, "/api/transaction_tags/")
	parts := strings.SplitN(path, "/", 2)

	txID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		jsonError(w, "無効な取引IDです", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		tags, err := service.GetTransactionTags(txID)
		if err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, tags, http.StatusOK)

	case http.MethodPost:
		var body struct {
			TagIDs []int64 `json:"tag_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		if err := service.AddTransactionTags(txID, body.TagIDs); err != nil {
			writeFinancialError(w, err, http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]string{"message": "タグを追加しました"}, http.StatusOK)

	case http.MethodDelete:
		if len(parts) < 2 {
			jsonError(w, "タグIDが必要です", http.StatusBadRequest)
			return
		}
		tagID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			jsonError(w, "無効なタグIDです", http.StatusBadRequest)
			return
		}
		if err := service.RemoveTransactionTag(txID, tagID); err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"message": "タグを削除しました"}, http.StatusOK)

	}
}

// --- 取引紐付けAPI ハンドラー (Agent.md §6.2) ---

func handleTransactionLinksAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := financialService(w, r)
	if !ok {
		return
	}
	// /api/transaction_links/{txId} or /api/transaction_links/{txId}/{linkedId}
	path := strings.TrimPrefix(r.URL.Path, "/api/transaction_links/")
	parts := strings.SplitN(path, "/", 2)

	txID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		jsonError(w, "無効な取引IDです", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		links, err := service.GetTransactionLinks(txID)
		if err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, links, http.StatusOK)

	case http.MethodPost:
		var body struct {
			LinkedID int64 `json:"linked_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		if err := service.AddTransactionLink(txID, body.LinkedID); err != nil {
			writeFinancialError(w, err, http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]string{"message": "紐付けを追加しました"}, http.StatusOK)

	case http.MethodDelete:
		if len(parts) < 2 {
			jsonError(w, "紐付け先の取引IDが必要です", http.StatusBadRequest)
			return
		}
		linkedID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			jsonError(w, "無効な取引IDです", http.StatusBadRequest)
			return
		}
		if err := service.RemoveTransactionLink(txID, linkedID); err != nil {
			writeFinancialError(w, err, http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"message": "紐付けを解除しました"}, http.StatusOK)

	}
}

// --- AI分析API ハンドラー (Agent.md §6.3) ---

const aiAnalysisTimeout = 10 * time.Second

func handleAIAnalysis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	credential, ok := middleware.AICredentialFromContext(r.Context())
	if !ok {
		jsonError(w, "認証が必要です", http.StatusUnauthorized)
		return
	}

	var req models.AnalysisRequest
	if err := decodeStrictAIJSON(r, &req); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return
	}
	var status int
	req, status, err := validateAndScopeAIAnalysis(req, credential, time.Now())
	if err != nil {
		jsonError(w, err.Error(), status)
		return
	}
	middleware.RecordAIRequestAudit(r.Context(), req.Account, req.StartDate, req.EndDate, req.IncludeTransactions, req.IncludeMemo, nil, nil)

	analysisContext, cancel := context.WithTimeout(r.Context(), aiAnalysisTimeout)
	defer cancel()
	resp, err := core.AnalyzeTransactionsContext(analysisContext, req)
	if err != nil {
		writeAIAnalysisError(w, err)
		return
	}
	middleware.RecordAIRequestAudit(r.Context(), req.Account, req.StartDate, req.EndDate, req.IncludeTransactions, req.IncludeMemo, &resp.Count, &resp.ReturnedCount)

	jsonResponse(w, resp, http.StatusOK)
}

func writeAIAnalysisError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		jsonError(w, "AI分析がタイムアウトしました", http.StatusGatewayTimeout)
	case errors.Is(err, context.Canceled):
		jsonError(w, "AI分析リクエストがキャンセルされました", http.StatusRequestTimeout)
	default:
		jsonError(w, err.Error(), http.StatusInternalServerError)
	}
}

func decodeStrictAIJSON(r *http.Request, destination any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON本文には単一の値だけ指定してください")
	}
	return nil
}

func validateAndScopeAIAnalysis(req models.AnalysisRequest, credential *aicredentials.Credential, now time.Time) (models.AnalysisRequest, int, error) {
	const (
		dateLayout            = "2006-01-02"
		defaultAnalysisDays   = 30
		hardMaxAnalysisTagIDs = 20
		hardMaxResults        = 500
	)
	if credential == nil {
		return req, http.StatusUnauthorized, fmt.Errorf("認証が必要です")
	}
	req.MaxTagSummaries = credential.MaxResults

	req.StartDate = strings.TrimSpace(req.StartDate)
	req.EndDate = strings.TrimSpace(req.EndDate)
	req.Account = strings.TrimSpace(req.Account)
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	req.Cursor = strings.TrimSpace(req.Cursor)

	if req.Account == "" {
		if len(credential.Accounts) == 1 && credential.Accounts[0] != "*" {
			req.Account = credential.Accounts[0]
		} else {
			return req, http.StatusBadRequest, fmt.Errorf("分析対象の口座を明示してください")
		}
	}
	if len(req.Account) > maxAIAccountBytes || hasAIUnsafeRune(req.Account) {
		return req, http.StatusBadRequest, fmt.Errorf("口座名が無効です")
	}
	if !credential.AllowsAccount(req.Account) {
		return req, http.StatusForbidden, fmt.Errorf("指定した口座の分析権限がありません")
	}
	credentialStartDate, err := time.Parse(dateLayout, credential.AnalysisStartDate)
	if err != nil {
		return req, http.StatusInternalServerError, fmt.Errorf("資格情報の分析期間設定が無効です")
	}
	credentialEndDate, err := time.Parse(dateLayout, credential.AnalysisEndDate)
	if err != nil {
		return req, http.StatusInternalServerError, fmt.Errorf("資格情報の分析期間設定が無効です")
	}

	if (req.StartDate == "") != (req.EndDate == "") {
		return req, http.StatusBadRequest, fmt.Errorf("開始日と終了日は両方指定してください")
	}
	if req.StartDate == "" {
		end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		if end.After(credentialEndDate) {
			end = credentialEndDate
		}
		if end.Before(credentialStartDate) {
			return req, http.StatusForbidden, fmt.Errorf("現在日は資格情報に許可された分析期間外です")
		}
		days := defaultAnalysisDays
		if credential.MaxAnalysisDays < days {
			days = credential.MaxAnalysisDays
		}
		start := end.AddDate(0, 0, -(days - 1))
		if start.Before(credentialStartDate) {
			start = credentialStartDate
		}
		req.StartDate = start.Format(dateLayout)
		req.EndDate = end.Format(dateLayout)
	}
	startDate, err := time.Parse(dateLayout, req.StartDate)
	if err != nil {
		return req, http.StatusBadRequest, fmt.Errorf("開始日はYYYY-MM-DD形式で指定してください")
	}
	endDate, err := time.Parse(dateLayout, req.EndDate)
	if err != nil {
		return req, http.StatusBadRequest, fmt.Errorf("終了日はYYYY-MM-DD形式で指定してください")
	}
	if endDate.Before(startDate) {
		return req, http.StatusBadRequest, fmt.Errorf("終了日は開始日以降にしてください")
	}
	if startDate.Before(credentialStartDate) || endDate.After(credentialEndDate) {
		return req, http.StatusForbidden, fmt.Errorf(
			"分析期間はこの資格情報に許可された%sから%sまでにしてください",
			credential.AnalysisStartDate,
			credential.AnalysisEndDate,
		)
	}
	inclusiveDays := int(endDate.Sub(startDate)/(24*time.Hour)) + 1
	if inclusiveDays > credential.MaxAnalysisDays {
		return req, http.StatusForbidden, fmt.Errorf("分析期間はこの資格情報に許可された%d日以内にしてください", credential.MaxAnalysisDays)
	}

	if req.Type != "" && req.Type != "income" && req.Type != "expense" {
		return req, http.StatusBadRequest, fmt.Errorf("種別はincomeまたはexpenseで指定してください")
	}
	if len(req.TagIDs) > hardMaxAnalysisTagIDs {
		return req, http.StatusBadRequest, fmt.Errorf("タグIDは%d件までです", hardMaxAnalysisTagIDs)
	}
	seenTags := make(map[int64]struct{}, len(req.TagIDs))
	uniqueTags := make([]int64, 0, len(req.TagIDs))
	for _, tagID := range req.TagIDs {
		if tagID <= 0 {
			return req, http.StatusBadRequest, fmt.Errorf("タグIDは正の整数で指定してください")
		}
		if _, exists := seenTags[tagID]; exists {
			continue
		}
		if !credential.AllowsTag(tagID) {
			return req, http.StatusForbidden, errAITagNotAllowed
		}
		seenTags[tagID] = struct{}{}
		uniqueTags = append(uniqueTags, tagID)
	}
	req.TagIDs = uniqueTags

	if req.IncludeMemo && !req.IncludeTransactions {
		return req, http.StatusBadRequest, fmt.Errorf("メモを含めるには取引明細も要求してください")
	}
	if req.IncludeTransactions && !credential.HasScope(aicredentials.ScopeAnalysisTransactions) {
		return req, http.StatusForbidden, fmt.Errorf("取引明細を取得する権限がありません")
	}
	if req.IncludeMemo && !credential.HasScope(aicredentials.ScopeAnalysisMemo) {
		return req, http.StatusForbidden, fmt.Errorf("メモを取得する権限がありません")
	}
	if !req.IncludeTransactions {
		if req.Cursor != "" || req.Limit != 0 {
			return req, http.StatusBadRequest, fmt.Errorf("カーソルと件数は取引明細を要求する場合だけ指定できます")
		}
		return req, http.StatusOK, nil
	}

	maxResults := credential.MaxResults
	if maxResults > hardMaxResults {
		maxResults = hardMaxResults
	}
	if req.Limit == 0 {
		req.Limit = 100
		if req.Limit > maxResults {
			req.Limit = maxResults
		}
	}
	if req.Limit < 1 || req.Limit > maxResults {
		return req, http.StatusForbidden, fmt.Errorf("取引明細の件数はこの資格情報に許可された%d件以内にしてください", maxResults)
	}
	return req, http.StatusOK, nil
}
