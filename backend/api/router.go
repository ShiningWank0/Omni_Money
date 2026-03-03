// Package api はAPIの接続口定義と通信経路（ルーティング）を提供する
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"omni_money/backend/core"
	"omni_money/backend/middleware"
	"omni_money/backend/models"
)

// NewRouter はサーバーモード用のHTTPルーターを作成する
func NewRouter() http.Handler {
	mux := http.NewServeMux()

	// 静的ファイル配信（フロントエンドのビルド成果物）
	mux.Handle("/", http.FileServer(http.Dir("frontend/dist")))

	// API エンドポイント
	mux.HandleFunc("/api/accounts", handleAccounts)
	mux.HandleFunc("/api/items", handleItems)
	mux.HandleFunc("/api/transactions", handleTransactions)
	mux.HandleFunc("/api/transactions/", handleTransactionByID)
	mux.HandleFunc("/api/balance_history", handleBalanceHistory)
	mux.HandleFunc("/api/balance_history_filtered", handleBalanceHistoryFiltered)
	mux.HandleFunc("/api/credit_card_settings", handleCreditCardSettings)
	mux.HandleFunc("/api/backup_csv", handleBackupCSV)
	mux.HandleFunc("/api/import_csv", handleImportCSV)

	// AI専用エンドポイント（書き込み専用）
	apiToken := os.Getenv("AI_API_TOKEN")
	mux.Handle("/api/v1/ai/transactions",
		middleware.AIWriteOnlyMiddleware(apiToken, http.HandlerFunc(handleAITransactions)))

	return mux
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
	if r.Method != http.MethodPost {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
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

// --- ヘルパー ---

func jsonResponse(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, message string, status int) {
	jsonResponse(w, map[string]string{"error": message}, status)
}
