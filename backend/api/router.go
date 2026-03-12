// Package api はAPIの接続口定義と通信経路（ルーティング）を提供する
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"omni_money/backend/core"
	"omni_money/backend/database"
	"omni_money/backend/middleware"
	"omni_money/backend/models"
)

// NewRouter はサーバーモード用のHTTPルーターを作成する
func NewRouter() http.Handler {
	mux := http.NewServeMux()

	// 静的ファイル配信（フロントエンドのビルド成果物）
	mux.Handle("/", http.FileServer(http.Dir("frontend/dist")))

	// API エンドポイント（メソッド制約付き）
	mux.HandleFunc("/api/accounts", methodGuard(http.MethodGet, handleAccounts))
	mux.HandleFunc("/api/items", methodGuard(http.MethodGet, handleItems))
	mux.HandleFunc("/api/transactions", handleTransactions)
	mux.HandleFunc("/api/transactions/", handleTransactionByID)
	mux.HandleFunc("/api/balance_history", methodGuard(http.MethodGet, handleBalanceHistory))
	mux.HandleFunc("/api/balance_history_filtered", methodGuard(http.MethodGet, handleBalanceHistoryFiltered))
	mux.HandleFunc("/api/credit_card_settings", handleCreditCardSettings)
	mux.HandleFunc("/api/backup_csv", methodGuard(http.MethodGet, handleBackupCSV))
	mux.HandleFunc("/api/import_csv", methodGuard(http.MethodPost, handleImportCSV))
	mux.HandleFunc("/api/snapshots", handleSnapshots)
	mux.HandleFunc("/api/snapshots/restore", methodGuard(http.MethodPost, handleSnapshotRestore))

	// 画像API（Agent.md §6.5）
	mux.HandleFunc("/api/transaction_images/", handleTransactionImages)

	// タグAPI（Agent.md §6.6）
	mux.HandleFunc("/api/tags", handleTags)
	mux.HandleFunc("/api/tags/", handleTagByID)
	mux.HandleFunc("/api/tags/path", handleCreateTagByPath)
	mux.HandleFunc("/api/tags/summary", handleTagSummary)
	mux.HandleFunc("/api/transaction_tags/", handleTransactionTagsAPI)

	// AI専用エンドポイント（Agent.md §6.3: POST のみ許可）
	apiToken := os.Getenv("AI_API_TOKEN")
	aiMux := http.NewServeMux()
	aiMux.HandleFunc("/api/v1/ai/transactions", handleAITransactions)
	aiMux.HandleFunc("/api/v1/ai/analysis", handleAIAnalysis)
	mux.Handle("/api/v1/ai/", middleware.AIAPIMiddleware(apiToken, aiMux))

	// CORSミドルウェアで包む
	return corsMiddleware(mux)
}

// corsMiddleware はサーバーモードでブラウザからのクロスオリジンアクセスを許可する
func corsMiddleware(next http.Handler) http.Handler {
	allowedOrigins := make(map[string]struct{})
	for _, origin := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		origin = strings.TrimSpace(origin)
		if origin == "" || origin == "*" {
			continue
		}
		allowedOrigins[origin] = struct{}{}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		originAllowed := origin != "" && isOriginAllowed(origin, r.Host, allowedOrigins)
		if originAllowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if r.Method == http.MethodOptions {
			if origin != "" && !originAllowed {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isOriginAllowed(origin, host string, allowedOrigins map[string]struct{}) bool {
	if len(allowedOrigins) > 0 {
		_, ok := allowedOrigins[origin]
		return ok
	}

	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == host
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

func handleAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := core.GetAccounts()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, accounts, http.StatusOK)
}

func handleItems(w http.ResponseWriter, r *http.Request) {
	account := r.URL.Query().Get("account")
	items, err := core.GetItems(account)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, items, http.StatusOK)
}

func handleTransactions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		search := r.URL.Query().Get("search")
		account := r.URL.Query().Get("account")
		transactions, err := core.GetTransactions(account, search)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, transactions, http.StatusOK)

	case http.MethodPost:
		var req models.TransactionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		resp, err := core.AddTransaction(req)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"message":     "取引が正常に追加されました",
			"transaction": resp,
		}, http.StatusCreated)

	default:
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleTransactionByID(w http.ResponseWriter, r *http.Request) {
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
		resp, err := core.UpdateTransaction(id, req)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"message":     "取引が更新されました",
			"transaction": resp,
		}, http.StatusOK)

	case http.MethodDelete:
		if err := core.DeleteTransaction(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"message": "取引が削除されました"}, http.StatusOK)

	default:
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleBalanceHistory(w http.ResponseWriter, r *http.Request) {
	resp, err := core.GetBalanceHistory()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, resp, http.StatusOK)
}

func handleBalanceHistoryFiltered(w http.ResponseWriter, r *http.Request) {
	fundItems := r.URL.Query()["fund_items"]
	resp, err := core.GetBalanceHistoryFiltered(fundItems)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, resp, http.StatusOK)
}

func handleCreditCardSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := core.GetCreditCardSettings()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
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
		if err := core.SaveCreditCardSettings(body.CreditCardItems); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"message":           "クレジットカード設定を保存しました",
			"credit_card_items": body.CreditCardItems,
		}, http.StatusOK)

	default:
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleBackupCSV(w http.ResponseWriter, r *http.Request) {
	csvContent, err := core.BackupToCSV()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
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

	count, err := core.ImportCSV(body.Content, body.Mode)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
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

	var req models.TransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return
	}

	resp, err := core.AddTransaction(req)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"message":     "取引が正常に追加されました (AI API)",
		"transaction": resp,
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
		images, err := core.GetTransactionImages(txID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, images, http.StatusOK)

	case http.MethodPost:
		var img models.TransactionImageRequest
		if err := json.NewDecoder(r.Body).Decode(&img); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		resp, err := core.AddTransactionImage(txID, img)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
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
		if err := core.DeleteTransactionImage(imgID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"message": "画像を削除しました"}, http.StatusOK)

	default:
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// --- タグAPI ハンドラー (Agent.md §6.6) ---

func handleTags(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tags, err := core.GetTags()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
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
		tag, err := core.CreateTag(body.Name, body.ParentID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResponse(w, tag, http.StatusCreated)

	default:
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleCreateTagByPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return
	}
	tag, err := core.CreateTagByPath(body.Path)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
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
		if err := core.UpdateTag(id, body.Name); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"message": "タグを更新しました"}, http.StatusOK)

	case http.MethodDelete:
		if err := core.DeleteTag(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"message": "タグを削除しました"}, http.StatusOK)

	default:
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleTagSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	txType := r.URL.Query().Get("type")
	startDate := r.URL.Query().Get("start_date")
	endDate := r.URL.Query().Get("end_date")

	summaries, err := core.GetTagSummary(txType, startDate, endDate)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, summaries, http.StatusOK)
}

func handleTransactionTagsAPI(w http.ResponseWriter, r *http.Request) {
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
		tags, err := core.GetTransactionTags(txID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
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
		if err := core.AddTransactionTags(txID, body.TagIDs); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
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
		if err := core.RemoveTransactionTag(txID, tagID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"message": "タグを削除しました"}, http.StatusOK)

	default:
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// --- AI分析API ハンドラー (Agent.md §6.3) ---

func handleAIAnalysis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.AnalysisRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return
	}

	resp, err := core.AnalyzeTransactions(req)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, resp, http.StatusOK)
}
